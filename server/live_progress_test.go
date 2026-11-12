package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func progressMessage(t *testing.T, typ string, message llm.Message) generated.Message {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	return generated.Message{Type: typ, LlmData: &raw}
}

func TestLiveProgressFiltersRows(t *testing.T) {
	result := func(failed bool, text string) llm.Content {
		return llm.Content{Type: llm.ContentTypeToolResult, ToolError: failed, ToolResult: []llm.Content{llm.StringContent(text)}}
	}
	message := func(typ string, content ...llm.Content) generated.Message {
		return progressMessage(t, typ, llm.Message{Content: content})
	}
	excluded := dialogueRow("agent", "do not forward", false, "")
	excluded.ExcludedFromContext = true
	bad := "{"
	for _, tt := range []struct {
		name     string
		row      generated.Message
		notes    string
		activity string
		end      bool
	}{
		{"typed text", dialogueRow("user", "Please fix it", false, ""), "", "", false},
		{"subagent report", dialogueRow("user", "helper report", false, `{"sender_conversation_id":"child","sender_relationship":"subagent"}`), "", "", false},
		{"agent notes", dialogueRow("agent", " Found the stale cache. ", false, ""), "notes: Found the stale cache.", "", false},
		{"successful result omitted", message("user", result(false, "private successful output")), "", "", false},
		{"failed result output omitted", message("user", llm.StringContent("not a finding"), result(true, "PRIVATE OUTPUT")), "", "Action: tool invocation\nState: failed", false},
		{"private thinking omitted", message("agent", llm.Content{Type: llm.ContentTypeThinking, Text: "PRIVATE REASONING"}), "", "", false},
		{"final reply", dialogueRow("agent", "Done", true, ""), "", "", true},
		{"excluded row", excluded, "", "", false},
		{"terminal error", dialogueRow("error", "PRIVATE failure", true, ""), "", "", true},
		{"invalid data", generated.Message{Type: "agent", LlmData: &bad}, "", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			notes, activity, end := liveProgress(tt.row, nil)
			expectEqual(t, "notes", strings.Join(notes, "\n"), tt.notes)
			expectEqual(t, "silent activity", activityText(activity), tt.activity)
			expectEqual(t, "end of turn", end, tt.end)
		})
	}
}

const testLiveProgressEvery = 12 * time.Second

func newLiveProgressHarness(t *testing.T, enabled bool) *liveHarness {
	t.Helper()
	h := newLiveHarness(t)
	h.call.progressEnabled = enabled
	_, detach := h.call.attach()
	t.Cleanup(detach)
	return h
}

func startLiveProgressTask(h *liveHarness) {
	h.say("Run the tests", 0, 500)
	h.delegate("d1", 500)
	h.advance(liveQuiet)
	h.call.observe([]liveRow{{seq: 11, user: true, text: "Run the tests", liveDeliveryID: "delivery-1"}})
}

func TestLiveProgressDefaultsToDisabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		_, detach := h.call.attach()
		t.Cleanup(detach)
		startLiveProgressTask(h)
		before := h.context()
		notes, activity, final := liveProgress(progressMessage(t, "agent", llm.Message{EndOfTurn: true, ExcludedFromContext: true}), nil)
		h.call.observe([]liveRow{{seq: 12, progress: []string{"silent notes"}, activity: progressActivities("Running tests or code checks.")}, {seq: 13, progress: notes, activity: activity, endOfTurn: final}})
		h.advance(3 * time.Minute)
		expectEqual(t, "Never adds no context", h.context(), before)
		if h.call.tick != nil || len(h.call.progressNotes) != 0 || len(h.call.progressActivity) != 0 {
			t.Error("Never started a timer or collected notes")
		}
		if h.call.submitted[0].finished {
			t.Fatal("Never changed baseline empty-marker handling")
		}
		h.call.observe([]liveRow{{seq: 14, text: "Done", endOfTurn: true}})
		got := h.context()
		expectEqual(t, "final reply retains delegation", got[len(got)-1], "commentary [d1]: Backend reply: Done")
		if !h.call.submitted[0].finished {
			t.Error("final reply did not finish delegation")
		}
	})
}

func TestLiveProgressWorkingLifecycle(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveProgressHarness(t, false)
				if enabled {
					h.call.progressEnabled = true
				}
				h.call.observeWorking(true)
				h.call.observe([]liveRow{{seq: 11, activity: progressActivities("Starting: make build")}})
				h.advance(testLiveProgressEvery / 2)
				expectEqual(t, "opt-in", len(h.context()) > 0, enabled)
				h.call.observeWorking(false)
				before := h.context()
				h.advance(3 * time.Minute)
				expectEqual(t, "idle", h.context(), before)
				if h.call.tick != nil || len(h.call.progressNotes) != 0 || len(h.call.progressActivity) != 0 {
					t.Fatal("idle retained progress")
				}
				h.call.observeWorking(true)
				h.call.observe([]liveRow{{seq: 12, activity: progressActivities("Starting: make build")}})
				h.call.end("hangup")
				before = h.context()
				h.advance(3 * time.Minute)
				expectEqual(t, "hangup", h.context(), before)
			})
		})
	}
}

