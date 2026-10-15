package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"
)

// A Live call's delegation logic. GPT-Live decides when to delegate; the
// call collects the speech around each client delegation, waits for late
// transcript, hands it to the backend, and keeps Live informed of what the
// backend says. The client only relays: Live's events in (handle), and
// instructions out (the outbox, read by the call's stream).

const (
	// Live has no transcript turn boundary: a delegation settles after late
	// deltas, with a hard deadline for an interrupted utterance.
	liveQuiet            = 1200 * time.Millisecond
	liveDeadline         = 5 * time.Second
	liveMaxPerMinute     = 12
	liveFeedbackLoop     = "Voice delegation paused to prevent a feedback loop. Start voice again to continue."
	liveInvalidOffset    = "Live returned an invalid delegation timestamp."
	liveNoRequest        = "No user request was captured for this delegation. Do not report progress. If the user asked for something, ask them to repeat it."
	liveDeliveryFailed   = "The last delegation could not be sent to the backend. Tell the user briefly and ask them to repeat it. Do not report progress."
	liveFragmentsKept    = 500
	liveFragmentsMax     = 1000
	liveInstructionsKept = 256
	// liveIdle ends a call that has had no instruction stream this long.
	liveIdle = time.Minute
)

// liveReceipt is chat's answer to a delivered delegation.
type liveReceipt struct {
	Status  string `json:"status"` // "accepted" or "queued"
	Message string `json:"message"`
}

// liveDeliverFunc hands speech and its voice exchange to the backend.
type liveDeliverFunc func(ctx context.Context, transcript, voiceContext string) (liveReceipt, error)

// liveEvent is the part of a Live data-channel event a call reads.
type liveEvent struct {
	typ                            string
	delta                          string
	startMs, endMs                 float64
	offsetMs                       json.RawMessage
	delegationID, delegationTarget string
}

// parseLiveEvent reads an event object as leniently as Live's clients
// did; ok is false if raw is not a JSON object.
func parseLiveEvent(raw json.RawMessage) (e liveEvent, ok bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return e, false
	}
	json.Unmarshal(fields["type"], &e.typ)
	var delta any
	if json.Unmarshal(fields["delta"], &delta) == nil {
		switch d := delta.(type) {
		case nil:
		case string:
			e.delta = d
		default:
			e.delta = string(fields["delta"])
		}
	}
	e.startMs, _ = liveNumber(fields["start_ms"])
	e.endMs, _ = liveNumber(fields["end_ms"])
	e.offsetMs = fields["offset_ms"]
	var delegation map[string]any
	json.Unmarshal(fields["delegation"], &delegation)
	e.delegationID, _ = delegation["id"].(string)
	e.delegationTarget, _ = delegation["target"].(string)
	return e, true
}

// liveNumber reads a JSON number; ok is false for anything else.
func liveNumber(raw json.RawMessage) (n float64, ok bool) {
	return n, len(raw) > 0 && raw[0] != 'n' && json.Unmarshal(raw, &n) == nil
}

type livePending struct {
	offsetMs float64
	settle   *time.Timer
	settleN  int // the settle timer that may commit
	deadline *time.Timer
}

// liveSubmitted is a delegation the backend took: the message it became,
// to recognise its row, and whether its reply has arrived.
type liveSubmitted struct {
	id       string
	message  string
	afterSeq int64
	userSeq  int64 // 0 until its row arrives
	finished bool
}

type liveInstruction struct {
	id   int64
	data []byte
}

type liveCall struct {
	id, conversationID string
	deliver            liveDeliverFunc
	logger             *slog.Logger

	mu          sync.Mutex
	active      bool
	done        chan struct{} // closed when the call ends
	fragments   []liveFragment
	transcriptN int // accepted transcript deltas, independent of fragment trimming
	claimed     int // fragments already part of a delegation
	seen        map[string]bool
	pending     map[string]*livePending
	order       []string // pending, oldest first
	sendTimes   []time.Time
	jobs        []func()
	kick        chan struct{}
	inflight    int
	handing     bool
	submitted   []*liveSubmitted
	lastSeen    int64 // the newest row Live heard about
	latest      int64
	unseen      map[int64]liveRow
	eventSeq    int

	out    []liveInstruction
	nextID int64
	wake   chan struct{} // closed and replaced on each instruction

	streams  int
	attached chan struct{} // closed when a newer stream attaches
	idle     *time.Timer
}

