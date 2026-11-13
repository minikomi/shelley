package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/llm"
)

// Activity is bounded evidence, not prose inferred from command output.
type liveActivityKind int

const (
	liveInspect liveActivityKind = iota
	liveCheck
	liveEdit
	livePatch
	liveFailure
	liveCompactionStart
)

// Shell status is a process receipt. Live interprets its command operators.
type livePhase int

const (
	liveRequested livePhase = iota
	liveBackground
	liveFinished
	liveFailed
	liveShellExited
)

type liveActivity struct {
	text       string // safe description, without lifecycle wording
	kind       liveActivityKind
	phase      livePhase
	toolID     string
	dedupKey   string // semantic identity shared by multiple evidence sources
	exitCode   *int
	background bool
	webLookup  bool
	command    bool // text is a bounded shell command, not a translated activity
}

func (a liveActivity) quietShellExit() bool {
	return a.phase == liveShellExited && a.exitCode != nil && *a.exitCode == 0
}

func (a liveActivity) isOutcome() bool {
	return a.kind == liveFailure || a.phase >= liveFinished && a.kind >= liveCheck
}

func (a liveActivity) key() string {
	if a.webLookup && a.toolID != "" {
		return "web:" + a.toolID
	}
	if a.dedupKey != "" {
		return a.dedupKey
	}
	if a.kind == liveInspect {
		return a.text
	}
	return fmt.Sprintf("%s:%d:%s", a.toolID, a.phase, a.text)
}

// Format evidence once, when offering it to Live; no queue decisions parse prose.
func (a liveActivity) report() string {
	if a.kind == liveCompactionStart {
		switch a.phase {
		case liveFinished:
			return "Shortened this conversation’s history."
		case liveFailed:
			return "The attempt to shorten this conversation’s history failed."
		default:
			return a.text
		}
	}
	description := a.text
	if a.kind == livePatch {
		description = "Applying patch: " + a.text
		if a.phase == liveFinished {
			return "Applied patch: " + a.text
		}
	}
	if a.command {
		status := "submitted"
		switch a.phase {
		case liveBackground:
			status = "running in background"
		case liveFinished, liveShellExited:
			status = "exited"
		case liveFailed:
			status = "tool failed; completion unknown"
		}
		if a.exitCode != nil {
			status = fmt.Sprintf("exited; exit %d", *a.exitCode)
		}
		return "Command: " + a.text + "\nStatus: " + status
	}
	switch a.phase {
	case liveFailed:
		if a.background {
			return "A background command was lost; completion is unknown."
		}
		if description == "" {
			return "A tool reported failure; that step has not succeeded."
		}
		return "Tool failed: " + description
	case liveFinished:
		return description
	default:
		if a.kind == livePatch {
			return description
		}
		return "Starting: " + description
	}
}

// Only bounded commands and selected tool metadata enter the progress feed.
func liveToolProgress(tool llm.Content) []liveActivity {
	var input struct {
		Action, Command, Path string
		Background            bool
	}
	if json.Unmarshal(tool.ToolInput, &input) != nil {
		return nil
	}
	fact := liveActivity{toolID: tool.ID}
	switch tool.ToolName {
	case "compact_in_place":
		if input.Action != "compact" {
			return nil
		}
		fact.text, fact.kind = liveCompacting, liveCompactionStart
	case "subagent":
		fact.text = "delegating a task to a worker"
	case "patch":
		target := strings.TrimSpace(livePaths([]string{input.Path}))
		if target == "" {
			return nil
		}
		fact.text, fact.kind = target, livePatch
	case "apply_patch":
		targets := livePatchTargets(tool.ToolInput)
		if targets == "" {
			return nil
		}
		fact.text, fact.kind = targets, livePatch
	case "browser":
		switch input.Action {
		case "navigate":
			fact.text = "opening a browser page"
		case "eval":
			fact.text = "running browser JavaScript"
		case "screenshot":
			fact.text = "capturing a browser screenshot"
		default:
			return nil
		}
	case "bash":
		facts := liveShellProgress(input.Command)
		for i := range facts {
			a := &facts[i]
			a.toolID, a.background = tool.ID, input.Background
			if input.Background {
				a.kind = liveCheck
			}
		}
		return facts
	default:
		return nil
	}
	return []liveActivity{fact}
}

// Only envelope headers are evidence of intended edits. Hunk bodies never leave
// the extractor. Absolute paths expose only filtered basenames.
func livePatchTargets(raw json.RawMessage) string {
	var input struct{ Input string }
	if len(raw) > 1<<20 || json.Unmarshal(raw, &input) != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(input.Input), "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != "*** Begin Patch" || lines[len(lines)-1] != "*** End Patch" {
		return ""
	}
	var targets []string
	for _, line := range lines[1 : len(lines)-1] {
		for _, operation := range []string{"Add File", "Update File", "Delete File", "Move to"} {
			name, ok := strings.CutPrefix(line, "*** "+operation+": ")
			if !ok || livePaths([]string{strings.TrimPrefix(name, "/")}) == "" {
				continue
			}
			if path.IsAbs(name) {
				name = path.Base(name)
			}
			target := strings.ToLower(strings.TrimSuffix(operation, " File")) + " " + name
			if !slices.Contains(targets, target) {
				targets = append(targets, target)
			}
			if len(targets) == 3 {
				return strings.Join(targets, ", ")
			}
		}
	}
	return strings.Join(targets, ", ")
}

