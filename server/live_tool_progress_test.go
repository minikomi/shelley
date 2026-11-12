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
		{"make ui", "make ui"},
		{"make build", "make build"},
		{"npm test", "npm run test"},
		{"go test ./payments/...", "go test payments/..."},
		{"python3 /tmp/work/task.py --key PRIVATE", "Running a Python script"},
		{"/usr/bin/git -C \"$PRIVATE\" -C /another/repo diff -- server/live_call.go", "git diff (comparing changes) server/live_call.go"},
		{"rg -- PRIVATE server/live_call.go", "Searching server/live_call.go"},
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
		{"bash", `{"command":"make build","background":true}`, "Action: make build\nState: submitted; result pending; success is not yet known\nExecution: background"},
		{"bash", `{"command":"go test ./server"}`, "Action: go test server\nState: submitted; result pending; success is not yet known"},
		{"bash", `{`, ""},
		{"apply_patch", `{"patch":"PRIVATE"}`, ""},
		{"patch", `{"path":"nginx.conf","patches":[{"newText":"PRIVATE"}]}`, "Action: patch: nginx.conf\nState: submitted; result pending; success is not yet known"},
		{"patch", `{"path":"/home/person/app.py","patches":[{"newText":"PRIVATE"}]}`, "Action: patch: app.py\nState: submitted; result pending; success is not yet known"},
		{"patch", `{"path":".env","patches":[{"newText":"PRIVATE"}]}`, ""},
		{"browser", `{"url":"PRIVATE"}`, ""},
		{"browser", `{"action":"navigate","url":"http://localhost:8000/index.html?token=PRIVATE"}`, "Action: opening a browser page\nState: submitted; result pending; success is not yet known"},
		{"browser", `{"action":"eval","expression":"PRIVATE"}`, "Action: running browser JavaScript\nState: submitted; result pending; success is not yet known"},
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
		{`{"exitCode":0}`, false, "Action: go test server\nState: shell exited with status 0", liveShellExited},
		{`{"exitCode":2}`, false, "Action: go test server\nState: shell exited with status 2", liveFailed},
		{`{"exitCode":1}`, true, "Action: go test server\nState: shell exited with status 1", liveFailed},
		{`{"background":{"jobId":"j","logPath":"PRIVATE"}}`, false, "Action: go test server\nState: running; result pending; success is not yet known\nExecution: background", liveBackground},
		{`{}`, false, "", liveRequested},
		{`{}`, true, "Action: go test server\nState: failed", liveFailed},
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
				if !strings.Contains(got, "State: shell exited with status") || !strings.Contains(got, "Action: go test server") || !strings.Contains(got, "not proof each step ran") || strings.Contains(got, "successfully") || strings.Contains(got, "PRIVATE") {
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
	expectEqual(t, "success", activityText(liveToolResult(llm.Content{ToolUseID: "p"}, start)), "Action: patch: update server/a.go\nState: completed")
	failed := activityText(liveToolResult(llm.Content{ToolUseID: "p", ToolError: true, ToolResult: llm.TextContent("PRIVATE")}, start))
	expectEqual(t, "failure", failed, "Action: patch: update server/a.go\nState: failed")
}

func TestLiveReadSuccessIsSilentButFailuresAreNot(t *testing.T) {
	started := liveToolProgress(llm.Content{ID: "read", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"cat server/live_call.go"}`)})
	expectEqual(t, "success", len(liveToolResult(llm.Content{Display: map[string]any{"exitCode": 0}}, started)), 0)
	expectEqual(t, "nonzero", activityText(liveToolResult(llm.Content{Display: map[string]any{"exitCode": 1}}, started)), "Action: Reading server/live_call.go\nState: shell exited with status 1")
}

func TestLiveCompactionResultIgnoresOutput(t *testing.T) {
	started := liveToolProgress(llm.Content{ToolName: "compact_in_place", ToolInput: json.RawMessage(`{"action":"compact"}`)})
	for _, failed := range []bool{false, true} {
		want := "Action: " + liveCompacting + "\nState: completed"
		if failed {
			want = "Action: " + liveCompacting + "\nState: failed"
		}
		expectEqual(t, "outcome", activityText(liveToolResult(llm.Content{ToolError: failed, ToolResult: llm.TextContent("PRIVATE")}, started)), want)
	}
}

func TestLiveBackgroundCompletionUsesWholeShellEvidence(t *testing.T) {
	finished := liveBackgroundProgress(backgroundJobUserData{BackgroundJobID: "job", Command: "false && go test ./server; true", ExitCode: new(int)}, "shell")
	expectEqual(t, "shell success does not prove the tests ran", activityText(finished), "Action: go test server\nState: shell exited with status 0\nScope: combined shell; listed actions are requested steps, not proof each step ran.\nExecution: background")
}

func TestLivePythonScriptMetadata(t *testing.T) {
	for _, command := range []string{"python3 - <<'PY'\nprint('PRIVATE BODY')\nPY", "python /tmp/private/script.py --key PRIVATE"} {
		input, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		tool := llm.Content{ID: "first", ToolName: "bash", ToolInput: input}
		facts := liveToolProgress(tool)
		expectEqual(t, "script activity", activityText(facts), "Action: Running a Python script\nState: submitted; result pending; success is not yet known")
		tool.ID = "second"
		next := liveToolProgress(tool)
		if facts[0].key() == next[0].key() {
			t.Fatal("distinct scripts share an identity")
		}
		expectEqual(t, "same invocation deduplicates", next[0].key(), liveToolProgress(tool)[0].key())
	}
}
