package server

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

const liveProgressCoalesce = time.Second
const liveCheckDelay = 5 * time.Second

// progressTick offers new evidence to Live. The caller holds mu.
func (c *liveCall) progressTick(timer *time.Timer) {
	if !c.active || c.tick == nil || c.tick != timer {
		return
	}
	c.tick.Stop()
	c.tick = nil
	var delegation *string
	for _, s := range c.submitted {
		if !s.finished && s.userSeq != 0 {
			delegation = &s.id
		}
	}
	if len(c.progressNotes) == 0 && len(c.progressActivity) == 0 {
		return
	}
	// Consequential facts survive later inspections. Findings lead, but cannot
	// consume the entire payload and hide the concrete work.
	slices.SortStableFunc(c.progressActivity, func(a, b liveActivity) int { return int(b.kind - a.kind) })
	heading := liveProgressHeading
	if len(c.progressNotes) > 0 {
		heading = liveReportHeading
	}
	budget := liveProgressChars - utf8.RuneCountInString(heading)
	reserve := 0
	if len(c.progressActivity) > 0 {
		reserve = min(300, utf8.RuneCountInString(c.progressActivity[0].text)) + 1
	}
	limit := budget - reserve
	content := strings.Join(c.progressNotes, "\n")
	if utf8.RuneCountInString(content) > limit {
		latest := c.progressNotes[len(c.progressNotes)-1]
		if utf8.RuneCountInString(latest) <= limit {
			content = latest
		} else {
			content = truncateRunes(content, limit-1)
		}
	}
	var pending []liveActivity
	for _, activity := range c.progressActivity {
		// Reads are expendable context once this batch has a concrete change.
		if activity.kind == liveInspect && c.progressActivity[0].kind > liveInspect {
			continue
		}
		remaining := budget - utf8.RuneCountInString(content)
		if content != "" {
			remaining--
		}
		if utf8.RuneCountInString(activity.text) > remaining {
			if activity.kind != liveInspect {
				pending = append(pending, activity)
			}
			continue
		}
		if content != "" {
			content += "\n"
		}
		content += activity.text
		if area := inspectionArea(activity); area != "" {
			c.rememberProgress(area)
		}
	}
	if content != "" {
		c.commentary(heading+content, delegation)
		c.lastProgress = time.Now()
	}
	c.progressNotes = nil
	c.progressActivity = pending
	if slices.ContainsFunc(pending, liveActivity.isOutcome) {
		c.scheduleProgress(liveProgressCoalesce)
	}
}

func (c *liveCall) startProgress() {
	if c.progressEvery > 0 && c.progressSeen == nil {
		c.progressSeen = []string{}
	}
}

// Findings and outcomes get a brief window to merge adjacent rows. Routine
// activity waits for the user's spacing. A later event can only advance a batch.
func (c *liveCall) scheduleProgress(delay time.Duration) {
	if c.progressEvery <= 0 {
		return
	}
	// Leave a short gap between substantial changes, even when the agent emits
	// multiple patch results in quick succession.
	delay = max(delay, time.Until(c.lastProgress.Add(5*time.Second)))
	due := time.Now().Add(delay)
	if c.tick != nil {
		if !due.Before(c.progressDue) {
			return
		}
		c.tick.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() { c.locked(func() { c.progressTick(timer) }) })
	c.tick, c.progressDue = timer, due
}

// After a directory has featured in speech context, another file read there
// alone adds little. A public report can still carry specifics from that area.
func inspectionArea(a liveActivity) string {
	if a.kind == liveInspect {
		if target, ok := strings.CutPrefix(a.key(), "inspect:"); ok && path.Ext(target) != "" {
			return "area:" + path.Dir(target)
		}
	}
	return ""
}

func (c *liveCall) stopProgress() {
	c.progressNotes = nil
	c.progressActivity = nil
	c.progressSeen = nil
	c.lastProgress = time.Time{}
	if c.tick != nil {
		c.tick.Stop()
		c.tick = nil
	}
}

func (c *liveCall) observeWorking(working bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active || c.progressEvery == 0 {
		return
	}
	if working {
		c.startProgress()
	} else {
		c.stopProgress()
	}
}

func (c *liveCall) noteActivity(activities []liveActivity) {
	var fresh []liveActivity
	for _, activity := range activities {
		activity.text = truncateRunes(activity.text, 299)
		if area := inspectionArea(activity); area != "" && len(c.progressNotes) == 0 && slices.Contains(c.progressSeen, area) {
			continue
		}
		if activity.text != "" && c.rememberProgress("activity:"+activity.key()) {
			fresh = append(fresh, activity)
		}
	}
	// Replace the previous phase once per batch, without erasing a pending
	// result when the same evidence arrives twice.
	for _, activity := range fresh {
		if activity.done && activity.toolID != "" {
			c.progressActivity = slices.DeleteFunc(c.progressActivity, func(p liveActivity) bool {
				return p.toolID == activity.toolID
			})
		}
	}
	// A successful shell exit retires its start but proves no test result.
	fresh = slices.DeleteFunc(fresh, func(a liveActivity) bool {
		return a.done && strings.HasPrefix(a.text, "Shell exited with status 0;")
	})
	important := slices.ContainsFunc(fresh, liveActivity.isOutcome)
	c.progressActivity = append(c.progressActivity, fresh...)
	slices.SortStableFunc(c.progressActivity, func(a, b liveActivity) int { return int(b.kind - a.kind) })
	if len(c.progressActivity) > 32 {
		c.progressActivity = c.progressActivity[:32]
	}
	// During the quiet period, retain concrete context for the next change.
	// The passage of time alone must not turn it into an announcement.
	if important {
		c.scheduleProgress(liveProgressCoalesce)
	} else if slices.ContainsFunc(fresh, func(a liveActivity) bool { return a.directCheck && !a.done }) {
		c.scheduleProgress(liveCheckDelay)
	} else if len(fresh) > 0 && time.Since(c.lastProgress) >= c.progressEvery {
		c.scheduleProgress(c.progressEvery / 2)
	} else if len(c.progressNotes) == 0 && len(c.progressActivity) == 0 && c.tick != nil {
		c.tick.Stop()
		c.tick = nil
	}
}

