package server

import (
	"strings"
	"testing"
	"testing/synctest"
	"unicode/utf8"

	"shelley.exe.dev/llm"
)

func TestLivePublicNotePreservesCorrectionAndDeduplicatesPersistedNote(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newLiveProgressHarness(t, true)
		correction := "Correction: the final answer is 42."
		raw := strings.Repeat("界", 900) + "\n" + correction

		h.call.notePublicProgress(llm.PublicProgress{Commentary: raw})
		notes, _, _ := liveProgress(dialogueRow("agent", raw, false, ""), nil)
		if len(notes) != 1 {
			t.Fatalf("persisted notes = %v, want one normalized note", notes)
		}
		h.call.observe([]liveRow{{seq: 11, progress: notes}})
		h.call.locked(func() { h.call.progressTick(h.call.tick) })

		got := h.context()
		if len(got) != 1 || !strings.Contains(got[0], correction) {
			t.Fatalf("correction missing from streamed/persisted update: %v", got)
		}
		if n := utf8.RuneCountInString(got[0][len("commentary [-]: "):]); n > liveProgressChars {
			t.Fatalf("progress payload has %d runes, limit is %d", n, liveProgressChars)
		}
	})
}
