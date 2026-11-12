package server

import (
	"slices"
	"strings"
	"time"
)

type liveSpokenUpdate struct {
	content    string
	delegation *string
}

// Playback is measured in the browser. Captions also protect the caller's
// speech and the interval before output audio reaches the player.
func (c *liveCall) speechBusy() bool {
	return c.playbackActive || time.Since(c.lastSpeech) < 2*time.Second
}

func (c *liveCall) scheduleSpeech() {
	if c.spokenTimer != nil {
		return
	}
	c.spokenTimer = time.AfterFunc(time.Second, func() {
		c.locked(func() {
			c.spokenTimer = nil
			if !c.active || len(c.spokenQueue) == 0 {
				return
			}
			if !c.speechBusy() {
				next := c.spokenQueue[0]
				c.spokenQueue = c.spokenQueue[1:]
				c.appendContext("session.commentary.append", next.content, next.delegation)
				// Allow speech to begin before offering another queued result.
				c.lastSpeech = time.Now()
			}
			if len(c.spokenQueue) > 0 {
				c.scheduleSpeech()
			}
		})
	})
}

// Backend work can take a minute between observable steps. Report that it is
// still active without inventing another tool run or repeating a completed one.
func (c *liveCall) scheduleProgressPulse() {
	if c.progressPulse != nil {
		return
	}
	last := c.lastProgress
	delay := liveProgressSpacing
	if !last.IsZero() {
		delay = max(time.Second, time.Until(last.Add(liveProgressSpacing)))
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		c.locked(func() {
			if c.progressPulse != timer {
				return
			}
			c.progressPulse = nil
			if !c.active || c.progressSeen == nil || c.backendWorking == nil || !*c.backendWorking {
				return
			}
			if !c.speechBusy() && len(c.spokenQueue) == 0 && c.tick == nil && time.Since(c.lastProgress) >= liveProgressSpacing {
				content := "Working status: this is a snapshot of existing state, not a new tool event or another completion. The overall task is still active, with no final answer. Report the confirmed tool state below, then explain that work on the overall task continues."
				var pending []string
				for _, tool := range c.progressTools {
					pending = append(pending, tool.report())
				}
				slices.Sort(pending)
				if len(pending) > 0 {
					content += "\nSubmitted tools awaiting a confirmed result. No completion or success has been reported for these tools; say that their result is still pending:\n" + strings.Join(pending[:min(3, len(pending))], "\n")
				} else if c.progressLast != "" {
					content += "\nMost recent completed tool outcome (the tool finished; the overall task continues):\n" + c.progressLast
				}
				c.commentary(content, c.progressDelegation())
				c.lastProgress = time.Now()
			}
			c.scheduleProgressPulse()
		})
	})
	c.progressPulse = timer
}