// liveRow is a conversation row the call may tell Live about: its
// dialogue text ("" if none, see liveDialogue) and whole visible text.
type liveRow struct {
	seq        int64
	user       bool
	text, full string
}

// newLiveCall starts a call whose Live session already holds the
// conversation up to sequence initialSeq.
func newLiveCall(id, conversationID string, initialSeq int64, deliver liveDeliverFunc, logger *slog.Logger) *liveCall {
	c := &liveCall{
		id: id, conversationID: conversationID, deliver: deliver,
		logger: logger.With("liveCall", id, "conversationID", conversationID),
		active: true, done: make(chan struct{}), seen: map[string]bool{}, pending: map[string]*livePending{},
		kick: make(chan struct{}, 1), lastSeen: initialSeq, latest: initialSeq, unseen: map[int64]liveRow{},
		wake: make(chan struct{}), attached: make(chan struct{}),
	}
	c.idle = time.AfterFunc(liveIdle, func() { c.end("no instruction stream") })
	go c.deliveries()
	return c
}

// handle applies Live's events, in the order Live sent them.
func (c *liveCall) handle(events []liveEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range events {
		if !c.active {
			return
		}
		switch e.typ {
		case "session.input_transcript.delta", "session.output_transcript.delta":
			user := e.typ == "session.input_transcript.delta"
			c.transcriptN++
			c.fragments = append(c.fragments, liveFragment{user: user, text: e.delta, startMs: e.startMs, endMs: e.endMs})
			if len(c.fragments) > liveFragmentsMax && len(c.pending) == 0 {
				drop := len(c.fragments) - liveFragmentsKept
				c.fragments = slices.Clone(c.fragments[drop:])
				c.claimed = max(0, c.claimed-drop)
			}
			for _, id := range c.order {
				c.settle(id)
			}
		case "session.delegation.created":
			c.delegation(e)
		case "session.closed":
			c.endLocked("Live closed the session")
		}
	}
}

func (c *liveCall) delegation(e liveEvent) {
	id := e.delegationID
	if id == "" || e.delegationTarget != "client" || c.seen[id] {
		c.logger.Info("Live delegation ignored", "delegation", id, "target", e.delegationTarget, "seen", c.seen[id])
		return
	}
	c.seen[id] = true
	offset := 0.0
	if len(e.offsetMs) > 0 && string(e.offsetMs) != "null" {
		var ok bool
		if offset, ok = liveNumber(e.offsetMs); !ok || offset < 0 {
			c.logger.Info("Live delegation dropped: invalid offset", "delegation", id, "offset", string(e.offsetMs))
			c.error(liveInvalidOffset)
			return
		}
	}
	p := &livePending{offsetMs: offset}
	p.deadline = time.AfterFunc(liveDeadline, func() { c.locked(func() { c.commit(id) }) })
	c.pending[id] = p
	c.order = append(c.order, id)
	c.activity()
	// Settle now only if the user's speech already leads up to the
	// delegation; otherwise wait for its transcript.
	user := c.unclaimedUser()
	if len(user) > 0 && liveTurnEnd(user[len(user)-1].endMs, c.fragments)+liveUserLeadMs >= offset {
		c.settle(id)
	}
}

func (c *liveCall) locked(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f()
}

// settle (re)starts a delegation's quiet period.
func (c *liveCall) settle(id string) {
	p := c.pending[id]
	if p.settle != nil {
		p.settle.Stop()
	}
	p.settleN++
	n := p.settleN
	p.settle = time.AfterFunc(liveQuiet, func() {
		c.locked(func() {
			if p := c.pending[id]; p != nil && p.settleN == n {
				c.commit(id)
			}
		})
	})
}

func (c *liveCall) unclaimedUser() []liveFragment {
	var user []liveFragment
	for _, f := range c.fragments[c.claimed:] {
		if f.user {
			user = append(user, f)
		}
	}
	return user
}

