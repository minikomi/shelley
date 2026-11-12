package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

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
	tool := func(name, input string) llm.Content {
		return llm.Content{Type: llm.ContentTypeToolUse, ToolName: name, ToolInput: json.RawMessage(input)}
	}
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
		{"delegated request", dialogueRow("user", "Spoken request:\nlint it"+exchange(1, 2, "USER: lint it\nLIVE: Sure."), false, `{"live_delivery_id":"delivery"}`), "", "", false},
		{"subagent report", dialogueRow("user", "helper report", false, `{"sender_conversation_id":"child","sender_relationship":"subagent"}`), "", "", false},
		{"background report", dialogueRow("user", "job report", false, `{"background_job_id":"job"}`), "", "A background command was lost; completion is unknown.", false},
		{"unknown-purpose background success", dialogueRow("user", "PRIVATE OUTPUT", false, `{"background_job_id":"job","command":"python3 -c 'PRIVATE'","exit_code":0}`), "", "", false},
		{"unknown-purpose background failure", dialogueRow("user", "PRIVATE OUTPUT", false, `{"background_job_id":"job","command":"python3 -c 'PRIVATE'","exit_code":1}`), "", "Background shell exited with status 1; submitted step: ", false},
		{"user tool use", message("user", tool("bash", `{"command":"secret"}`)), "", "", false},
		{"agent notes", dialogueRow("agent", " Found the stale cache. ", false, ""), "notes: Found the stale cache.", "", false},
		{"bounded agent notes", dialogueRow("agent", strings.Repeat("界", 700), false, ""), "notes: …" + strings.Repeat("界", 592), "", false},
		{"tool purpose only", message("agent", tool("bash", `{"command":"cat .env","secret":"token"}`)), "", "", false},
		{"patch input omitted", message("agent", tool("apply_patch", `{"patch":"private contents"}`)), "", "", false},
		{"helper task", message("agent", tool("subagent", `{"slug":"lint","prompt":"Check the tests"}`)), "", "Starting: delegating a task to a worker", false},
		{"background command", message("agent", tool("bash", `{"command":"make test","background":true}`)), "", "Submitted background shell; requested step: make test", false},
		{"successful result omitted", message("user", result(false, "private successful output")), "", "", false},
		{"failed result output omitted", message("user", llm.StringContent("not a finding"), result(true, "PRIVATE OUTPUT")), "", "A tool reported failure; that step has not succeeded.", false},
		{"private thinking omitted", message("agent", llm.Content{Type: llm.ContentTypeThinking, Text: "PRIVATE REASONING"}), "", "", false},
		{"final reply", dialogueRow("agent", "Done", true, ""), "", "", true},
		{"empty final", dialogueRow("agent", "", true, ""), "", "", true},
		{"user end is not agent end", dialogueRow("user", "request", true, ""), "", "", false},
		{"excluded row", excluded, "", "", false},
		{"excluded message", progressMessage(t, "agent", llm.Message{Content: []llm.Content{llm.StringContent("hidden")}, ExcludedFromContext: true}), "", "", false},
		{"excluded end marker", progressMessage(t, "agent", llm.Message{EndOfTurn: true, ExcludedFromContext: true}), "", "", true},
		{"non dialogue", dialogueRow("error", "failure", false, ""), "", "", false},
		{"terminal error", dialogueRow("error", "PRIVATE failure", true, ""), "", "", true},
		{"excluded terminal error", progressMessage(t, "error", llm.Message{EndOfTurn: true, ExcludedFromContext: true}), "", "", true},
		{"missing data", generated.Message{Type: "agent"}, "", "", false},
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

const testLiveProgressEvery = 20 * time.Second

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

func TestLiveProgressOnlyNewNotesSurviveTheCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		startLiveProgressTask(h)
		h.call.observe([]liveRow{{seq: 12, progress: []string{strings.Repeat("old finding ", 100)}}})
		h.advance(testLiveProgressEvery / 2)
		if got := h.context()[1]; !strings.HasPrefix(got, "commentary [d1]: "+liveReportHeading) {
			t.Fatalf("first update = %q", got)
		}
		for i := range 40 {
			h.call.observe([]liveRow{{seq: int64(13 + i), progress: []string{fmt.Sprintf("pending %d: %s", i, strings.Repeat("界", 600))}}})
		}
		h.call.observe([]liveRow{{seq: 53, progress: []string{"NEWEST: the cache is fixed"}}})
		h.call.locked(func() {
			if utf8.RuneCountInString(strings.Join(h.call.progressNotes, "\n")) > liveProgressChars-len(liveReportHeading) {
				t.Fatal("pending progress buffer exceeded the update budget")
			}
		})
		h.advance(testLiveProgressEvery)
		got := h.context()[2]
		if strings.Contains(got, "old finding") || strings.Contains(got, "Earlier this task") || !strings.HasSuffix(got, "NEWEST: the cache is fixed") {
			t.Errorf("newest notes lost or old notes replayed: %q", got)
		}
		content := strings.TrimPrefix(got, "commentary [d1]: ")
		if n := utf8.RuneCountInString(content); n != liveProgressChars {
			t.Errorf("update has %d characters, want %d", n, liveProgressChars)
		}
	})
}