func TestLiveProgressRoutineActivityIsCoalescedAndSpaced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		seq := int64(10)
		inspect := func(command string) {
			seq++
			input, err := json.Marshal(map[string]string{"command": command})
			if err != nil {
				t.Fatal(err)
			}
			facts := liveToolProgress(llm.Content{ID: fmt.Sprint(seq), ToolName: "bash", ToolInput: input})
			h.call.observe([]liveRow{{seq: seq, activity: facts}})
		}
		for _, command := range []string{
			"cat server/live_call.go",
			"rg progress server/live_progress.go",
			"git status",
			"git log -3",
			"git diff",
		} {
			inspect(command)
		}
		expectEqual(t, "burst not narrated command by command", len(h.context()), 0)
		h.advance(5 * time.Second)
		expectEqual(t, "one initial focus update", len(h.context()), 1)
		if !strings.HasPrefix(h.context()[0], "commentary [-]: "+liveProgressHeading) {
			t.Fatal("routine activity never reached the spoken update channel")
		}
		inspect("cat server/live_session.go")
		h.advance(11 * time.Second)
		expectEqual(t, "no rapid repeat", len(h.context()), 1)
		h.advance(time.Second)
		expectEqual(t, "fresh focus update after spacing", len(h.context()), 2)
		inspect("cat server/live_session.go")
		h.advance(2 * time.Minute)
		expectEqual(t, "no periodic or duplicate updates", len(h.context()), 2)
		before := h.context()
		seq++
		h.call.observe([]liveRow{{
			seq:      seq,
			progress: []string{"Found the duplicate listener.", "Found the duplicate listener."},
			activity: []liveActivity{{text: "Reading server/another.go", kind: liveInspect}},
		}})
		h.advance(liveProgressCoalesce)
		expectEqual(t, "finding retains its supporting activity", h.context()[len(before):], []string{
			"commentary [-]: " + liveReportHeading + "Found the duplicate listener.\nAction: Reading server/another.go\nState: submitted; result pending; success is not yet known",
		})
	})
}

func TestLiveProgressMatchesConcurrentToolResults(t *testing.T) {
	tools := map[string][]liveActivity{}
	for _, command := range []string{"go test ./server", "make build"} {
		liveProgress(progressMessage(t, "agent", llm.Message{Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: command, ToolName: "bash", ToolInput: json.RawMessage(fmt.Sprintf(`{"command":%q}`, command)),
		}}}), tools)
	}
	notes, activity, _ := liveProgress(progressMessage(t, "user", llm.Message{Content: []llm.Content{{
		Type: llm.ContentTypeToolResult, ToolUseID: "go test ./server", Display: map[string]any{"exitCode": 0}, ToolResult: llm.TextContent("PRIVATE OUTPUT"),
	}}}), tools)
	expectEqual(t, "no output", len(notes), 0)
	expectEqual(t, "matched result", activityText(activity), "Action: go test server\nState: shell exited with status 0")
	expectEqual(t, "remaining tool", len(tools), 1)
}

// Test-only helpers keep assertions focused on the evidence sent to Live.
func activityText(facts []liveActivity) string {
	var text []string
	for _, fact := range facts {
		text = append(text, fact.report())
	}
	return strings.Join(text, "\n")
}

func progressActivities(text string) []liveActivity {
	return []liveActivity{{text: text, phase: liveFinished}}
}

// Sanitized sequence from c77KVU6 call 2: changing read batches surround four
// patches. The large first patch and later conversation-view edit must be heard.
func TestLiveProgressWebSearchAndDelegationHideInputs(t *testing.T) {
	row := progressMessage(t, "agent", llm.Message{Content: []llm.Content{
		{Type: llm.ContentTypeServerToolUse, ToolName: "web_search", ToolInput: json.RawMessage(`{"query":"PRIVATE"}`)},
		{Type: llm.ContentTypeToolUse, ToolName: "subagent", ToolInput: json.RawMessage(`{"slug":"PRIVATE","prompt":"PRIVATE"}`)},
	}})
	_, facts, _ := liveProgress(row, nil)
	expectEqual(t, "public activity", activityText(facts), "Action: searching the web\nState: submitted; result pending; success is not yet known\nAction: delegating a task to a worker\nState: submitted; result pending; success is not yet known")
}

func TestLiveProgressStreamedAndSavedBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		c := h.call
		never := newLiveProgressHarness(t, false).call
		never.observeWorking(true)
		l := liveCalls{calls: map[string]*liveCall{c.id: c, "disabled": never}}
		stream := func(id, text string) {
			l.withActiveProgress(id, func(target *liveCall) { target.noteProgress(livePublicNote(text)) })
		}
		stream(c.conversationID, "before work")
		c.observeWorking(true)
		stream("other", "other conversation")
		stream(c.conversationID, "First result.")
		stream(c.conversationID, "First result.")
		h.advance(liveProgressCoalesce)
		row := progressMessage(t, "agent", llm.Message{Content: []llm.Content{llm.StringContent("First result."), llm.StringContent("Second result.")}})
		notes, _, _ := liveProgress(row, nil)
		c.observe([]liveRow{{seq: 11, progress: notes}})
		h.advance(5 * time.Second)
		c.observeWorking(false)
		stream(c.conversationID, "stale")
		h.advance(time.Minute)
		if never.tick != nil || len(never.progressNotes) != 0 {
			t.Fatal("Never retained stream commentary")
		}
		expectEqual(t, "whole blocks once", h.context(), []string{
			"thinking [-]: " + liveBackendWorking,
			"commentary [-]: " + liveReportHeading + "notes: First result.",
			"commentary [-]: " + liveReportHeading + "notes: Second result.",
			"thinking [-]: " + liveBackendIdle,
		})
	})
}

// A quick whole-command success must not erase a submitted edit or read.
// Missing descriptions must not silence structured failures.
