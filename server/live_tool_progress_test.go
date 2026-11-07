package server

import (
	"encoding/json"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func TestLiveToolProgress(t *testing.T) {
	for _, tt := range []struct{ command, want string }{
		{"go test ./server -run Live", "go test server"},
		{"scripts/with-node pnpm --dir ui run type-check:vue", "pnpm run type-check:vue"},
		{"make build", "make build"},
		{"git fetch upstream", "git fetch (fetching repository updates)"},
		{"git log -5", "git log (checking commit history)"},
		{"git diff upstream/main -- server/live_call.go", "git diff (comparing changes) server/live_call.go"},
		{"cat server/live_call.go", "Reading server/live_call.go"},
		{"rg -- PRIVATE server/live_call.go", "Searching server/live_call.go"},
		{"cat .env /home/user/private.go secrets.go ../private.go token.ts", ""},
		{"git log -5; rg PRIVATE server", "git log (checking commit history); Searching server"},
		{"git diff | cat", "git diff (comparing changes)"},
		{"echo $(git diff)", ""},
		{"KEY=PRIVATE git diff", ""},
		{"git diff > private.txt", "git diff (comparing changes)"},
		{"curl -H 'Authorization: PRIVATE' https://example.com", ""},
		{"echo 'PRIVATE", ""},
		{"sed -n -e private.go server/live_call.go", ""},
		{"sed -n -f private.go", ""},
		{"cat $(echo private.go)", ""},
		{"go test -run private.go ./server", "go test"},
		{"rg -n 'APIKey|PRIVATE' llm/models.go llm/*.go | head -75; sed -n '1,170p' claudetool/llm_one_shot.go; sed -n '1,150p' claudetool/toolset.go", "Searching llm/models.go; Reading claudetool/llm_one_shot.go; Reading claudetool/toolset.go"},
		{"rg -n 'Image' claudetool --glob '*.go' | head -65", "Searching claudetool"},
		{"nl -ba server/handlers.go | sed -n '60,125p'", "Reading server/handlers.go"},
		{"cat > /home/private/reports/image-generation.org <<'EOF'\nPRIVATE CONTENT\ngit fetch SECRET\nEOF", "Writing file image-generation.org"},
		{"python3 - <<'PY'\nprint('git fetch PRIVATE')\nPY", ""},
		{"f() { git fetch PRIVATE; }; echo hi", ""},
		{"rg -g PRIVATE --type PRIVATE -n 'secret.go' server/live_call.go", "Searching server/live_call.go"},
		{"rg --unknown secret.go server", ""},
		{"cat > /private/token.org <<'EOF'\nPRIVATE\nEOF", ""},
	} {
		t.Run(tt.command, func(t *testing.T) {
			steps, _ := liveShellProgress(tt.command)
			expectEqual(t, "description", strings.Join(steps, "; "), tt.want)
		})
	}
	for _, tt := range []struct{ name, input, want string }{
		{"bash", `{"command":"make build","background":true}`, "Starting in background: make build"},
		{"bash", `{"command":"go test ./server"}`, "Starting: go test server"},
		{"bash", `{`, ""},
		{"apply_patch", `{"patch":"PRIVATE"}`, ""},
		{"browser", `{"url":"PRIVATE"}`, ""},
		{"browser", `{"action":"navigate","url":"http://localhost:8000/image-generation.html?token=PRIVATE"}`, "Starting: opening browser page image-generation.html"},
		{"browser", `{"action":"eval","expression":"PRIVATE"}`, "Starting: running browser JavaScript"},
		{"PRIVATE", `{"value":"PRIVATE"}`, ""},
	} {
		expectEqual(t, "tool", activityText(liveToolProgress(llm.Content{ToolName: tt.name, ToolInput: json.RawMessage(tt.input)})), tt.want)
	}
}

func TestLiveToolResultsUseMetadataOnly(t *testing.T) {
	for _, tt := range []struct {
		display string
		failed  bool
		want    string
	}{
		{`{"exitCode":0}`, false, "Shell exited with status 0; submitted step: go test server"},
		{`{"exitCode":2}`, false, "Shell exited with status 2; submitted step: go test server"},
		{`{"background":{"jobId":"j","logPath":"PRIVATE"}}`, false, "Running in background; submitted step: go test server"},
		{`{}`, false, ""},
		{`{}`, true, "Tool failed: go test server"},
	} {
		var display any
		if err := json.Unmarshal([]byte(tt.display), &display); err != nil {
			t.Fatal(err)
		}
		got := activityText(liveToolResult(llm.Content{Display: display, ToolError: tt.failed, ToolResult: llm.TextContent("PRIVATE OUTPUT")}, []liveActivity{{text: "Starting: go test server", kind: liveCheck, toolID: "test"}}))
		expectEqual(t, "result", got, tt.want)
	}
}

func TestLivePatchFactsUseOnlySafeHeaders(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"operations", "*** Begin Patch\n*** Add File: server/new.go\n+PRIVATE\n*** Delete File: ui/old.ts\n*** Update File: ui/a.vue\n*** Move to: ui/b.vue\n@@\n-PRIVATE\n+PRIVATE\n*** End Patch", "add server/new.go, delete ui/old.ts, update ui/a.vue"},
		{"absolute", "*** Begin Patch\n*** Update File: /home/person/work/server/live_call.go\n@@\n-PRIVATE\n+PRIVATE\n*** End Patch", "update live_call.go"},
		{"private paths", "*** Begin Patch\n*** Update File: /home/person/.hidden/a.go\n*** Update File: secret/a.go\n*** Update File: token.go\n*** Update File: ../a.go\n*** Update File: .env\n*** End Patch", ""},
		{"body headers", "*** Begin Patch\n*** Add File: server/new.go\n+*** Update File: stolen.go\n+PRIVATE\n*** End Patch", "add server/new.go"},
		{"no envelope", "*** Update File: server/a.go", ""},
		{"too large", "*** Begin Patch\n*** Add File: a.go\n+" + strings.Repeat("x", 1<<20) + "\n*** End Patch", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"input": tt.body})
			expectEqual(t, "targets", livePatchTargets(raw), tt.want)
		})
	}
	start := []liveActivity{{text: "Applying patch: update server/a.go", kind: liveEdit, toolID: "p"}}
	expectEqual(t, "success", activityText(liveToolResult(llm.Content{ToolUseID: "p"}, start)), "Applied patch: update server/a.go")
	failed := activityText(liveToolResult(llm.Content{ToolUseID: "p", ToolError: true, ToolResult: llm.TextContent("PRIVATE")}, start))
	expectEqual(t, "failure", failed, "Tool failed: Applying patch: update server/a.go")
}

func TestLiveReadSuccessIsSilentButFailuresAreNot(t *testing.T) {
	started := liveToolProgress(llm.Content{ID: "read", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"cat server/live_call.go"}`)})
	expectEqual(t, "success", len(liveToolResult(llm.Content{Display: map[string]any{"exitCode": 0}}, started)), 0)
	expectEqual(t, "nonzero", activityText(liveToolResult(llm.Content{Display: map[string]any{"exitCode": 1}}, started)), "Shell exited with status 1; submitted step: Reading server/live_call.go")
}