// Omit opaque commands rather than truncate code or forward embedded data.
const liveCommandChars = 400

var livePrivateCommand = regexp.MustCompile(`(?i)(authorization|bearer|password|passwd|secret|credential|token|api[-_]?key|private|sk-[a-z0-9]|\.env|://)`)
var liveSedRead = regexp.MustCompile(`^[0-9]+(?:,[0-9]+)?p$`)

func liveShellProgress(command string) []liveActivity {
	if len(command) > liveCommandChars*utf8.UTFMax {
		return nil
	}
	command = strings.TrimSpace(command)
	if utf8.RuneCountInString(command) > liveCommandChars || strings.ContainsAny(command, "\r\n") || strings.Contains(command, "<<") {
		return nil
	}
	program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	valid, check := true, false
	syntax.Walk(program, func(node syntax.Node) bool {
		if !valid {
			return false
		}
		switch n := node.(type) {
		case *syntax.Stmt:
			switch n.Cmd.(type) {
			case *syntax.CallExpr, *syntax.BinaryCmd:
			default:
				valid = false
			}
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp:
			valid = false
		case *syntax.CallExpr:
			if len(n.Assigns) != 0 {
				valid = false
				break
			}
			args := make([]string, len(n.Args))
			for i, word := range n.Args {
				args[i] = word.Lit()
				if args[i] == "" && len(word.Parts) == 1 {
					if quoted, ok := word.Parts[0].(*syntax.SglQuoted); ok {
						args[i] = quoted.Value
					}
				}
				if args[i] == "" || slices.Contains([]string{"-c", "-e", "--eval", "-args", "-ldflags", "-exec", "-toolexec", "--upload-pack", "--config-env", "--exec-path", "--ext-diff", "--textconv"}, strings.SplitN(args[i], "=", 2)[0]) {
					valid = false
				}
			}
			offset := 0
			if len(args) > 0 && path.Base(args[0]) == "with-node" {
				args = args[1:]
				offset = 1
			}
			if len(args) == 0 {
				valid = false
				break
			}
			name := path.Base(args[0])
			rest := args[1:]
			switch name {
			case "go":
				valid = valid && len(rest) > 0 && slices.Contains([]string{"test", "build", "vet"}, rest[0])
				check = true
			case "make":
				valid = valid && len(rest) > 0
				for _, arg := range rest {
					valid = valid && slices.Contains([]string{"build", "ui", "test", "lint", "templates", "exe-scroll"}, arg)
				}
				check = true
			case "pnpm":
				if len(rest) >= 2 && rest[0] == "--dir" {
					rest = rest[2:]
				}
				valid = valid && (len(rest) == 2 && rest[0] == "run" && slices.Contains([]string{"test", "build", "lint", "type-check", "type-check:vue"}, rest[1]) || len(rest) == 3 && rest[0] == "exec" && rest[1] == "tsx" && strings.HasSuffix(rest[2], ".test.ts"))
				check = true
			case "python3":
				valid = valid && (len(rest) == 1 && strings.HasSuffix(rest[0], ".py") || len(rest) >= 2 && rest[0] == "-m" && slices.Contains([]string{"unittest", "py_compile"}, rest[1]))
				check = true
			case "nginx":
				valid = valid && slices.Contains(rest, "-t")
				check = true
			case "git":
				for len(rest) >= 2 && rest[0] == "-C" {
					rest = rest[2:]
				}
				valid = valid && len(rest) > 0 && slices.Contains([]string{"fetch", "log", "status", "diff"}, rest[0])
			case "cat", "nl":
				for _, arg := range rest {
					if !strings.HasPrefix(arg, "-") {
						valid = valid && livePaths([]string{arg}) != ""
					}
				}
			case "sed":
				valid = valid && len(rest) == 3 && rest[0] == "-n" && liveSedRead.MatchString(rest[1]) && livePaths(rest[2:]) != ""
			case "rg":
				i := 1
				for i < len(args) && strings.HasPrefix(args[i], "-") {
					flag := args[i]
					i++
					switch flag {
					case "--files":
						return true
					case "--":
						goto pattern
					case "-n", "-l", "-i", "-S", "--hidden", "--no-heading":
					case "--glob", "-g", "--type", "-t":
						i++
					default:
						valid = false
					}
				}
			pattern:
				if i >= len(args) {
					valid = false
				} else {
					n.Args[i+offset] = &syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{Value: "search pattern omitted"}}}
				}
			case "ls", "pwd", "date", "read", "true", "false", "head", "tail":
			default:
				valid = false
			}
		}
		return valid
	})
	if !valid {
		return nil
	}
	var text bytes.Buffer
	if syntax.NewPrinter().Print(&text, program) != nil {
		return nil
	}
	command = strings.TrimSpace(text.String())
	if command == "" || utf8.RuneCountInString(command) > liveCommandChars || livePrivateCommand.MatchString(command) {
		return nil
	}
	kind := liveInspect
	if check {
		kind = liveCheck
	}
	return []liveActivity{{text: command, kind: kind, command: true}}
}