// commit decides what a delegation becomes, once its transcript settled.
func (c *liveCall) commit(id string) {
	p := c.pending[id]
	if !c.active || p == nil {
		return
	}
	delete(c.pending, id)
	c.order = slices.DeleteFunc(c.order, func(o string) bool { return o == id })
	if p.settle != nil {
		p.settle.Stop()
	}
	p.deadline.Stop()
	defer c.activity()

	since := c.claimed
	speech, start, ok := liveUtterance(c.unclaimedUser(), p.offsetMs, c.fragments)
	c.claimed = len(c.fragments)
	if !ok || liveEcho(speech, start, liveLatestReply(c.fragments)) {
		c.logger.Info("Live delegation dropped: no user request", "delegation", id, "speech", ok)
		c.thinking(liveNoRequest, &id)
		return
	}
	now := time.Now()
	c.sendTimes = slices.DeleteFunc(c.sendTimes, func(t time.Time) bool { return now.Sub(t) >= time.Minute })
	if len(c.sendTimes) >= liveMaxPerMinute {
		c.logger.Warn("Live delegations look like a feedback loop; ending the call", "delegation", id)
		c.emit(map[string]string{"type": "stop", "message": liveFeedbackLoop})
		c.endLocked("feedback loop")
		return
	}
	c.sendTimes = append(c.sendTimes, now)
	voiceContext := liveVoiceContext(c.fragments, since)
	c.logger.Info("Live delegation committed", "delegation", id, "chars", len(speech))
	through := c.transcriptN
	// Delegations reach the backend in order.
	c.jobs = append(c.jobs, func() { c.deliverTask(id, speech, voiceContext, through) })
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *liveCall) deliveries() {
	for {
		select {
		case <-c.kick:
		case <-c.done:
			return
		}
		for {
			c.mu.Lock()
			if !c.active || len(c.jobs) == 0 {
				c.mu.Unlock()
				break
			}
			job := c.jobs[0]
			c.jobs = c.jobs[1:]
			c.mu.Unlock()
			job()
		}
	}
}

func (c *liveCall) deliverTask(id, speech, voiceContext string, through int) {
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return
	}
	c.inflight++
	afterSeq := c.latest
	c.activity()
	c.mu.Unlock()

	// A delivery outlives a hang-up, as a sent chat message would.
	receipt, err := c.deliver(context.Background(), speech, voiceContext)

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return
	}
	c.inflight--
	if err != nil {
		c.logger.Info("Live delegation failed", "delegation", id, "error", err)
		c.error(err.Error())
		c.thinking(liveDeliveryFailed, &id)
	} else {
		c.logger.Info("Live delegation delivered", "delegation", id, "status", receipt.Status)
		c.emit(map[string]any{"type": "error", "message": nil})
		c.emit(map[string]any{"type": "handoff", "through": through, "message": receipt.Message})
		c.submitted = append(c.submitted, &liveSubmitted{id: id, message: receipt.Message, afterSeq: afterSeq})
		if receipt.Status == "queued" {
			c.thinking("This task is queued behind earlier work: "+speech, &id)
		} else {
			c.thinking("The backend accepted this task: "+speech, &id)
		}
	}
	c.sync()
	c.activity()
}

// observe takes conversation rows; any after the newest Live heard about
// are told to Live, in order. Duplicates are fine.
func (c *liveCall) observe(rows []liveRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return
	}
	for _, row := range rows {
		if row.seq > c.lastSeen {
			c.unseen[row.seq] = row
			c.latest = max(c.latest, row.seq)
		}
	}
	c.sync()
}