func TestLiveProgressStopsAtTurnOrCallEnd(t *testing.T) {
	for _, end := range []string{"reply", "empty", "call"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveProgressHarness(t, true)
				startLiveProgressTask(h)
				h.call.observe([]liveRow{{seq: 12, progress: []string{"old pending notes"}}})
				h.advance(liveProgressCoalesce - time.Nanosecond)
				if end == "call" {
					h.call.end("hung up")
				} else {
					text, row := "", progressMessage(t, "agent", llm.Message{EndOfTurn: true, ExcludedFromContext: true})
					if end == "reply" {
						text, row = "All tests pass.", dialogueRow("agent", "All tests pass.", true, "")
					}
					notes, activity, final := liveProgress(row, nil)
					h.call.observe([]liveRow{{seq: 13, text: text, progress: notes, activity: activity, endOfTurn: final}})
					if h.call.submitted[0].finished != (end == "reply") || len(h.call.progressNotes) != 0 {
						t.Error("turn end changed reply completion or retained progress")
					}
				}
				before := h.context()
				h.advance(10 * time.Minute)
				expectEqual(t, "no updates after end", h.context(), before)
				if h.call.tick != nil {
					t.Error("progress timer still running")
				}
				if end == "reply" {
					expectEqual(t, "final reply", before[len(before)-1], "commentary [d1]: "+liveResultHeading+"All tests pass.")
				}
				if end != "call" {
					h.call.observe([]liveRow{{seq: 14, progress: []string{"new task"}}})
					h.advance(testLiveProgressEvery / 2)
					got := h.context()
					delegation := "-"
					if end == "empty" {
						delegation = "d1"
					}
					expectEqual(t, "fresh turn", got[len(got)-1], "commentary ["+delegation+"]: "+liveReportHeading+"new task")
				}
			})
		})
	}
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

func TestLiveProgressIgnoresPreviousTurnTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		h.call.observe([]liveRow{{seq: 11, progress: []string{"old task"}}})
		stale := h.call.tick
		h.call.observe([]liveRow{{seq: 12, endOfTurn: true}, {seq: 13, progress: []string{"new task"}}})
		// An old callback can fire before turn end, then acquire the lock
		// only after the new turn starts its timer.
		h.call.locked(func() { h.call.progressTick(stale) })
		expectEqual(t, "stale callback emits nothing", len(h.context()), 0)
		h.advance(liveProgressCoalesce - time.Nanosecond)
		expectEqual(t, "new turn timer not reset", len(h.context()), 0)
		h.advance(time.Nanosecond)
		got := h.context()
		expectEqual(t, "new turn update", got, []string{"commentary [-]: " + liveReportHeading + "new task"})
	})
}

func TestLiveProgressConcreteActivityBeforePublicReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		startLiveProgressTask(h)
		row := progressMessage(t, "agent", llm.Message{Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: "PRIVATE REASONING"},
			{Type: llm.ContentTypeToolUse, ToolName: "bash", ToolInput: json.RawMessage(`{"command":"git diff -- server/live_call.go"}`)},
		}})
		notes, activity, final := liveProgress(row, nil)
		h.call.observe([]liveRow{{seq: 12, progress: notes, activity: activity, endOfTurn: final}})
		h.advance(testLiveProgressEvery / 2)
		got := h.context()
		expectEqual(t, "concrete activity", got[len(got)-1], "commentary [d1]: "+liveProgressHeading+"Starting: git diff (comparing changes) server/live_call.go")
		h.call.observe([]liveRow{{seq: 13, activity: activity}})
		h.advance(3 * time.Minute)
		expectEqual(t, "no repeats or silence announcements", h.context(), got)
		h.call.observe([]liveRow{{seq: 14, progress: []string{"Found two SQLC commits."}, activity: progressActivities("Starting: git diff (comparing changes) db/query/live.sql")}})
		h.advance(testLiveProgressEvery)
		got = h.context()
		expectEqual(t, "finding and activity", got[len(got)-1], "commentary [d1]: "+liveReportHeading+"Found two SQLC commits.\nStarting: git diff (comparing changes) db/query/live.sql")
		h.call.observe([]liveRow{{seq: 15, progress: []string{"Found two SQLC commits."}}})
		h.advance(testLiveProgressEvery)
		expectEqual(t, "delivered finding deduplicated", h.context(), got)
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
				h.call.observe([]liveRow{{seq: 12, activity: progressActivities("Starting: make ui")}})
				h.call.end("hangup")
				before = h.context()
				h.advance(3 * time.Minute)
				expectEqual(t, "hangup", h.context(), before)
			})
		})
	}
}