// Accept only source paths; never forward arbitrary command arguments.
var liveSafePath = regexp.MustCompile(`^(?:[[:alnum:]_-]+/)*[[:alnum:]_-]+(?:(?:\.[[:alnum:]_-]+)*\.(?:go|py|js|ts|tsx|vue|sql|md|css|html|org|json|conf(?:\.in)?|service)|/\.\.\.)?$`)

func livePaths(args []string) string {
	var paths []string
	for _, name := range args {
		name = strings.TrimPrefix(name, "./")
		lower := strings.ToLower(name)
		if len(name) > 100 || !liveSafePath.MatchString(strings.TrimPrefix(name, "/")) || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "credential") || strings.Contains(lower, "password") {
			continue
		}
		if path.IsAbs(name) {
			name = path.Base(name)
		}
		if path.Ext(name) == "" && !slices.Contains([]string{"server", "ui", "ui/src", "llm", "db", "claudetool", "Makefile"}, name) {
			continue
		}
		if !slices.Contains(paths, name) {
			paths = append(paths, name)
		}
		if len(paths) == 3 {
			break
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return " " + strings.Join(paths, ", ")
}

func liveToolStatus(tool llm.Content) claudetool.BashDisplayData {
	var status claudetool.BashDisplayData
	raw, err := json.Marshal(tool.Display)
	if err != nil {
		return status
	}
	if json.Unmarshal(raw, &status) != nil {
		return claudetool.BashDisplayData{}
	}
	return status
}

const liveCompacting = "Starting compaction of this conversation’s older messages."

// Only structured status establishes completion. Ignore successful inspections:
// their exit status adds no useful development to the submitted activity.
func liveToolResult(tool llm.Content, started []liveActivity) []liveActivity {
	result := liveToolStatus(tool)
	var facts []liveActivity
	for _, activity := range started {
		activity.dedupKey = ""
		activity.phase = liveFinished
		switch {
		case activity.kind == liveCompactionStart:
			if tool.ToolError {
				activity.phase = liveFailed
			}
		case tool.ToolError:
			if activity.kind == livePatch {
				activity.text = "Applying patch: " + activity.text
			}
			activity.phase, activity.kind, activity.background = liveFailed, liveFailure, false
		case activity.kind == livePatch:
		case result.Background != nil && result.Background.JobID != "" && result.ExitCode == nil:
			activity.phase, activity.kind, activity.background = liveBackground, liveCheck, true
		case result.ExitCode != nil && *result.ExitCode == 0 && activity.command && activity.kind == liveCheck && !activity.background && tool.ToolUseStartTime != nil && tool.ToolUseEndTime != nil && tool.ToolUseEndTime.Sub(*tool.ToolUseStartTime) >= liveCheckDelay:
			activity.exitCode = result.ExitCode
		case result.ExitCode != nil && (*result.ExitCode != 0 || activity.kind != liveInspect):
			activity.phase, activity.exitCode = liveShellExited, result.ExitCode
			if *result.ExitCode != 0 {
				activity.phase, activity.kind = liveFailed, liveFailure
			}
		default:
			continue
		}
		facts = append(facts, activity)
	}
	if tool.ToolError && len(facts) == 0 {
		facts = append(facts, liveActivity{kind: liveFailure, toolID: tool.ToolUseID, phase: liveFailed})
	}
	return facts
}

// Job completion is available before its notice reaches the agent's next turn.
// Both sources use the job identity so Live hears it once.
func liveBackgroundProgress(job backgroundJobUserData, toolID string) []liveActivity {
	if job.ExitCode == nil {
		return []liveActivity{{toolID: toolID, dedupKey: "background:" + job.BackgroundJobID, kind: liveFailure, phase: liveFailed, background: true}}
	}
	facts := liveShellProgress(job.Command)
	if len(facts) == 0 {
		return nil
	}
	fact := facts[0]
	fact.toolID, fact.dedupKey, fact.phase, fact.background, fact.exitCode, fact.kind = toolID, "background:"+job.BackgroundJobID, liveFinished, true, job.ExitCode, liveCheck
	if *job.ExitCode != 0 {
		fact.phase, fact.kind = liveFailed, liveFailure
	}
	return []liveActivity{fact}
}