// sync keeps Live informed of the backend's dialogue, replies scoped to the
// delegation they answer. Rows wait while a delivery is in flight: the
// delegation's own message may arrive before its receipt.
func (c *liveCall) sync() {
	if !c.active || c.inflight > 0 {
		return
	}
	var rows []liveRow
	for _, row := range c.unseen {
		rows = append(rows, row)
	}
	clear(c.unseen)
	slices.SortFunc(rows, func(a, b liveRow) int { return int(a.seq - b.seq) })
	for _, row := range rows {
		if row.seq <= c.lastSeen {
			continue
		}
		c.lastSeen = row.seq
		if row.text == "" {
			continue
		}
		if row.user {
			i := slices.IndexFunc(c.submitted, func(s *liveSubmitted) bool {
				return s.userSeq == 0 && row.seq > s.afterSeq && s.message == row.full
			})
			if i >= 0 {
				c.submitted[i].userSeq = row.seq
			} else {
				c.thinking("New typed message to the backend: "+row.text, nil)
			}
			continue
		}
		var current *liveSubmitted
		for _, s := range c.submitted {
			if !s.finished && s.userSeq != 0 && row.seq > s.userSeq {
				current = s
			}
		}
		var delegation *string
		if current != nil {
			delegation = &current.id
			for _, s := range c.submitted {
				if s.userSeq != 0 && s.userSeq <= row.seq {
					s.finished = true
				}
			}
		}
		c.commentary("Backend reply: "+row.text, delegation)
	}
}

// commentary can be spoken; thinking keeps Live current without
// interrupting.
func (c *liveCall) commentary(content string, delegation *string) {
	c.appendContext("session.commentary.append", content, delegation)
}

func (c *liveCall) thinking(content string, delegation *string) {
	c.appendContext("session.thinking.append", content, delegation)
}

func (c *liveCall) appendContext(typ, content string, delegation *string) {
	c.eventSeq++
	c.emit(map[string]any{"type": "send", "event": struct {
		Type         string  `json:"type"`
		EventID      string  `json:"event_id"`
		DelegationID *string `json:"delegation_id"`
		Content      string  `json:"content"`
	}{typ, "shelley_" + strconv.Itoa(c.eventSeq), delegation, liveTruncate(content)}})
}

func (c *liveCall) error(message string) {
	c.emit(map[string]any{"type": "error", "message": message})
}

// activity reports whether a delegation is waiting for its transcript or
// to be handed to the backend, or is being handed over, when that changes.
func (c *liveCall) activity() {
	handing := len(c.pending) > 0 || len(c.jobs) > 0 || c.inflight > 0
	if c.active && handing != c.handing {
		c.handing = handing
		c.emit(map[string]any{"type": "activity", "handing_off": handing})
	}
}

func (c *liveCall) emit(instruction any) {
	data, err := json.Marshal(instruction)
	if err != nil {
		panic(err) // only fixed shapes of strings and bools
	}
	c.nextID++
	c.out = append(c.out, liveInstruction{c.nextID, data})
	if len(c.out) > liveInstructionsKept {
		c.out = slices.Clone(c.out[len(c.out)-liveInstructionsKept:])
	}
	close(c.wake)
	c.wake = make(chan struct{})
}

// instructions returns those after id, and channels that close on the
// next instruction and when the call ends.
func (c *liveCall) instructions(after int64) (out []liveInstruction, wake, done <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, in := range c.out {
		if in.id > after {
			out = append(out, in)
		}
	}
	return out, c.wake, c.done
}

// attach registers an instruction stream. Only the newest stream reads:
// superseded closes when another attaches. When the last one detaches,
// the call ends unless one attaches within liveIdle.
func (c *liveCall) attach() (superseded <-chan struct{}, detach func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streams++
	c.idle.Stop()
	close(c.attached)
	c.attached = make(chan struct{})
	return c.attached, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.streams--; c.streams == 0 && c.active {
			c.idle.Reset(liveIdle)
		}
	}
}

// end stops the call for good: timers are cancelled, queued deliveries
// dropped, and an in-flight delivery finishes without reporting back.
func (c *liveCall) end(reason string) {
	c.locked(func() { c.endLocked(reason) })
}

func (c *liveCall) endLocked(reason string) {
	if !c.active {
		return
	}
	c.logger.Info("Live call ended", "reason", reason)
	c.active = false
	c.idle.Stop()
	for _, p := range c.pending {
		if p.settle != nil {
			p.settle.Stop()
		}
		p.deadline.Stop()
	}
	clear(c.pending)
	c.order, c.jobs = nil, nil
	close(c.done)
}
