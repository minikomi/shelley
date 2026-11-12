package server

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/llm"
)

func TestLiveProgressWaitsForPlayback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		h.call.handle([]liveEvent{{typ: "shelley.playback", playbackActive: true}})
		h.call.observe([]liveRow{{seq: 11, progress: []string{"The first check failed."}}})
		h.advance(10 * time.Second)
		expectEqual(t, "no update over audio", len(h.context()), 0)
		h.call.observe([]liveRow{{seq: 12, progress: []string{"The cause is an extra listener."}}})
		h.advance(10 * time.Second)
		expectEqual(t, "new evidence also waits", len(h.context()), 0)
		h.call.handle([]liveEvent{{typ: "shelley.playback", playbackActive: false}})
		h.advance(time.Second)
		got := h.context()
		if len(got) != 1 || !strings.Contains(got[0], "first check failed") || !strings.Contains(got[0], "extra listener") {
			t.Fatalf("pending findings were lost: %v", got)
		}
	})
}

func TestLiveFinalWaitsForSpeechAndSurvivesIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		h.call.handle([]liveEvent{{typ: "shelley.playback", playbackActive: true}})
		h.call.noteProgress("Old pending progress.")
		h.call.observe([]liveRow{{seq: 11, text: "The regression check passes.", endOfTurn: true}})
		h.call.observeWorking(false)
		before := len(h.context())
		h.advance(time.Minute)
		expectEqual(t, "final cannot cut off speech", len(h.context()), before)
		h.call.handle([]liveEvent{{typ: "shelley.playback", playbackActive: false}, {typ: "session.output_transcript.delta", delta: "Finishing my sentence."}})
		h.advance(time.Second)
		expectEqual(t, "captions protect the trailing audio", len(h.context()), before)
		h.advance(time.Second)
		got := h.context()
		if len(got) != before+1 || !strings.Contains(got[len(got)-1], "regression check passes") {
			t.Fatal(got)
		}
		h.advance(time.Minute)
		expectEqual(t, "idle stays quiet", h.context(), got)
	})
}

func TestLiveWorkingStatusDuringLongBackendStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		h.call.observeWorking(true)
		h.call.noteActivity([]liveActivity{{text: "config.json", kind: livePatch, phase: liveFinished, toolID: "edit"}})
		h.call.noteActivity(liveToolProgress(llm.Content{ID: "check", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"python3 -m unittest test_app"}`)}))
		h.advance(time.Second)
		before := len(h.context())
		h.advance(11 * time.Second)
		expectEqual(t, "no early heartbeat", len(h.context()), before)
		h.advance(time.Second)
		got := h.context()
		if len(got) != before+1 || !strings.Contains(got[len(got)-1], "still awaiting a result") {
			t.Fatal(got)
		}
		status := got[len(got)-1]
		if !strings.Contains(status, "result pending") || !strings.Contains(status, "unittest") || strings.Contains(status, "Applied patch") {
			t.Fatalf("pending check confused with earlier completed edit: %s", status)
		}
		h.advance(time.Minute)
		expectEqual(t, "unchanged check stays quiet", h.context(), got)
		h.call.noteActivity(liveToolProgress(llm.Content{ID: "check", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"python3 -m unittest test_app"}`)}))
		h.advance(time.Minute)
		expectEqual(t, "duplicate tool start does not rearm reminder", h.context(), got)
		h.call.noteActivity(liveToolProgress(llm.Content{ID: "next", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"go vet ./server"}`)}))
		h.advance(5 * time.Second)
		before = len(h.context())
		h.advance(12 * time.Second)
		got = h.context()
		if len(got) != before+1 || !strings.Contains(got[len(got)-1], "go vet") || strings.Contains(got[len(got)-1], "unittest") {
			t.Fatalf("new tool should get its own waiting update: %v", got)
		}
		h.call.observeWorking(false)
		got = h.context()
		h.advance(time.Minute)
		expectEqual(t, "no status after completion", h.context(), got)
	})
}

func TestLiveHangupDropsDeferredSpeech(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		h.call.handle([]liveEvent{{typ: "shelley.playback", playbackActive: true}})
		h.call.commentary("A result.", nil)
		h.call.end("test hangup")
		h.advance(time.Minute)
		expectEqual(t, "nothing spoken after hangup", len(h.context()), 0)
		if h.call.spokenTimer != nil || len(h.call.spokenQueue) != 0 {
			t.Fatal("speech retained after hangup")
		}
	})
}

func TestLiveWorkingStatusDoesNotResurrectFinishedCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		h.call.observeWorking(true)
		h.call.noteProgress("I am running the regression check.")
		started := liveToolProgress(llm.Content{ID: "check", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"python3 -m unittest test_app"}`)})
		h.call.noteActivity(started)
		start := time.Now()
		h.advance(5 * time.Second)
		end := time.Now()
		h.call.noteActivity(liveToolResult(llm.Content{ToolUseID: "check", ToolUseStartTime: &start, ToolUseEndTime: &end, Display: map[string]any{"exitCode": 0}}, started))
		h.advance(5 * time.Second)
		got := h.context()
		h.advance(time.Minute)
		expectEqual(t, "no heartbeat after the check finishes", h.context(), got)
	})
}

func TestLiveBackendWorkAloneDoesNotTriggerSpeech(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		h.call.observeWorking(true)
		before := h.context()
		h.advance(time.Minute)
		expectEqual(t, "no still-on-it filler", h.context(), before)
	})
}
