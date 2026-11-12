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
	livePatch
	liveFailure
	liveCompactionStart
)

// A shell exit proves the submitted shell finished, not that every listed step
// ran. Keep that receipt distinct from confirmed patch/check completion.
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
	direct     bool
	compound   bool
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
	if a.exitCode != nil {
		if !a.direct {
			label := "Combined shell"
			if a.background {
				label = "Combined background shell"
			}
			return fmt.Sprintf("%s exited with status %d.\nRequested steps: %q", label, *a.exitCode, description)
		}
		if a.background {
			return fmt.Sprintf("Background shell exited with status %d; submitted step: %s", *a.exitCode, description)
		}
		return fmt.Sprintf("Shell exited with status %d; submitted step: %s", *a.exitCode, description)
	}
	switch a.phase {
	case liveBackground:
		if !a.direct {
			return fmt.Sprintf("Submitted combined background shell.\nRequested steps: %q", description)
		}
		return "Running in background; submitted step: " + description
	case liveFailed:
		if a.background {
			return "A background command was lost; completion is unknown."
		}
		if a.compound {
			return fmt.Sprintf("Combined shell failed before a shell status was available.\nRequested steps: %q", description)
		}
		if description == "" {
			return "A tool reported failure; that step has not succeeded."
		}
		return "Tool failed: " + description
	case liveFinished:
		if a.direct && a.kind == liveCheck {
			return "Command finished successfully: " + description
		}
		return description
	default:
		if a.kind == livePatch {
			return description
		}
		if a.background {
			return "Submitted background shell; requested step: " + description
		}
		if a.compound {
			return fmt.Sprintf("Submitted combined shell.\nRequested steps: %q", description)
		}
		return "Starting: " + description
	}
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
		facts, direct := liveShellProgress(input.Command)
		for i := range facts {
			a := &facts[i]
			a.toolID, a.background = tool.ID, input.Background
			if input.Background {
				a.kind = liveCheck
			}
			a.direct = direct
			a.compound = !direct
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

func liveShellProgress(command string) ([]liveActivity, bool) {
	if len(command) > 64<<10 {
		return nil, false
	}
	program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, false
	}
	var notes []liveActivity
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
			if len(cmd.Args) == 1 && path.Base(cmd.Args[0].Lit()) == "cat" {
				for _, redirect := range stmt.Redirs {
					if redirect.Op == syntax.RdrOut {
						// Expose only the output basename, never its absolute directory or body.
						if name := livePaths([]string{path.Base(redirect.Word.Lit())}); name != "" {
							note = liveActivity{text: "Writing file" + name, kind: liveEdit}
						}
					}
				}
			}
			if note.text != "" && !slices.ContainsFunc(notes, func(a liveActivity) bool { return a.key() == note.key() }) {
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

func liveShellCall(call *syntax.CallExpr) liveActivity {
	var args []string
	for _, word := range call.Args {
		args = append(args, word.Lit())
	}
	if len(args) == 0 || args[0] == "" {
		return liveActivity{}
	}
	args[0] = path.Base(args[0])
	// Normalize only known wrappers. Directory/configuration values never become evidence.
	if args[0] == "git" {
		for len(args) >= 3 && args[1] == "-C" {
			args = append(args[:1], args[3:]...)
		}
	}
	if len(args) < 2 {
		return liveActivity{}
	}
	switch args[0] {
	case "python", "python3":
		if len(args) >= 3 && args[1] == "-m" && slices.Contains([]string{"unittest", "py_compile"}, args[2]) {
			return liveActivity{text: "Checking Python code with " + args[2], kind: liveCheck}
		}
		if args[1] == "-" || !strings.HasPrefix(args[1], "-") && strings.HasSuffix(args[1], ".py") {
			return liveActivity{text: "Running a Python script", kind: liveCheck}
		}
	case "nginx":
		if slices.Contains(args[1:], "-t") {
			return liveActivity{text: "Checking Nginx configuration", kind: liveCheck}
		}
	case "git":
		switch args[1] {
		case "fetch":
			return liveActivity{text: "git fetch (fetching repository updates)"}
		case "log":
			return liveActivity{text: "git log (checking commit history)"}
		case "status":
			return liveActivity{text: "git status (checking working tree changes)"}
		case "diff":
			note := "git diff (comparing changes)"
			if i := slices.Index(args, "--"); i >= 0 {
				note += livePaths(args[i+1:])
			}
			return liveActivity{text: note}
		}
	case "cat", "nl":
		if names := livePaths(args[1:]); names != "" {
			return liveActivity{text: "Reading" + names, dedupKey: "inspect:" + strings.TrimSpace(names)}
		}
	case "sed":
		// Only sed -n SCRIPT FILE; never quote the script itself.
		if len(args) == 4 && args[1] == "-n" && !strings.HasPrefix(args[2], "-") {
			if names := livePaths(args[3:]); names != "" {
				return liveActivity{text: "Reading" + names, dedupKey: "inspect:" + strings.TrimSpace(names)}
			}
		}
	case "rg":
		if args[1] == "--files" {
			return liveActivity{text: "Listing files with rg"}
		}
		return liveActivity{text: "Searching files with rg"}
	case "go":
		if args[1] == "test" || args[1] == "build" || args[1] == "vet" {
			// Stop at the first flag: its value may look like a path.
			end := 2
			for end < len(args) && !strings.HasPrefix(args[end], "-") {
				end++
			}
			return liveActivity{text: "go " + args[1] + livePaths(args[2:end]), kind: liveCheck}
		}
	case "make", "pnpm", "npm":
		rest := args[1:]
		if args[0] != "make" && len(rest) > 0 && rest[0] == "run" {
			rest = rest[1:]
		}
		if len(rest) == 1 && slices.Contains([]string{"build", "test", "lint", "check", "type-check"}, rest[0]) {
			return liveActivity{text: args[0] + " " + strings.Join(args[1:], " "), kind: liveCheck}
		}
	}
	return liveActivity{}
}

// Accept bounded file paths and package directories; never forward arbitrary arguments.
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
		case tool.ToolError && result.ExitCode == nil:
			if activity.kind == livePatch {
				activity.text = "Applying patch: " + activity.text
			}
			activity.phase, activity.kind, activity.background = liveFailed, liveFailure, false
		case activity.kind == livePatch:
		case result.Background != nil && result.Background.JobID != "" && result.ExitCode == nil:
			activity.phase, activity.kind, activity.background = liveBackground, liveCheck, true
		case result.ExitCode != nil && *result.ExitCode == 0 && activity.direct && activity.kind == liveCheck && !activity.background && tool.ToolUseStartTime != nil && tool.ToolUseEndTime != nil && tool.ToolUseEndTime.Sub(*tool.ToolUseStartTime) >= liveCheckDelay:
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
	fact := liveActivity{toolID: toolID, dedupKey: "background:" + job.BackgroundJobID, phase: liveFinished, background: true, exitCode: job.ExitCode, kind: liveCheck}
	if job.ExitCode == nil {
		fact.phase, fact.kind = liveFailed, liveFailure
	} else {
		steps, direct := liveShellProgress(job.Command)
		fact.direct, fact.compound = direct, !direct
		var descriptions []string
		for _, step := range steps {
			descriptions = append(descriptions, step.text)
		}
		fact.text = strings.Join(descriptions, "; ")
		if *job.ExitCode != 0 {
			fact.phase, fact.kind = liveFailed, liveFailure
		}
	}
	if fact.phase == liveFinished && fact.text == "" {
		return nil
	}
	return []liveActivity{fact}
}
