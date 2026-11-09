package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/syntax"
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
	var facts []liveActivity
	add := func(text string, kind liveActivityKind) {
		facts = append(facts, liveActivity{text: text, kind: kind, toolID: tool.ID})
	}
	switch tool.ToolName {
	case "compact_in_place":
		var input struct{ Action string }
		if json.Unmarshal(tool.ToolInput, &input) == nil && input.Action == "compact" {
			add(liveCompacting, liveCompactionStart)
		}
	case "subagent":
		add("Starting: delegating a task to a worker", liveInspect)
	case "apply_patch":
		if targets := livePatchTargets(tool.ToolInput); targets != "" {
			add("Applying patch: "+targets, liveEdit)
		}
	case "browser":
		var input struct{ Action, URL string }
		if json.Unmarshal(tool.ToolInput, &input) != nil {
			return nil
		}
		switch input.Action {
		case "navigate":
			u, err := url.Parse(input.URL)
			if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
				if name := livePaths([]string{path.Base(u.Path)}); name != "" {
					add("Starting: opening browser page"+name, liveInspect)
				}
			}
		case "eval":
			add("Starting: running browser JavaScript", liveInspect)
		case "screenshot":
			add("Starting: capturing a browser screenshot", liveInspect)
		}
	case "bash":
		var input struct {
			Command    string
			Background bool
		}
		if json.Unmarshal(tool.ToolInput, &input) != nil {
			return nil
		}
		steps, direct := liveShellProgress(input.Command)
		for _, step := range steps {
			if step == "" {
				continue
			}
			kind, prefix := liveInspect, "Starting: "
			if strings.HasPrefix(step, "go ") || strings.HasPrefix(step, "make ") || strings.HasPrefix(step, "pnpm ") {
				kind = liveCheck
			}
			if strings.HasPrefix(step, "Writing file ") {
				kind = liveEdit
			}
			if input.Background {
				kind, prefix = liveCheck, "Starting in background: "
			}
			// Deduplicate individual targets even when command batches change.
			verb, targets, _ := strings.Cut(step, " ")
			if verb == "Reading" || verb == "Searching" {
				for _, target := range strings.Split(targets, ", ") {
					add(prefix+verb+" "+target, kind)
				}
			} else {
				add(prefix+step, kind)
				facts[len(facts)-1].directCheck = direct && kind == liveCheck && !input.Background
			}
		}
	}
	return facts
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
		for _, header := range []struct{ prefix, operation string }{
			{"*** Add File: ", "add"}, {"*** Update File: ", "update"},
			{"*** Delete File: ", "delete"}, {"*** Move to: ", "move to"},
		} {
			name, ok := strings.CutPrefix(line, header.prefix)
			if !ok {
				continue
			}
			// Validate the entire path before discarding private directories.
			if livePaths([]string{strings.TrimPrefix(name, "/")}) == "" {
				continue
			}
			if path.IsAbs(name) {
				name = path.Base(name)
			}
			name = strings.TrimSpace(livePaths([]string{name}))
			if name != "" {
				target := header.operation + " " + name
				if !slices.Contains(targets, target) {
					targets = append(targets, target)
					if len(targets) == 3 {
						return strings.Join(targets, ", ")
					}
				}
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
		file := args[1]
		if path.Ext(file) == ".py" && !strings.HasPrefix(file, "-") && livePaths([]string{strings.TrimPrefix(file, "/")}) != "" {
			return "Python script in submitted command: " + path.Base(file)
		}
	case "sqlite3":
		// Describe only a literal SELECT. Never send the database path or SQL.
		sqlArgs := call.Args[1:]
		if len(sqlArgs) == 3 && sqlArgs[0].Lit() == "-json" {
			sqlArgs = sqlArgs[1:]
		}
		if len(sqlArgs) == 2 && path.Ext(sqlArgs[0].Lit()) == ".db" && len(sqlArgs[1].Parts) == 1 {
			var query string
			switch q := sqlArgs[1].Parts[0].(type) {
			case *syntax.SglQuoted:
				query = q.Value
			case *syntax.DblQuoted:
				if len(q.Parts) == 1 {
					if lit, ok := q.Parts[0].(*syntax.Lit); ok {
						query = lit.Value
					}
				}
			}
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT ") {
				return "Database lookup in submitted command"
			}
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
		operands := args[1:]
		if args[0] == "nl" && len(operands) > 0 && operands[0] == "-ba" {
			operands = operands[1:]
		}
		if names := livePaths(operands); names != "" {
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

// Bounded relative source paths, never absolute paths, dotfiles or secret names.
func livePaths(args []string) string {
	var paths []string
	for _, name := range args {
		name = strings.TrimPrefix(name, "./")
		if len(name) == 0 || len(name) > 100 || strings.HasPrefix(name, "/") {
			continue
		}
		lower := strings.ToLower(name)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "credential") || strings.Contains(lower, "password") {
			continue
		}
		valid := true
		for _, part := range strings.Split(strings.TrimSuffix(name, "/..."), "/") {
			if part == "" || strings.HasPrefix(part, ".") {
				valid = false
			}
		}
		for _, r := range name {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("/._-", r) {
				valid = false
			}
		}
		ext := path.Ext(name)
		if !slices.Contains([]string{".go", ".py", ".ts", ".tsx", ".vue", ".sql", ".md", ".css", ".html", ".org"}, ext) &&
			!slices.Contains([]string{"server", "ui", "ui/src", "llm", "db", "claudetool", "Makefile", "go.mod", "go.sum"}, name) &&
			!slices.Contains([]string{"server/...", "llm/...", "db/...", "claudetool/..."}, name) {
			valid = false
		}
		if valid && !slices.Contains(paths, name) {
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

type liveResultStatus struct {
	ExitCode   *int `json:"exitCode"`
	Background *struct {
		JobID string `json:"jobId"`
	} `json:"background"`
}

func liveToolStatus(tool llm.Content) liveResultStatus {
	var status liveResultStatus
	raw, err := json.Marshal(tool.Display)
	if err != nil {
		return status
	}
	if json.Unmarshal(raw, &status) != nil {
		return liveResultStatus{}
	}
	return status
}

const liveCompacting = "Starting compaction of this conversation’s older messages."

var oversizedHistoryRange = regexp.MustCompile(`(?m)^(?:- )?collapse [s0-9.]+-[s0-9.]+: covers ~[0-9]+ tokens; at most ~[0-9]+ per collapse unless it is a single row, so split it(?:\.|$)`)

// Only the compactor's own validation receipt can supply this reason. Never
// forward the range IDs, note contents, or arbitrary tool output.
func liveCompactionOutcome(tool llm.Content) string {
	if !tool.ToolError {
		return "Shortened this conversation’s history."
	}
	if len(tool.ToolResult) == 1 {
		receipt := tool.ToolResult[0].Text
		if strings.HasSuffix(receipt, compactRetryGuidance) {
			switch n := len(oversizedHistoryRange.FindAllStringIndex(receipt, -1)); n {
			case 0:
			case 1:
				return "This conversation’s history was not shortened. One history range was too large."
			default:
				return fmt.Sprintf("This conversation’s history was not shortened. %d history ranges were too large.", n)
			}
		}
	}
	return "The attempt to shorten this conversation’s history failed."
}

// Only structured status establishes completion. Ignore successful inspections:
// their exit status adds no useful development to the submitted activity.
func liveToolResult(tool llm.Content, started []liveActivity) []liveActivity {
	result := liveToolStatus(tool)
	var facts []liveActivity
	for _, activity := range started {
		description := strings.TrimPrefix(strings.TrimPrefix(activity.text, "Starting: "), "Starting in background: ")
		switch {
		case activity.kind == liveCompactionStart:
			activity.text, activity.kind = liveCompactionOutcome(tool), liveCheck
			if tool.ToolError {
				activity.kind = liveFailure
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