func TestLiveProgressWorkingState(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveProgressHarness(t, enabled)
				c := h.call
				c.observeWorking(false)
				c.observeWorking(false)
				c.observeWorking(true)
				c.observeWorking(true)
				if enabled {
					c.observe([]liveRow{{seq: 11, progress: []string{"pending old work"}}})
				}
				stale := c.tick
				c.observeWorking(false)
				c.locked(func() { c.progressTick(stale) })
				if c.tick != nil || len(c.progressNotes) != 0 || len(c.progressActivity) != 0 {
					t.Fatal("idle retained pending work")
				}
				c.observeWorking(false)
				c.observeWorking(true)
				c.end("hangup")
				c.observeWorking(false)
				var want []string
				if enabled {
					want = []string{
						"thinking [-]: " + liveBackendIdle,
						"thinking [-]: " + liveBackendWorking,
						"thinking [-]: " + liveBackendIdle,
						"thinking [-]: " + liveBackendWorking,
					}
				} else if c.backendWorking != nil {
					t.Fatal("default-off call collected backend state")
				}
				expectEqual(t, "silent deduplicated state transitions", h.context(), want)
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
		h.advance(19 * time.Second)
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
			"commentary [-]: " + liveReportHeading + "Found the duplicate listener.\nStarting: Reading server/another.go",
		})
	})
}

func TestLiveProgressBackgroundCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		startLiveProgressTask(h)
		h.call.observe([]liveRow{{seq: 12, activity: progressActivities("A shell command is running in the background; it has not completed.")}})
		h.advance(testLiveProgressEvery / 2)
		row := dialogueRow("user", "PRIVATE OUTPUT", false, `{"background_job_id":"j","command":"go test ./server","exit_code":0,"tail":"PRIVATE OUTPUT"}`)
		notes, activity, final := liveProgress(row, nil)
		h.call.observe([]liveRow{{seq: 13, progress: notes, activity: activity, endOfTurn: final}})
		h.advance(testLiveProgressEvery)
		got := h.context()
		expectEqual(t, "background completion", got[len(got)-1], "commentary [d1]: "+liveProgressHeading+"Background shell exited with status 0; submitted step: go test server")
		h.advance(3 * time.Minute)
		expectEqual(t, "no stale waits", h.context(), got)
	})
}

func TestLiveProgressStartsOnlyAfterSuccessfulDelivery(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint("failed=", failed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveProgressHarness(t, true)
				if failed {
					h.set(func() {
						h.reply = func(string) (liveReceipt, error) { return liveReceipt{}, errors.New("rejected") }
					})
				}
				h.say("Run the tests", 0, 500)
				h.delegate("d1", 500)
				h.advance(liveQuiet)
				expectEqual(t, "work requires accepted delivery", h.call.progressSeen != nil, !failed)
				before := h.context()
				h.advance(2 * time.Minute)
				expectEqual(t, "no fabricated updates for a silent task", h.context(), before)
			})
		})
	}
}

func TestLiveProgressMatchesConcurrentToolResults(t *testing.T) {
	tools := map[string][]liveActivity{}
	for _, command := range []string{"go test ./server", "make ui"} {
		liveProgress(progressMessage(t, "agent", llm.Message{Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: command, ToolName: "bash", ToolInput: json.RawMessage(fmt.Sprintf(`{"command":%q}`, command)),
		}}}), tools)
	}
	notes, activity, _ := liveProgress(progressMessage(t, "user", llm.Message{Content: []llm.Content{{
		Type: llm.ContentTypeToolResult, ToolUseID: "go test ./server", Display: map[string]any{"exitCode": 0}, ToolResult: llm.TextContent("PRIVATE OUTPUT"),
	}}}), tools)
	expectEqual(t, "no output", len(notes), 0)
	expectEqual(t, "matched result", activityText(activity), "Shell exited with status 0; submitted step: go test server")
	expectEqual(t, "remaining tool", len(tools), 1)
}

func TestLiveProgressUnknownSuccessDoesNotEraseActivity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		startLiveProgressTask(h)
		h.call.observe([]liveRow{{seq: 12, activity: progressActivities("Starting: Reading claudetool/toolset.go")}})
		_, activity, _ := liveProgress(progressMessage(t, "user", llm.Message{Content: []llm.Content{{
			Type: llm.ContentTypeToolResult, ToolUseID: "unrecognised", Display: map[string]any{"exitCode": 0},
		}}}), map[string][]liveActivity{})
		expectEqual(t, "unknown success supplies no generic activity", activityText(activity), "")
		h.call.observe([]liveRow{{seq: 13, activity: activity}})
		h.advance(testLiveProgressEvery / 2)
		got := h.context()
		expectEqual(t, "concrete activity survives", got[len(got)-1], "commentary [d1]: "+liveProgressHeading+"Starting: Reading claudetool/toolset.go")
	})
}

func TestLiveProgressBufferedErrorStopsAfterDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		h.set(func() { h.hold = make(chan struct{}) })
		h.say("Run the tests", 0, 500)
		h.delegate("d1", 500)
		h.call.locked(func() { h.call.commit("d1") })
		synctest.Wait()
		expectEqual(t, "delivery in flight", h.call.inflight, 1)

		tools := map[string][]liveActivity{"tool": progressActivities("make test")}
		notes, activity, end := liveProgress(dialogueRow("error", "PRIVATE failure", true, ""), tools)
		h.call.observe([]liveRow{
			{seq: 11, user: true, text: "Run the tests", liveDeliveryID: "delivery-1"},
			{seq: 12, progress: []string{"Checking tests"}, activity: progressActivities("Starting: make test")},
			{seq: 13, progress: notes, activity: activity, endOfTurn: end},
		})
		h.call.observeWorking(false)
		h.set(func() { close(h.hold) })
		synctest.Wait()

		expectEqual(t, "rows replayed", h.call.lastSeen, int64(13))
		expectEqual(t, "tool descriptions cleared", len(tools), 0)
		if h.call.tick != nil || len(h.call.progressNotes) != 0 || len(h.call.progressActivity) != 0 {
			t.Error("buffered progress restarted after terminal error")
		}
		expectEqual(t, "no stale progress or private error", h.context(), []string{
			"thinking [-]: " + liveBackendIdle,
			"thinking [d1]: The backend accepted this task: Run the tests",
		})
	})
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

