package server

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

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
	liveFailure
	liveCompactionStart
)

type liveActivity struct {
	text        string
	kind        liveActivityKind
	toolID      string
	done        bool
	webLookup   bool
	directCheck bool
}

func (a liveActivity) quietShellExit() bool {
	return a.done && strings.HasPrefix(a.text, "Shell exited with status 0;")
}

func (a liveActivity) isOutcome() bool {
	return a.kind == liveFailure || a.done && (a.kind == liveEdit || a.kind == liveCheck)
}

func (a liveActivity) key() string {
	if a.webLookup && a.toolID != "" {
		return "web:" + a.toolID
	}
	if a.kind == liveInspect {
		for _, prefix := range []string{"Starting: Reading ", "Starting: Searching "} {
			if target, ok := strings.CutPrefix(a.text, prefix); ok {
				return "inspect:" + target
			}
		}
		return a.text
	}
	return fmt.Sprintf("%s:%t:%s", a.toolID, a.done, a.text)
}

// Describe supported inputs only; never copy scripts or arbitrary arguments.
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
		fact.text = "Starting: delegating a task to a worker"
	case "patch":
		target := strings.TrimSpace(livePaths([]string{input.Path}))
		if target == "" {
			return nil
		}
		fact.text, fact.kind = "Applying patch: "+target, liveEdit
	case "apply_patch":
		targets := livePatchTargets(tool.ToolInput)
		if targets == "" {
			return nil
		}
		fact.text, fact.kind = "Applying patch: "+targets, liveEdit
	case "browser":
		switch input.Action {
		case "navigate":
			fact.text = "Starting: opening a browser page"
		case "eval":
			fact.text = "Starting: running browser JavaScript"
		case "screenshot":
			fact.text = "Starting: capturing a browser screenshot"
		default:
			return nil
		}
	case "bash":
		steps, direct := liveShellProgress(input.Command)
		var facts []liveActivity
		for _, step := range steps {
			a := fact
			prefix := "Starting: "
			if strings.HasPrefix(step, "go ") || strings.HasPrefix(step, "make ") || strings.HasPrefix(step, "pnpm ") {
				a.kind = liveCheck
			}
			if strings.HasPrefix(step, "Writing file ") {
				a.kind = liveEdit
			}
			if input.Background {
				a.kind, prefix = liveCheck, "Starting in background: "
			}
			a.text, a.directCheck = prefix+step, direct && a.kind == liveCheck && !input.Background
			facts = append(facts, a)
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

func liveShellProgress(command string) ([]string, bool) {
	if len(command) > 64<<10 {
		return nil, false
	}
	program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, false
	}
	var notes []string
	// Walk executable top-level steps and pipelines only. Do not descend into
	// shell substitutions, function definitions, heredocs or script bodies.
	var step func(*syntax.Stmt)
	step = func(stmt *syntax.Stmt) {
		if len(notes) == 3 {
			return
		}
		switch cmd := stmt.Cmd.(type) {
		case *syntax.BinaryCmd:
			step(cmd.X)
			step(cmd.Y)
		case *syntax.CallExpr:
			if len(cmd.Assigns) != 0 {
				return
			}
			note := liveShellCall(cmd)
			if len(cmd.Args) == 1 && cmd.Args[0].Lit() == "cat" {
				for _, redirect := range stmt.Redirs {
					if redirect.Op == syntax.RdrOut {
						// Expose only the output basename, never its absolute directory or body.
						if name := livePaths([]string{path.Base(redirect.Word.Lit())}); name != "" {
							note = "Writing file" + name
						}
					}
				}
			}
			if note != "" && !slices.Contains(notes, note) {
				notes = append(notes, note)
			}
		}
	}
	for _, stmt := range program.Stmts {
		step(stmt)
	}
	direct := false
	if len(program.Stmts) == 1 {
		stmt := program.Stmts[0]
		_, simple := stmt.Cmd.(*syntax.CallExpr)
		direct = simple && !stmt.Negated && !stmt.Background && !stmt.Coprocess && !stmt.Disown
	}
	return notes, direct
}

