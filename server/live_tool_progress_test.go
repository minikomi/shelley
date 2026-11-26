package server

import (
	"encoding/json"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func TestLiveShortCommandEvidence(t *testing.T) {
	for _, command := range []string{
		"go test ./server -run Live", "scripts/with-node pnpm --dir ui run type-check && scripts/with-node pnpm --dir ui run type-check:vue && scripts/with-node pnpm --dir ui run lint",
		"make templates && make exe-scroll && go build -o bin/shelley ./cmd/shelley", "false && go test ./server; true", "go test ./server | cat", "! go test ./server",
		"git fetch upstream", "git diff -- server/live_call.go", "cat server/live_call.go", "python3 -m unittest test_app", "python3 /tmp/replay.py", "nginx -t",
	} {
		t.Run(command, func(t *testing.T) {
			facts := liveShellProgress(command)
			if len(facts) != 1 || !facts[0].command {
				t.Fatalf("missing command evidence: %v", facts)
			}
			if !strings.HasPrefix(facts[0].report(), "Command: ") || !strings.Contains(facts[0].report(), "Status: submitted") {
				t.Fatal(facts[0].report())
			}
			if strings.Contains(command, "&&") && !strings.Contains(facts[0].text, "&&") {
				t.Fatal("lost shell operators")
			}
		})
	}
	for _, command := range []string{
		"python3 - <<'PY'\nprint('private body')\nPY", "python3 -c 'print(123)'", "node -e 'print(123)'", "bash -c 'go test ./server'",
		"cat .env", "cat .hidden/a.go", "cat password.go", "curl -H 'Authorization: abc' https://example.com", "go test -args abc", "go test -ldflags=inline",
		"KEY=value git diff", "cat $(echo a.go)", "cat <(echo a.go)", "cat $HOME/a.go", "f() { git diff; }; f", "if true; then git diff; fi", "echo 'unterminated",
		"python3 " + strings.Repeat("x", liveCommandChars),
	} {
		if facts := liveShellProgress(command); len(facts) != 0 {
			t.Errorf("forwarded opaque input %q: %v", command, facts)
		}
	}
	for _, command := range []string{"rg -n 'PRIVATE FindThisSymbol' server/live_call.go | head -75", "scripts/with-node rg -n 'PRIVATE FindThisSymbol' server/live_call.go"} {
		facts := liveShellProgress(command)
		if len(facts) != 1 || (strings.Contains(facts[0].text, "FindThisSymbol") || strings.Contains(facts[0].text, "PRIVATE")) || !strings.Contains(facts[0].text, "search pattern omitted") {
			t.Fatalf("search data escaped filtering: %v", facts)
		}
	}
}

func TestLiveToolResultsUseMetadataOnly(t *testing.T) {
	for _, tt := range []struct {
		display string
		failed  bool
		want    string
		phase   livePhase
	}{
		{`{"exitCode":0}`, false, "Command: go test ./server\nStatus: exited; exit 0", liveShellExited},
		{`{"exitCode":2}`, false, "Command: go test ./server\nStatus: exited; exit 2", liveFailed},
		{`{"background":{"jobId":"j","logPath":"PRIVATE"}}`, false, "Command: go test ./server\nStatus: running in background", liveBackground},
		{`{}`, false, "", liveRequested},
		{`{}`, true, "Command: go test ./server\nStatus: tool failed; completion unknown", liveFailed},
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
	start := []liveActivity{{text: "update server/a.go", kind: livePatch, toolID: "p"}}
	expectEqual(t, "success", activityText(liveToolResult(llm.Content{ToolUseID: "p"}, start)), "Applied patch: update server/a.go")
	failed := activityText(liveToolResult(llm.Content{ToolUseID: "p", ToolError: true, ToolResult: llm.TextContent("PRIVATE")}, start))
	expectEqual(t, "failure", failed, "Tool failed: Applying patch: update server/a.go")
}

func TestLiveReadSuccessIsSilentButFailuresAreNot(t *testing.T) {
	started := liveToolProgress(llm.Content{ID: "read", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"cat server/live_call.go"}`)})
	expectEqual(t, "success", len(liveToolResult(llm.Content{Display: map[string]any{"exitCode": 0}}, started)), 0)
	expectEqual(t, "nonzero", activityText(liveToolResult(llm.Content{Display: map[string]any{"exitCode": 1}}, started)), "Command: cat server/live_call.go\nStatus: exited; exit 1")
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

func TestLiveBackgroundCompletionUsesWholeCommand(t *testing.T) {
	for _, command := range []string{"go test ./server", "go test ./server && go vet ./server", "false && go test ./server; true", "go test ./server | cat"} {
		t.Run(command, func(t *testing.T) {
			started := liveShellProgress(command)
			running := liveToolResult(llm.Content{Display: map[string]any{"background": map[string]any{"jobId": "job"}}}, started)
			finished := liveBackgroundProgress(backgroundJobUserData{BackgroundJobID: "job", Command: command, ExitCode: new(int)}, "shell")
			expectEqual(t, "one running receipt", len(running), 1)
			expectEqual(t, "one completion receipt", len(finished), 1)
			expectEqual(t, "same command", finished[0].text, started[0].text)
			if !strings.Contains(finished[0].report(), "Status: exited; exit 0") || strings.Contains(finished[0].report(), "unconfirmed") || strings.Contains(finished[0].report(), "passed") {
				t.Fatal(finished[0].report())
			}
		})
	}
}
