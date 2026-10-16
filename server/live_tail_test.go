package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestLiveTranscriptSavesLateLiveOnlyIncident(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		const message = "Spoken request:\nCap the panel\n\nVoice exchange (for context only):\nUSER: Cap the panel\nLIVE: Okay, I'll pass that along. And yeah, I'll have them make sure the live chat"
		h.set(func() {
			h.reply = func(string) (liveReceipt, error) {
				return liveReceipt{Status: "queued", Message: message}, nil
			}
		})
		h.say("Cap the panel", 0, 500)
		h.liveSays("Okay, I'll pass that along. And yeah, I'll have them make sure the live chat", 600, 3000)
		h.delegate("d1", 3000)
		h.advance(liveQuiet)
		h.liveSays(" panel doesn't get too tall.", 3000, 4000)
		synctest.Wait() // archival is immediate, with no second delegation or quiet timer
		expectEqual(t, "tasks", h.transcripts(), []string{"Cap the panel"})
		expectEqual(t, "archive", h.archives, []liveTranscriptData{{
			SessionID: "call", Message: message,
			Fragments: []liveTranscriptFragment{{N: 3, Text: " panel doesn't get too tall.", StartMs: 3000, EndMs: 4000}},
		}})
		h.call.end("hung up")
		synctest.Wait()
		expectEqual(t, "no duplicate archive on hangup", len(h.archives), 1)
	})
}

func TestLiveTranscriptRetainsDeltasBeforeReceiptAndThroughHangup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.set(func() { h.hold = make(chan struct{}) })
		h.say("Run tests", 0, 1000)
		h.delegate("d1", 2000)
		h.advance(liveQuiet)
		h.liveSays("I'm handing that off.", 2000, 3000)
		h.say(" and lint", 1000, 1500)
		h.call.end("hung up before receipt")
		expectEqual(t, "not archived before success", len(h.archives), 0)
		h.set(func() { close(h.hold) })
		synctest.Wait()
		expectEqual(t, "received late user words survive", h.transcripts(), []string{"Run tests", "and lint"})
		expectEqual(t, "continuation heading", h.headings, []string{"Spoken request:", "Spoken request (continued):"})
		expectEqual(t, "late Live survives", h.archives, []liveTranscriptData{{
			SessionID: "call", Message: "Run tests",
			Fragments: []liveTranscriptFragment{{N: 2, Text: "I'm handing that off.", StartMs: 2000, EndMs: 3000}},
		}})
		expectEqual(t, "no successful handoff after hangup", h.instructions(), []string{"activity: true"})
	})
}

func TestLiveTranscriptTailOwnershipUsesUserOnsetNotArrivalOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run tests", 0, 500)
		h.delegate("d1", 600)
		h.advance(liveQuiet)
		h.say("What else can you do?", 5000, 6000)
		h.liveSays(" handed off.", 700, 1500) // delayed old acknowledgement
		h.liveSays("I can also lint.", 6100, 7000)
		synctest.Wait()
		expectEqual(t, "old ack only", h.archives, []liveTranscriptData{{
			SessionID: "call", Message: "Run tests",
			Fragments: []liveTranscriptFragment{{N: 3, Text: " handed off.", StartMs: 700, EndMs: 1500}},
		}})
		expectEqual(t, "no new tasks from Live speech", h.transcripts(), []string{"Run tests"})
		h.delegate("d2", 7000)
		h.advance(liveQuiet)
		h.liveSays(" still the old acknowledgement.", 1600, 2000)
		synctest.Wait()
		expectEqual(t, "delayed old ack after newer handoff", h.archives[1].Message, "Run tests")
	})
}

func TestLiveTranscriptContinuationConsumesOnlySparseUserOrdinals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.set(func() { h.hold = make(chan struct{}) })
		h.say("Change the panel", 0, 500)
		h.liveSays("Handing it off", 1600, 2000)
		h.delegate("d1", 2000)
		h.advance(liveQuiet)
		h.say(" and cap", 500, 1000)     // n=3, old request
		h.say("Run lint", 5000, 5500)    // n=4, next request
		h.say(" its height", 1000, 1500) // n=5, delayed old request
		h.set(func() { close(h.hold) })
		synctest.Wait()
		expectEqual(t, "continued speech", h.transcripts(), []string{"Change the panel", "and cap its height"})
		expectEqual(t, "continued heading", h.headings[1], "Spoken request (continued):")
		all, _, _ := h.call.instructions(0)
		var handoffs []struct {
			Type      string `json:"type"`
			Through   int    `json:"through"`
			Fragments []int  `json:"fragments"`
		}
		for _, instruction := range all {
			var wire struct {
				Type      string `json:"type"`
				Through   int    `json:"through"`
				Fragments []int  `json:"fragments"`
			}
			if err := json.Unmarshal(instruction.data, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Type == "handoff" {
				handoffs = append(handoffs, wire)
			}
		}
		if len(handoffs) != 2 {
			t.Fatalf("handoffs = %+v", handoffs)
		}
		expectEqual(t, "original prefix unchanged", handoffs[0].Fragments, []int(nil))
		expectEqual(t, "sparse continuation", handoffs[1].Fragments, []int{3, 5})
		expectEqual(t, "continuation through", handoffs[1].Through, 5)
		if strings.Contains(h.deliveries[1].voiceContext, "Run lint") {
			t.Fatal("following utterance entered continuation context")
		}
		h.delegate("d2", 5500)
		h.advance(liveQuiet)
		expectEqual(t, "next utterance remains and no repeated late words", h.transcripts(), []string{"Change the panel", "and cap its height", "Run lint"})
	})
}

