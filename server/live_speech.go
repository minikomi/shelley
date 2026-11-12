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

// Offer one waiting update per pending tool. Elapsed time alone is not news.
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
				var ids []string
				for id, tool := range c.progressTools {
					if !tool.reminded {
						ids = append(ids, id)
					}
				}
				if len(ids) == 0 {
					return
				}
				slices.Sort(ids)
				var pending []string
				for _, id := range ids[:min(3, len(ids))] {
					tool := c.progressTools[id]
					pending = append(pending, tool.report())
					tool.reminded = true
					c.progressTools[id] = tool
				}
				if len(pending) > 0 {
					c.commentary("Working status: name the specific tool still awaiting a result.\n"+strings.Join(pending, "\n"), c.progressDelegation())
					c.lastProgress = time.Now()
				}
			}
			c.scheduleProgressPulse()
		})
	})
	c.progressPulse = timer
}
