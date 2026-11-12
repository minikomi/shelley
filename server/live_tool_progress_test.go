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
		{"pnpm run lint", "pnpm run lint"},
		{"make build", "make build"},
		{"npm test", "npm test"},
		{"go test ./payments/...", "go test payments/..."},
		{"python3 /tmp/work/task.py --key PRIVATE", "Running a Python script"},
		{"/usr/bin/git -C \"$PRIVATE\" -C /another/repo diff -- server/live_call.go", "git diff (comparing changes) server/live_call.go"},
		{"rg -- PRIVATE server/live_call.go", "Searching files with rg"},
		{"cat .env /home/user/private.go secrets.go ../private.go token.ts", "Reading private.go"},
		{"echo $(git diff)", ""},
		{"curl -H 'Authorization: PRIVATE' https://example.com", ""},
		{"sed -n '1,50p' inject.conf.in", "Reading inject.conf.in"},
		{"python3 -m unittest -v test_configure", "Checking Python code with unittest"},
		{"nginx -t -c /private/nginx.conf", "Checking Nginx configuration"},
		{"cat > /home/private/reports/report.md <<'EOF'\nPRIVATE CONTENT\ngit fetch SECRET\nEOF", "Writing file report.md"},
		{"python3 - <<'PY'\nprint('git fetch PRIVATE')\nPY", "Running a Python script"},
	} {
		t.Run(tt.command, func(t *testing.T) {
			steps, _ := liveShellProgress(tt.command)
			var descriptions []string
			for _, step := range steps {
				descriptions = append(descriptions, step.text)
			}
			expectEqual(t, "description", strings.Join(descriptions, "; "), tt.want)
		})
	}
	for _, tt := range []struct{ name, input, want string }{
		{"bash", `{"command":"make build","background":true}`, "Submitted background shell; requested step: make build"},
		{"bash", `{"command":"go test ./server"}`, "Starting: go test server"},
		{"bash", `{`, ""},
		{"apply_patch", `{"patch":"PRIVATE"}`, ""},
		{"patch", `{"path":"nginx.conf","patches":[{"newText":"PRIVATE"}]}`, "Applying patch: nginx.conf"},
		{"patch", `{"path":"/home/person/app.py","patches":[{"newText":"PRIVATE"}]}`, "Applying patch: app.py"},
		{"patch", `{"path":".env","patches":[{"newText":"PRIVATE"}]}`, ""},
		{"browser", `{"url":"PRIVATE"}`, ""},
		{"browser", `{"action":"navigate","url":"http://localhost:8000/index.html?token=PRIVATE"}`, "Starting: opening a browser page"},
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
		phase   livePhase
	}{
		{`{"exitCode":0}`, false, "Shell exited with status 0; submitted step: go test server", liveShellExited},
		{`{"exitCode":2}`, false, "Shell exited with status 2; submitted step: go test server", liveFailed},
		{`{"exitCode":1}`, true, "Shell exited with status 1; submitted step: go test server", liveFailed},
		{`{"background":{"jobId":"j","logPath":"PRIVATE"}}`, false, "Running in background; submitted step: go test server", liveBackground},
		{`{}`, false, "", liveRequested},
		{`{}`, true, "Tool failed: go test server", liveFailed},
	} {
		var display any
		if err := json.Unmarshal([]byte(tt.display), &display); err != nil {
			t.Fatal(err)
		}
		facts := liveToolResult(llm.Content{Display: display, ToolError: tt.failed, ToolResult: llm.TextContent("PRIVATE OUTPUT")}, liveToolProgress(llm.Content{ID: "test", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"go test ./server"}`)}))
		got := activityText(facts)
		if len(facts) > 0 {
			expectEqual(t, "phase", facts[0].phase, tt.phase)
		}
		expectEqual(t, "result", got, tt.want)
	}
}

func TestLiveForegroundCompoundCommandsReportShellLifecycle(t *testing.T) {
	for _, command := range []string{"false && go test ./server", "true || go test ./server", "go test ./server | cat"} {
		t.Run(command, func(t *testing.T) {
			input, err := json.Marshal(map[string]string{"command": command})
			if err != nil {
				t.Fatal(err)
			}
			started := liveToolProgress(llm.Content{ID: "shell", ToolName: "bash", ToolInput: input})
			for _, code := range []int{0, 1} {
				finished := liveToolResult(llm.Content{Display: map[string]any{"exitCode": code}, ToolResult: llm.TextContent("PRIVATE")}, started)
				got := activityText(finished)
				if !strings.Contains(got, "Combined shell exited with status") || !strings.Contains(got, `Requested steps: "go test server"`) || strings.Contains(got, "successfully") || strings.Contains(got, "PRIVATE") {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestLivePatchFactsUseOnlySafeHeaders(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"operations", "*** Begin Patch\n*** Add File: server/new.go\n+PRIVATE\n*** Delete File: ui/old.ts\n*** Update File: ui/a.vue\n*** Move to: ui/b.vue\n@@\n-PRIVATE\n+PRIVATE\n*** End Patch", "add server/new.go, delete ui/old.ts, update ui/a.vue"},
		{"private paths", "*** Begin Patch\n*** Update File: /home/person/.hidden/a.go\n*** Update File: secret/a.go\n*** Update File: token.go\n*** Update File: ../a.go\n*** Update File: .env\n*** End Patch", ""},
		{"body headers", "*** Begin Patch\n*** Add File: server/new.go\n+*** Update File: stolen.go\n+PRIVATE\n*** End Patch", "add server/new.go"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"input": tt.body})
			expectEqual(t, "targets", livePatchTargets(raw), tt.want)
		})
	}
	start := []liveActivity{{text: "update server/a.go", kind: livePatch, toolID: "p"}}
	expectEqual(t, "success", activityText(liveToolResult(llm.Content{ToolUseID: "p"}, start)), "Applied patch: update server/a.go")
	failed := activityText(liveToolResult(llm.Content{ToolUseID: "p", ToolError: true, ToolResult: llm.TextContent("PRIVATE")}, start))
	expectEqual(t, "failure", failed, "Tool failed: Applying patch: update server/a.go")
}

func TestLiveReadSuccessIsSilentButFailuresAreNot(t *testing.T) {
	started := liveToolProgress(llm.Content{ID: "read", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"cat server/live_call.go"}`)})
	expectEqual(t, "success", len(liveToolResult(llm.Content{Display: map[string]any{"exitCode": 0}}, started)), 0)
	expectEqual(t, "nonzero", activityText(liveToolResult(llm.Content{Display: map[string]any{"exitCode": 1}}, started)), "Shell exited with status 1; submitted step: Reading server/live_call.go")
}