func TestLiveProgressMergesResultsAndPrioritisesEdits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		start := liveActivity{text: "update server/live_call.go", kind: livePatch, toolID: "patch"}
		h.call.observe([]liveRow{{seq: 11, activity: []liveActivity{start}}})
		result := liveToolResult(llm.Content{ToolUseID: "patch"}, []liveActivity{start})
		h.call.observe([]liveRow{{seq: 12, activity: result, progress: []string{strings.Repeat("public finding ", 80)}}})
		for i := range 40 {
			h.call.observe([]liveRow{{seq: int64(13 + i), activity: progressActivities(fmt.Sprintf("Starting: Reading server/file%d.go", i))}})
		}
		if len(h.call.progressActivity) > 32 {
			t.Fatal("unbounded pending activity")
		}
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		got := h.context()[0]
		if !strings.Contains(got, "Applied patch: update server/live_call.go") || strings.Contains(got, "Applying patch:") {
			t.Fatalf("edit lost or start repeated: %s", got)
		}
		if utf8.RuneCountInString(strings.TrimPrefix(got, "commentary [-]: ")) > liveProgressChars {
			t.Fatal("payload exceeded budget")
		}
	})
}

func TestLiveProgressInspectionDedupAndDistinctEdits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		tool := func(id, command string) []liveActivity {
			raw, _ := json.Marshal(map[string]string{"command": command})
			return liveToolProgress(llm.Content{ID: id, ToolName: "bash", ToolInput: raw})
		}
		h.call.observe([]liveRow{{seq: 11, activity: tool("read1", "cat server/live_call.go server/live_call_http.go")}})
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		before := h.context()
		h.call.observe([]liveRow{{seq: 12, activity: tool("read2", "rg something server/live_call_http.go; cat server/live_call_http.go server/live_call.go")}})
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		expectEqual(t, "same inspections in different batches", h.context(), before)
		h.call.observe([]liveRow{{seq: 13, activity: tool("read3", "cat server/live_call.go server/live_session.go")}})
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		before = h.context()
		expectEqual(t, "only the new file", before[len(before)-1], "commentary [-]: "+liveProgressHeading+"Starting: Reading server/live_session.go")
		for i := range 2 {
			id := fmt.Sprintf("patch%d", i)
			fact := liveActivity{text: "update server/live_call.go", kind: livePatch, toolID: id, phase: liveFinished}
			h.call.observe([]liveRow{{seq: int64(14 + i), activity: []liveActivity{fact}}})
			h.call.locked(func() { h.call.progressTick(h.call.tick) })
		}
		expectEqual(t, "distinct edits remain reportable", len(h.context()), len(before)+2)
	})
}

// Sanitized sequence from c77KVU6 call 2: changing read batches surround four
// patches. The large first patch and later conversation-view edit must be heard.
func TestLiveProgressVoiceExchangeReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		tools := map[string][]liveActivity{}
		seq := int64(11)
		observe := func(typ string, content llm.Content) {
			notes, facts, end := liveProgress(progressMessage(t, typ, llm.Message{Content: []llm.Content{content}}), tools)
			h.call.observe([]liveRow{{seq: seq, progress: notes, activity: facts, endOfTurn: end}})
			seq++
		}
		read := func(command string) {
			raw, _ := json.Marshal(map[string]string{"command": command})
			id := fmt.Sprint(seq)
			observe("agent", llm.Content{ID: id, Type: llm.ContentTypeToolUse, ToolName: "bash", ToolInput: raw})
			observe("user", llm.Content{ToolUseID: id, Type: llm.ContentTypeToolResult, Display: map[string]any{"exitCode": 0}})
		}
		patch := func(files ...string) {
			body := "*** Begin Patch\n"
			for _, file := range files {
				body += "*** Update File: /home/exedev/work/" + file + "\n@@\n-PRIVATE\n+PRIVATE\n"
			}
			body += "*** End Patch"
			raw, _ := json.Marshal(map[string]string{"input": body})
			id := fmt.Sprint(seq)
			observe("agent", llm.Content{ID: id, Type: llm.ContentTypeToolUse, ToolName: "apply_patch", ToolInput: raw})
			observe("user", llm.Content{ToolUseID: id, Type: llm.ContentTypeToolResult, ToolResult: llm.TextContent("PRIVATE output")})
		}
		for _, files := range [][]string{
			{"server/live_call.go", "server/live_call_http.go", "ui/src/services/liveTranscript.ts", "ui/src/vue/components/coalesce.ts", "ui/src/vue/components/Message.vue"},
			{"ui/src/vue/components/Message.vue"}, {"server/live_call.go"}, {"ui/src/utils/conversationView.ts"},
		} {
			read("cat server/live_call.go; rg history ui/src/vue/components/coalesce.ts")
			patch(files...)
			read("rg changed server/live_call.go; cat ui/src/vue/components/coalesce.ts")
			h.call.locked(func() { h.call.progressTick(h.call.tick) })
		}
		got := strings.Join(h.context(), "\n")
		t.Log(got)
		if strings.Count(got, "Applied patch:") != 4 || !strings.Contains(got, "conversationView.ts") || strings.Contains(got, "other targets") {
			t.Fatalf("missing patch evidence: %s", got)
		}
		if strings.Count(got, "Reading server/live_call.go") != 0 || strings.Contains(got, "exited") || strings.Contains(got, "PRIVATE") {
			t.Fatalf("repetitive or private evidence: %s", got)
		}
	})
}