func (c *liveCall) noteProgress(note string) {
	note = truncateRunes(note, liveProgressChars)
	if note == "" || !c.rememberProgress("note: "+note) {
		return
	}
	// Preserve the boundary of the newest report inside the same bounded buffer.
	previous := strings.Join(c.progressNotes, "\n")
	limit := liveProgressChars - len(liveReportHeading)
	if r := []rune(note); len(r) > limit {
		note = "…" + string(r[len(r)-limit+1:])
	}
	room := limit - utf8.RuneCountInString(note) - 1
	c.progressNotes = nil
	if previous != "" && room > 0 {
		if r := []rune(previous); len(r) > room {
			previous = "…" + string(r[len(r)-room+1:])
		}
		c.progressNotes = append(c.progressNotes, previous)
	}
	c.progressNotes = append(c.progressNotes, note)
	c.scheduleProgress(liveProgressCoalesce)
}

// One bounded deduplication window for pending and delivered facts.
func (c *liveCall) rememberProgress(fact string) bool {
	if slices.Contains(c.progressSeen, fact) {
		return false
	}
	c.progressSeen = append(c.progressSeen, fact)
	if len(c.progressSeen) > 32 {
		c.progressSeen = c.progressSeen[1:]
	}
	return true
}

// Use the same text for streamed and persisted notes so the call deduplicates them.
func livePublicNote(text string) string {
	text = strings.TrimSpace(llm.StripInlineCitationMarkers(text))
	if text == "" {
		return ""
	}
	return "notes: " + truncateRunes(text, liveProgressChars)
}

// liveProgress reports agent activity and tool failures, plus whether an
// agent or error row ends the turn even without a reply.
func liveProgress(row generated.Message, tools map[string][]liveActivity) (notes []string, activities []liveActivity, endOfTurn bool) {
	if row.LlmData == nil || (row.Type != "agent" && row.Type != "user" && row.Type != "error") {
		return nil, nil, false
	}
	var message llm.Message
	if json.Unmarshal([]byte(*row.LlmData), &message) != nil {
		return nil, nil, false
	}
	// Turn-ending markers are often excluded from context; they still end it.
	if (row.Type == "agent" || row.Type == "error") && message.EndOfTurn {
		clear(tools)
		return nil, nil, true
	}
	if row.Type == "error" || row.ExcludedFromContext || message.ExcludedFromContext {
		return nil, nil, false
	}
	if row.Type == "user" && row.UserData != nil {
		var job backgroundJobUserData
		if json.Unmarshal([]byte(*row.UserData), &job) == nil && job.BackgroundJobID != "" {
			fact := liveActivity{toolID: job.BackgroundJobID, done: true, kind: liveCheck}
			if started := tools[job.BackgroundJobID]; len(started) > 0 {
				fact.toolID = started[0].toolID
			}
			delete(tools, job.BackgroundJobID)
			if job.ExitCode == nil {
				fact.text, fact.kind = "A background command was lost; completion is unknown.", liveFailure
			} else {
				steps, _ := liveShellProgress(job.Command)
				fact.text = fmt.Sprintf("Background command exited with status %d: %s", *job.ExitCode, strings.Join(steps, "; "))
				if *job.ExitCode != 0 {
					fact.kind = liveFailure
				}
			}
			return nil, []liveActivity{fact}, false
		}
	}

	for _, c := range message.Content {
		switch {
		case c.Type == llm.ContentTypeToolResult:
			started := tools[c.ToolUseID]
			facts := liveToolResult(c, started)
			activities = append(activities, facts...)
			delete(tools, c.ToolUseID)
			// Keep the same operation identity through its background completion.
			status := liveToolStatus(c)
			if !c.ToolError && status.Background != nil && status.Background.JobID != "" && status.ExitCode == nil && len(started) > 0 && tools != nil {
				if len(tools) >= 128 {
					clear(tools)
				}
				tools[status.Background.JobID] = started
			}
		case row.Type == "agent" && c.Type == llm.ContentTypeText && strings.TrimSpace(c.Text) != "":
			if note := livePublicNote(c.Text); note != "" {
				notes = append(notes, note)
			}
		case row.Type == "agent" && c.Type == llm.ContentTypeServerToolUse && c.ToolName == "web_search":
			activities = append(activities, liveActivity{text: "Starting: searching the web", kind: liveInspect, toolID: c.ID})
		case row.Type == "agent" && c.Type == llm.ContentTypeToolUse:
			if facts := liveToolProgress(c); len(facts) > 0 {
				if tools == nil { // Worker results are not observed by this call.
					for i := range facts {
						facts[i].directCheck = false
					}
				}
				activities = append(activities, facts...)
				if tools != nil && c.ID != "" {
					if len(tools) >= 128 {
						clear(tools)
					}
					tools[c.ID] = facts
				}
			}
		}
	}
	return notes, activities, false
}