func TestLiveTranscriptNextDelegationCanClaimLateUserWords(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run tests", 0, 500)
		h.delegate("d1", 2000)
		h.advance(liveQuiet)
		h.say(" and lint", 500, 1000)
		h.delegate("d2", 1800)
		h.advance(2 * liveQuiet)
		expectEqual(t, "no duplicate continuation", h.transcripts(), []string{"Run tests", "and lint"})
		expectEqual(t, "both ordinary delegations", h.headings, []string{"Spoken request:", "Spoken request:"})
	})
}

func TestLiveTranscriptUnusableNextDelegationDoesNotDropContinuation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run tests", 0, 500)
		h.delegate("d1", 2000)
		h.advance(liveQuiet)
		h.say(" and lint", 500, 1000)
		// This later delegation cannot claim speech from so long ago.
		h.delegate("d2", 30_000)
		h.advance(liveDeadline)
		expectEqual(t, "late words still delivered", h.transcripts(), []string{"Run tests", "and lint"})
		expectEqual(t, "continuation, not unusable delegation", h.headings, []string{"Spoken request:", "Spoken request (continued):"})
	})
}

func TestLiveTranscriptContinuationKeepsEchoGuard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run tests", 0, 500)
		h.liveSays("Shelley is working on that request now", 600, 2000)
		h.delegate("d1", 2000)
		h.advance(liveQuiet)
		h.say("Shelley is working on that request", 700, 1900)
		h.advance(liveQuiet)
		expectEqual(t, "echo never becomes a task", h.transcripts(), []string{"Run tests"})
	})
}

func TestLiveTranscriptFailedOriginalLeavesPendingAndNeverArchives(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.set(func() {
			h.hold = make(chan struct{})
			h.reply = func(string) (liveReceipt, error) { return liveReceipt{}, errors.New("delivery failed") }
		})
		h.say("Run tests", 0, 500)
		h.delegate("d1", 2000)
		h.advance(liveQuiet)
		h.liveSays("handing it off", 600, 2000)
		h.say(" and lint", 500, 1000)
		h.set(func() { close(h.hold) })
		synctest.Wait()
		expectEqual(t, "no archive for failed task", len(h.archives), 0)
		expectEqual(t, "original user words restored", liveSpeech(h.call.unclaimedUser()), "Run tests and lint")
		h.set(func() {
			h.reply = func(s string) (liveReceipt, error) { return liveReceipt{Status: "accepted", Message: s}, nil }
		})
		h.delegate("d2", 2000)
		h.advance(liveQuiet)
		expectEqual(t, "retry keeps all request words", h.transcripts(), []string{"Run tests", "Run tests and lint"})
	})
}

func TestLiveTranscriptArchiveFailureIsVisibleAndRetained(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.call.archive = func(context.Context, liveTranscriptData) error { return errors.New("disk full") }
		h.say("Run tests", 0, 500)
		h.delegate("d1", 500)
		h.advance(liveQuiet)
		h.liveSays("handing it off", 600, 2000)
		synctest.Wait()
		instructions := h.instructions()
		if got := instructions[len(instructions)-1]; got != "error: Failed to save the late Live transcript: disk full" {
			t.Fatalf("failure not surfaced: %q", got)
		}
		expectEqual(t, "failed archive retained", len(h.call.tails[0].archive), 1)
		expectEqual(t, "no task dispatch for archive", h.transcripts(), []string{"Run tests"})
	})
}

func TestLiveTranscriptHangupFlushesReceivedContinuationWithoutQuietWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveHarness(t)
		h.say("Run tests", 0, 500)
		h.delegate("d1", 2000)
		h.advance(liveQuiet)
		h.say(" and lint", 500, 1000)
		h.call.end("hung up")
		synctest.Wait()
		expectEqual(t, "received continuation delivered immediately", h.transcripts(), []string{"Run tests", "and lint"})
		h.advance(time.Second)
		expectEqual(t, "only once", len(h.transcripts()), 2)
	})
}