func TestLiveProgressBackgroundPhasesCollapse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		tools := map[string][]liveActivity{}
		use := llm.Content{Type: llm.ContentTypeToolUse, ID: "shell", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"go test ./server","background":true}`)}
		_, started, _ := liveProgress(progressMessage(t, "agent", llm.Message{Content: []llm.Content{use}}), tools)
		h.call.observe([]liveRow{{seq: 11, activity: started}})
		result := llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: "shell", Display: map[string]any{"background": map[string]any{"jobId": "job"}}}
		_, running, _ := liveProgress(progressMessage(t, "user", llm.Message{Content: []llm.Content{result}}), tools)
		h.call.observe([]liveRow{{seq: 12, activity: running}, {seq: 13, activity: running}})
		expectEqual(t, "duplicate result preserves pending evidence", len(h.call.progressActivity), 1)
		row := dialogueRow("user", "PRIVATE output", false, `{"background_job_id":"job","command":"go test ./server","exit_code":0}`)
		_, finished, _ := liveProgress(row, tools)
		h.call.observe([]liveRow{{seq: 14, activity: finished}})
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		expectEqual(t, "only the latest phase", h.context(), []string{"commentary [-]: " + liveProgressHeading + "Background shell exited with status 0; submitted step: go test server"})
		expectEqual(t, "finished background match removed", len(tools), 0)
	})
}

func TestLiveProgressPayloadIncludesUnicodeActivity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		h.call.observe([]liveRow{{seq: 11, progress: []string{strings.Repeat("界", 800)}, activity: []liveActivity{
			{text: strings.Repeat("界", 400), kind: livePatch, toolID: "patch", phase: liveFinished},
		}}})
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		got := strings.TrimPrefix(h.context()[0], "commentary [-]: ")
		if !strings.Contains(got, "Applied patch:") || utf8.RuneCountInString(got) > liveProgressChars {
			t.Fatalf("activity lost or budget exceeded: %s", got)
		}
	})
}

func TestLiveProgressCompletedShellRetiresStart(t *testing.T) {
	for _, command := range []string{"false && go test ./server; true", "make build"} {
		t.Run(command, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveProgressHarness(t, true)
				input, err := json.Marshal(map[string]string{"command": command})
				if err != nil {
					t.Fatal(err)
				}
				tool := llm.Content{Type: llm.ContentTypeToolUse, ID: "check", ToolName: "bash", ToolInput: input}
				start := liveToolProgress(tool)
				if len(start) != 1 {
					t.Fatalf("missing start: %v", start)
				}
				h.call.observe([]liveRow{{seq: 11, activity: start}, {seq: 12, activity: liveToolResult(llm.Content{ToolUseID: "check", Display: map[string]any{"exitCode": 0}}, start)}})
				if h.call.tick != nil {
					t.Fatal("retired shell left an armed timer")
				}
				h.call.locked(func() { h.call.progressTick(h.call.tick) })
				if got := h.context(); len(got) != 0 {
					t.Fatalf("spoke a shell exit or stale start: %v", got)
				}
			})
		})
	}
}
func TestLiveProgressCrowdedBatchKeepsCorrection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		plan := "notes: " + strings.Repeat("界", 245)
		finding := "notes: " + strings.Repeat("測", 210) + " The filter shows labels from another workspace."
		patch := liveActivity{kind: livePatch, phase: liveFinished, text: "update ui/src/services/liveTranscript.ts, update ui/src/vue/components/Message.vue, update ui/src/utils/conversationView.ts"}
		h.call.observe([]liveRow{{seq: 11, progress: []string{plan}}, {seq: 12, progress: []string{finding}, activity: []liveActivity{patch}}})
		// Reads can evict the report from the dedup window while it waits for speech.
		for i := range 40 {
			h.call.observe([]liveRow{{seq: int64(13 + i), activity: []liveActivity{{kind: liveInspect, text: fmt.Sprintf("Reading server/file%d.go", i)}}}})
		}
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		expectEqual(t, "whole correction with file activity", h.context(), []string{"commentary [-]: " + liveReportHeading + finding + "\n" + "Applied patch: update ui/src/services/liveTranscript.ts, update ui/src/vue/components/Message.vue, update ui/src/utils/conversationView.ts"})
	})
}

func TestLiveProgressOversizedReportKeepsConclusion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		conclusion := " However, writes still fail."
		h.call.observe([]liveRow{{seq: 11, progress: []string{strings.Repeat("界", 560) + conclusion}}})
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		if got := h.context(); len(got) != 1 || !strings.HasSuffix(got[0], conclusion) {
			t.Fatalf("lost conclusion: %v", got)
		}
	})
}

func TestLiveProgressCrowdedBatchKeepsNewestConclusion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		old := "OLD: " + strings.Repeat("界", 245)
		conclusion := " Correction: writes still fail."
		newest := strings.Repeat("測", 250) + conclusion
		patch := liveActivity{kind: livePatch, phase: liveFinished, text: strings.Repeat("p", 284)}
		h.call.observe([]liveRow{{seq: 11, progress: []string{old}}, {seq: 12, progress: []string{newest}, activity: []liveActivity{patch}}})
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		got := h.context()
		if len(got) != 1 || strings.Contains(got[0], "OLD") || strings.Contains(got[0], "界") || !strings.Contains(got[0], conclusion) || !strings.HasSuffix(got[0], patch.report()) {
			t.Fatalf("lost newest correction or concrete activity: %v", got)
		}
		if n := utf8.RuneCountInString(strings.TrimPrefix(got[0], "commentary [-]: ")); n > liveProgressChars {
			t.Fatalf("update has %d characters, limit %d", n, liveProgressChars)
		}
	})
}

func TestLiveProgressWebSearchAndDelegationHideInputs(t *testing.T) {
	row := progressMessage(t, "agent", llm.Message{Content: []llm.Content{
		{Type: llm.ContentTypeServerToolUse, ToolName: "web_search", ToolInput: json.RawMessage(`{"query":"PRIVATE"}`)},
		{Type: llm.ContentTypeToolUse, ToolName: "subagent", ToolInput: json.RawMessage(`{"slug":"PRIVATE","prompt":"PRIVATE"}`)},
	}})
	_, facts, _ := liveProgress(row, nil)
	expectEqual(t, "public activity", activityText(facts), "Starting: searching the web\nStarting: delegating a task to a worker")
}

func TestLiveProgressFollowsEvidence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		seq := int64(10)
		read := func(file string) {
			seq++
			h.call.observe([]liveRow{{seq: seq, activity: []liveActivity{{kind: liveInspect, text: "Reading " + file}}}})
		}
		read("server/first.go")
		h.advance(5 * time.Second)
		expectEqual(t, "first concrete activity", len(h.context()), 1)
		read("server/second.go")
		h.advance(19 * time.Second)
		expectEqual(t, "spacing respected", len(h.context()), 1)
		h.advance(time.Second)
		if got := h.context(); len(got) != 2 || !strings.Contains(got[1], "server/second.go") {
			t.Fatalf("fresh same-directory read lost: %v", got)
		}
		read("server/second.go")
		h.advance(time.Minute)
		expectEqual(t, "exact repeat stays quiet", len(h.context()), 2)

		read("ui/menu.ts")
		h.advance(2 * time.Second)
		seq++
		h.call.observe([]liveRow{{seq: seq, progress: []string{"Found two notification listeners."}}})
		h.advance(liveProgressCoalesce)
		if got := h.context(); len(got) != 3 || !strings.Contains(got[2], "two notification listeners") || !strings.Contains(got[2], "ui/menu.ts") {
			t.Fatalf("finding did not advance pending evidence: %v", got)
		}
		h.advance(time.Minute)
		expectEqual(t, "no periodic speech", len(h.context()), 3)
	})
}

func TestLiveProgressNearbyOutcomesShareAnUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		h.call.observe([]liveRow{{seq: 11, progress: []string{"Found the duplicate listener."}}})
		h.advance(liveProgressCoalesce)
		h.call.observe([]liveRow{{seq: 12, progress: []string{"Removed the extra listener."}}})
		h.advance(2 * time.Second)
		h.call.observe([]liveRow{{seq: 13, progress: []string{"Reconnect check passed."}}})
		h.advance(2 * time.Second)
		expectEqual(t, "no rapid-fire updates", len(h.context()), 1)
		h.advance(time.Second)
		got := h.context()
		if len(got) != 2 || !strings.Contains(got[1], "Removed the extra listener.\nReconnect check passed.") {
			t.Fatalf("lost adjacent outcomes: %v", got)
		}
	})
}

func TestLiveRoutineOverflowWaitsForNewEvidence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		var facts []liveActivity
		for i := range 4 {
			facts = append(facts, liveActivity{kind: liveCheck, toolID: fmt.Sprint(i), text: strings.Repeat("x", 200)})
		}
		h.call.observe([]liveRow{{seq: 11, activity: facts}})
		h.advance(time.Minute)
		if len(h.context()) != 1 || h.call.tick != nil {
			t.Fatal("routine backlog scheduled another interruption")
		}
	})
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
		h.advance(time.Minute)
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

func TestLiveSlowCheck(t *testing.T) {
	for _, tc := range []struct {
		command  string
		duration time.Duration
		updates  int
	}{
		{"go build ./server", 9 * time.Second, 2},
		{"go build ./server", time.Second, 0},
		{"false && go build ./server; true", 9 * time.Second, 0},
		{"! go test ./server", 9 * time.Second, 0},
	} {
		t.Run(tc.command, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveProgressHarness(t, true)
				tools := map[string][]liveActivity{}
				observe := func(seq int64, typ string, content llm.Content) {
					_, facts, _ := liveProgress(progressMessage(t, typ, llm.Message{Content: []llm.Content{content}}), tools)
					h.call.observe([]liveRow{{seq: seq, activity: facts}})
				}
				h.call.observe([]liveRow{{seq: 11, progress: []string{"Checking the build."}}})
				h.advance(liveProgressCoalesce)
				start := time.Now()
				input, err := json.Marshal(map[string]string{"command": tc.command})
				if err != nil {
					t.Fatal(err)
				}
				observe(12, "agent", llm.Content{Type: llm.ContentTypeToolUse, ID: "build", ToolName: "bash", ToolInput: input})
				h.advance(tc.duration)
				end := time.Now()
				expectEqual(t, "start before result", len(h.context()), 1+tc.updates/2)
				observe(13, "user", llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: "build", ToolUseStartTime: &start, ToolUseEndTime: &end, Display: map[string]any{"exitCode": 0}})
				h.advance(time.Minute)
				got := h.context()
				expectEqual(t, "no repeats", len(got), 1+tc.updates)
				if tc.updates == 2 && (!strings.Contains(got[1], "Starting: go build server") || !strings.Contains(got[2], "Command finished successfully: go build server")) {
					t.Fatal(got)
				}
			})
		})
	}
}

func TestLiveCompactionReportsStartAndCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		startLiveProgressTask(h)
		tools := map[string][]liveActivity{}
		row := func(seq int64, typ string, block llm.Content) liveRow {
			_, facts, _ := liveProgress(progressMessage(t, typ, llm.Message{Content: []llm.Content{block}}), tools)
			return liveRow{seq: seq, activity: facts}
		}
		nudge := dialogueRow("user", "PRIVATE context index", false, `{"context_nudge":true}`)
		_, facts, _ := liveProgress(nudge, tools)
		h.call.observe([]liveRow{{seq: 12, activity: facts}})
		got := h.context()
		expectEqual(t, "review start", got[len(got)-1], "commentary [d1]: "+liveProgressHeading+"Starting a compaction review of older messages.")
		start := row(13, "agent", llm.Content{Type: llm.ContentTypeToolUse, ID: "compact", ToolName: "compact_in_place", ToolInput: json.RawMessage(`{"action":"compact"}`)})
		end := row(14, "user", llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: "compact"})
		h.call.observe([]liveRow{start, end})
		h.advance(5 * time.Second)
		got = h.context()
		expectEqual(t, "start and completion", got[len(got)-2:], []string{
			"commentary [d1]: " + liveProgressHeading + liveCompacting,
			"commentary [d1]: " + liveProgressHeading + "Shortened this conversation’s history.",
		})
		h.advance(time.Minute)
		expectEqual(t, "no repeated maintenance", h.context(), got)
		h.call.observe([]liveRow{
			row(15, "agent", llm.Content{Type: llm.ContentTypeToolUse, ID: "failed-compact", ToolName: "compact_in_place", ToolInput: json.RawMessage(`{"action":"compact"}`)}),
			row(16, "user", llm.Content{Type: llm.ContentTypeToolResult, ToolUseID: "failed-compact", ToolError: true}),
		})
		h.advance(5 * time.Second)
		got = h.context()
		expectEqual(t, "failure still reportable", got[len(got)-1], "commentary [d1]: "+liveProgressHeading+"The attempt to shorten this conversation’s history failed.")
	})
}

func TestLiveLookupsKeepTheirDeadlineThroughThinking(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		lookup := func(id string) {
			h.call.locked(func() { h.call.notePublicProgress(llm.PublicProgress{WebLookupID: id, WebAction: "search"}) })
		}
		lookup("first")
		h.advance(4 * time.Second)
		row := progressMessage(t, "agent", llm.Message{Content: []llm.Content{{Type: llm.ContentTypeThinking, Thinking: "PRIVATE"}}})
		notes, activities, end := liveProgress(row, nil)
		h.call.observe([]liveRow{{seq: 11, progress: notes, activity: activities, endOfTurn: end}})
		lookup("second")
		h.advance(time.Second)
		expectEqual(t, "pending searches arrive on original deadline", h.context(), []string{"commentary [-]: " + liveProgressHeading + "Ran a web search for this investigation."})
		saved := progressMessage(t, "agent", llm.Message{Content: []llm.Content{{Type: llm.ContentTypeServerToolUse, ToolName: "web_search", ID: "second"}}})
		_, activities, _ = liveProgress(saved, nil)
		h.call.observe([]liveRow{{seq: 12, activity: activities}})
		h.advance(time.Minute)
		expectEqual(t, "saved lookup stays quiet", len(h.context()), 1)
	})
}

// A quick whole-command success must not erase a submitted edit or read.
func TestLiveFastShellKeepsConcreteWork(t *testing.T) {
	for _, command := range []string{
		"cat > app.py <<'PY'\nPRIVATE BODY\nPY\nnginx -t -c /private/nginx.conf",
		"cat assets/inject.js; python3 -m unittest -v test_configure",
	} {
		t.Run(command, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveProgressHarness(t, true)
				input, _ := json.Marshal(map[string]string{"command": command})
				started := liveToolProgress(llm.Content{ID: "setup", ToolName: "bash", ToolInput: input})
				h.call.observe([]liveRow{{seq: 11, activity: started}})
				h.advance(time.Second)
				h.call.observe([]liveRow{{seq: 12, activity: liveToolResult(llm.Content{Display: map[string]any{"exitCode": 0}, ToolResult: llm.TextContent("PRIVATE OUTPUT")}, started)}})
				h.advance(liveCheckDelay)
				got := strings.Join(h.context(), "\n")
				if len(h.context()) != 1 || strings.Contains(got, "PRIVATE") || strings.Contains(got, "exited with status") || (!strings.Contains(got, "app.py") && !strings.Contains(got, "assets/inject.js")) {
					t.Fatal(got)
				}
			})
		})
	}
}

// Missing descriptions must not silence structured failures.
func TestLiveProgressReportsFailuresWithoutSafeDescriptions(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprint(background), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newLiveProgressHarness(t, true)
				tools := map[string][]liveActivity{}
				row := progressMessage(t, "user", llm.Message{Content: []llm.Content{{Type: llm.ContentTypeToolResult, ToolError: true, ToolResult: llm.TextContent("PRIVATE")}}})
				want := "A tool reported failure; that step has not succeeded."
				if background {
					row = dialogueRow("user", "PRIVATE", false, `{"background_job_id":"job"}`)
					want = "A background command was lost; completion is unknown."
				}
				_, facts, _ := liveProgress(row, tools)
				h.call.observe([]liveRow{{seq: 11, activity: facts}})
				h.advance(liveProgressCoalesce)
				expectEqual(t, "failure delivered", h.context(), []string{"commentary [-]: " + liveProgressHeading + want})
			})
		})
	}
}

func TestLiveBackgroundCompletionPrecedesQueuedNotice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		job := backgroundJobUserData{BackgroundJobID: "job", Command: "go test ./server", ExitCode: new(int)}
		finished := liveBackgroundProgress(job, "shell")
		// The job can finish before the call observes the tool's saved rows.
		h.call.locked(func() { h.call.noteActivity(finished) })
		h.advance(liveProgressCoalesce)
		start := liveToolProgress(llm.Content{ID: "shell", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"go test ./server","background":true}`)})
		running := liveToolResult(llm.Content{Display: map[string]any{"background": map[string]any{"jobId": "job"}}}, start)
		h.call.observe([]liveRow{{seq: 11, activity: start}, {seq: 12, activity: running}})
		// A saved notice can lack the original tool association. Its job
		// identity must still deduplicate the already-spoken completion.
		h.call.locked(func() { h.call.noteActivity(liveBackgroundProgress(job, "job")) })
		h.advance(time.Minute)
		expectEqual(t, "completion once, no late start", h.context(), []string{"commentary [-]: " + liveProgressHeading + "Background shell exited with status 0; submitted step: go test server"})
		if h.call.tick != nil || len(h.call.progressActivity) != 0 {
			t.Fatal("late phases retained work after completion")
		}
	})
}