func TestLiveCompactionResultIgnoresOutput(t *testing.T) {
	started := liveToolProgress(llm.Content{ToolName: "compact_in_place", ToolInput: json.RawMessage(`{"action":"compact"}`)})
	for _, failed := range []bool{false, true} {
		want := "Shortened this conversation’s history."
		if failed {
			want = "The attempt to shorten this conversation’s history failed."
		}
		expectEqual(t, "outcome", activityText(liveToolResult(llm.Content{ToolError: failed, ToolResult: llm.TextContent("PRIVATE")}, started)), want)
	}
}

func TestLiveBackgroundCompletionUsesWholeShellEvidence(t *testing.T) {
	finished := liveBackgroundProgress(backgroundJobUserData{BackgroundJobID: "job", Command: "false && go test ./server; true", ExitCode: new(int)}, "shell")
	expectEqual(t, "shell success does not prove the tests ran", activityText(finished), "Combined background shell exited with status 0.\nRequested steps: \"go test server\"")
}

func TestLivePythonScriptMetadata(t *testing.T) {
	for _, command := range []string{"python3 - <<'PY'\nprint('PRIVATE BODY')\nPY", "python /tmp/private/script.py --key PRIVATE"} {
		input, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		tool := llm.Content{ID: "first", ToolName: "bash", ToolInput: input}
		facts := liveToolProgress(tool)
		expectEqual(t, "script activity", activityText(facts), "Starting: Running a Python script")
		tool.ID = "second"
		next := liveToolProgress(tool)
		if facts[0].key() == next[0].key() {
			t.Fatal("distinct scripts share an identity")
		}
		expectEqual(t, "same invocation deduplicates", next[0].key(), liveToolProgress(tool)[0].key())
	}
}