func liveShellCall(call *syntax.CallExpr) string {
	var args []string
	for _, word := range call.Args {
		args = append(args, word.Lit())
	}
	if len(args) > 0 && args[0] == "scripts/with-node" {
		args = args[1:]
	}
	if len(args) < 2 {
		return ""
	}
	switch args[0] {
	case "python3":
		if path.Ext(args[1]) == ".py" && livePaths([]string{strings.TrimPrefix(args[1], "/")}) != "" {
			return "Running Python script " + path.Base(args[1])
		}
		if len(args) >= 3 && args[1] == "-m" && slices.Contains([]string{"unittest", "py_compile"}, args[2]) {
			return "Checking Python code with " + args[2]
		}
	case "nginx":
		if slices.Contains(args[1:], "-t") {
			return "Checking Nginx configuration"
		}
	case "git":
		switch args[1] {
		case "fetch":
			return "git fetch (fetching repository updates)"
		case "log":
			return "git log (checking commit history)"
		case "status":
			return "git status (checking working tree changes)"
		case "diff":
			note := "git diff (comparing changes)"
			if i := slices.Index(args, "--"); i >= 0 {
				note += livePaths(args[i+1:])
			}
			return note
		}
	case "cat", "nl":
		if names := livePaths(args[1:]); names != "" {
			return "Reading" + names
		}
	case "sed":
		// Only sed -n SCRIPT FILE; never quote the script itself.
		if len(args) == 4 && args[1] == "-n" && !strings.HasPrefix(args[2], "-") {
			if names := livePaths(args[3:]); names != "" {
				return "Reading" + names
			}
		}
	case "rg":
		operands := args[1:]
		files := false
	flags:
		for len(operands) > 0 && strings.HasPrefix(operands[0], "-") {
			flag := operands[0]
			operands = operands[1:]
			switch flag {
			case "--":
				break flags
			case "--files":
				files = true
			case "-n", "-l", "-i", "-S", "--hidden", "--no-heading":
			case "--glob", "-g", "--type", "-t":
				if len(operands) == 0 {
					return ""
				}
				operands = operands[1:]
			default:
				return ""
			}
		}
		if !files {
			if len(operands) == 0 {
				return ""
			}
			operands = operands[1:] // omit the search pattern, including quoted patterns
		}
		// A trailing flag's value is not a path.
		end := 0
		for end < len(operands) && !strings.HasPrefix(operands[end], "-") {
			end++
		}
		names := livePaths(operands[:end])
		if files {
			return "Listing repository files" + names
		}
		if names != "" {
			return "Searching" + names
		}
	case "go":
		if args[1] == "test" || args[1] == "build" || args[1] == "vet" {
			// Stop at the first flag: its value may look like a path.
			end := 2
			for end < len(args) && !strings.HasPrefix(args[end], "-") {
				end++
			}
			return "go " + args[1] + livePaths(args[2:end])
		}
	case "make":
		if len(args) == 2 && slices.Contains([]string{"build", "ui", "test", "lint", "templates", "exe-scroll"}, args[1]) {
			return "make " + args[1]
		}
	case "pnpm":
		rest := args[1:]
		if len(rest) >= 2 && rest[0] == "--dir" && rest[1] == "ui" {
			rest = rest[2:]
		}
		if len(rest) == 2 && rest[0] == "run" && slices.Contains([]string{"test", "build", "lint", "type-check", "type-check:vue"}, rest[1]) {
			return "pnpm run " + rest[1]
		}
	}
	return ""
}

// Accept only source paths; never forward arbitrary command arguments.
var liveSafePath = regexp.MustCompile(`^(?:[[:alnum:]_-]+/)*[[:alnum:]_-]+(?:\.(?:go|py|js|ts|tsx|vue|sql|md|css|html|org|json|conf(?:\.in)?|service)|/\.\.\.)?$`)

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
		description := strings.TrimPrefix(strings.TrimPrefix(activity.text, "Starting: "), "Starting in background: ")
		switch {
		case activity.kind == liveCompactionStart:
			activity.text, activity.kind = "Shortened this conversation’s history.", liveCheck
			if tool.ToolError {
				activity.text, activity.kind = "The attempt to shorten this conversation’s history failed.", liveFailure
			}
		case tool.ToolError:
			activity.text, activity.kind = "Tool failed: "+description, liveFailure
		case strings.HasPrefix(activity.text, "Applying patch: "):
			activity.text = "Applied patch: " + strings.TrimPrefix(activity.text, "Applying patch: ")
		case result.Background != nil && result.Background.JobID != "" && result.ExitCode == nil:
			activity.text, activity.kind = "Running in background; submitted step: "+description, liveCheck
		case result.ExitCode != nil && *result.ExitCode == 0 && activity.directCheck && tool.ToolUseStartTime != nil && tool.ToolUseEndTime != nil && tool.ToolUseEndTime.Sub(*tool.ToolUseStartTime) >= liveCheckDelay:
			activity.text = "Command finished successfully: " + description
		case result.ExitCode != nil && (*result.ExitCode != 0 || activity.kind != liveInspect):
			activity.text = fmt.Sprintf("Shell exited with status %d; submitted step: %s", *result.ExitCode, description)
			if *result.ExitCode != 0 {
				activity.kind = liveFailure
			}
		default:
			continue
		}
		activity.done = true
		facts = append(facts, activity)
	}
	if tool.ToolError && len(facts) == 0 {
		facts = append(facts, liveActivity{text: "A tool reported failure; that step has not succeeded.", kind: liveFailure, toolID: tool.ToolUseID, done: true})
	}
	return facts
}