func TestLiveBackgroundReceiptKeepsStartDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		started := liveToolProgress(llm.Content{ID: "shell", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"go test ./server","background":true}`)})
		h.call.observe([]liveRow{{seq: 11, activity: started}})
		h.advance(time.Second)
		running := liveToolResult(llm.Content{Display: map[string]any{"background": map[string]any{"jobId": "job"}}}, started)
		h.call.observe([]liveRow{{seq: 12, activity: running}})
		h.advance(3 * time.Second)
		expectEqual(t, "receipt is not a completed outcome", len(h.context()), 0)
		h.advance(time.Second)
		expectEqual(t, "one start on its original deadline", h.context(), []string{"commentary [-]: " + liveProgressHeading + "Running in background; submitted step: go test server"})
	})
}

func TestLiveInspectionDedupKeepsFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		started := liveToolProgress(llm.Content{ID: "read", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"cat server/live_call.go"}`)})
		h.call.observe([]liveRow{{seq: 11, activity: started}})
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		failed := liveToolResult(llm.Content{ToolError: true, ToolResult: llm.TextContent("PRIVATE")}, started)
		h.call.observe([]liveRow{{seq: 12, activity: failed}})
		h.call.locked(func() { h.call.progressTick(h.call.tick) })
		expectEqual(t, "failure survives the earlier read", h.context(), []string{
			"commentary [-]: " + liveProgressHeading + "Starting: Reading server/live_call.go",
			"commentary [-]: " + liveProgressHeading + "Tool failed: Reading server/live_call.go",
		})
	})
}
