package server

import (
	"context"
	"fmt"
	"time"
)

// liveTranscriptData is append-only display metadata. Message is the original
// full chat receipt, not a rewritten request. Ordinals identify exactly the
// deltas saved here, even when newer speech has arrived between them.
type liveTranscriptData struct {
	SessionID string                   `json:"session_id"`
	Message   string                   `json:"message"`
	Fragments []liveTranscriptFragment `json:"fragments"`
}

type liveTranscriptFragment struct {
	N       int     `json:"n"`
	User    bool    `json:"user"`
	Text    string  `json:"text"`
	StartMs float64 `json:"start_ms"`
	EndMs   float64 `json:"end_ms"`
}

type liveArchiveFunc func(context.Context, liveTranscriptData) error

type liveJob struct {
	run      func()
	preserve bool // already-received extensions survive hang-up
	task     bool // archival is not a delegation in flight
}

// A snapshot opens a tail before delivery starts, so deltas received while
// chat is accepting it are retained too. Only a successful receipt unlocks
// archival or continued delivery.
type liveTail struct {
	id       string
	startMs  float64
	offsetMs float64
	through  int
	message  string
	failed   bool
	onset    *float64 // first subsequent user utterance, on the audio timeline
	archive  []liveTranscriptFragment
	user     []liveFragment
	settle   *time.Timer
	settleN  int
}

func (c *liveCall) enqueue(job liveJob) {
	c.jobs = append(c.jobs, job)
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// acceptTail runs under mu. Arrival order doesn't define exchange ownership:
// a delayed acknowledgement before the next user's onset remains in the old
// exchange, but Live's speech after that onset does not.
func (c *liveCall) acceptTail(f liveFragment) {
	if f.user {
		for _, t := range c.tails {
			if f.n > t.through && f.startMs >= t.offsetMs && (t.onset == nil || f.startMs < *t.onset) {
				onset := f.startMs
				t.onset = &onset
				c.flushContinuation(t)
			}
		}
	}
	for i := len(c.tails) - 1; i >= 0; i-- {
		t := c.tails[i]
		if t.failed || f.n <= t.through {
			continue
		}
		if f.user {
			if f.startMs >= t.offsetMs || (i > 0 && f.startMs < t.startMs) {
				continue
			}
			t.user = append(t.user, f)
			if t.settle != nil {
				t.settle.Stop()
			}
			t.settleN++
			n := t.settleN
			t.settle = time.AfterFunc(liveQuiet, func() {
				c.locked(func() {
					if t.settleN == n {
						c.flushContinuation(t)
					}
				})
			})
		} else {
			if f.startMs < t.startMs || (t.onset != nil && f.startMs >= *t.onset) {
				continue
			}
			t.archive = append(t.archive, liveTranscriptFragment{
				N: f.n, User: f.user, Text: f.text, StartMs: f.startMs, EndMs: f.endMs,
			})
		}
		return
	}
}

func (c *liveCall) flushArchives() {
	for _, t := range c.tails {
		if t.message == "" || len(t.archive) == 0 {
			continue
		}
		data := liveTranscriptData{SessionID: c.id, Message: t.message, Fragments: t.archive}
		t.archive = nil
		c.enqueue(liveJob{preserve: true, run: func() {
			var err error
			if c.archive == nil {
				err = fmt.Errorf("Live transcript archival is not configured")
			} else {
				err = c.archive(context.Background(), data)
			}
			if err != nil {
				c.locked(func() {
					// Retain failed batches for a later explicit event/hang-up
					// retry. Never signal the client to consume unsaved deltas.
					t.archive = append(data.Fragments, t.archive...)
					c.logger.Error("Live transcript could not be saved", "error", err)
					c.error("Failed to save the late Live transcript: " + err.Error())
				})
			}
		}})
	}
}

func (c *liveCall) flushContinuation(t *liveTail) {
	if t.failed || t.message == "" || len(c.pending) > 0 {
		return
	}
	var fragments []liveFragment
	var ordinals []int
	for _, f := range t.user {
		if !c.taken[f.n] {
			fragments = append(fragments, f)
			ordinals = append(ordinals, f.n)
		}
	}
	t.user = nil
	speech := liveSpeech(fragments)
	if speech == "" {
		return
	}
	if liveEcho(speech, fragments[0].startMs, liveLatestReply(c.fragments)) {
		for _, n := range ordinals {
			c.taken[n] = true
		}
		return
	}
	for _, n := range ordinals {
		c.taken[n] = true
	}
	through := ordinals[len(ordinals)-1]
	voiceContext := liveVoiceContext(fragments, 0)
	c.enqueue(liveJob{preserve: true, task: true, run: func() {
		c.deliverTask(t, "Spoken request (continued):", speech, voiceContext, through, ordinals, true)
	}})
	c.activity()
}
