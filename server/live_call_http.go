package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// livePing keeps proxies from closing an idle instruction stream.
const livePing = 25 * time.Second

// liveCalls are the calls in progress, by Live session id.
type liveCalls struct {
	mu    sync.Mutex
	calls map[string]*liveCall
}

func (l *liveCalls) add(c *liveCall) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls == nil {
		l.calls = map[string]*liveCall{}
	}
	if old := l.calls[c.id]; old != nil {
		go old.end("replaced")
	}
	l.calls[c.id] = c
	go func() {
		<-c.done
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.calls[c.id] == c {
			delete(l.calls, c.id)
		}
	}()
}

func (l *liveCalls) get(conversationID, sessionID string) *liveCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	if c := l.calls[sessionID]; c != nil && c.conversationID == conversationID {
		return c
	}
	return nil
}

// startLiveCall begins delegation for a Live session that holds the
// conversation up to initialSeq. Its tasks go through chat as r's user,
// with a draft's settings until the draft is promoted.
func (s *Server) startLiveCall(r *http.Request, conversationID, sessionID string, initialSeq int64, settings liveSettings) {
	header := r.Header.Clone()
	deliver := func(ctx context.Context, heading, transcript, voiceContext string) (liveReceipt, error) {
		conversation, err := s.db.GetConversationByID(ctx, conversationID)
		if err != nil {
			return liveReceipt{}, fmt.Errorf("Failed to deliver speech to Shelley: %w", err)
		}
		if !conversation.IsDraft {
			settings = liveSettings{}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/conversation/"+conversationID+"/chat", nil)
		if err != nil {
			return liveReceipt{}, err
		}
		req.Header = header.Clone()
		recorder := s.liveChat(req, conversationID, heading, transcript, voiceContext, settings)
		var receipt liveReceipt
		if recorder.Code != http.StatusAccepted || json.Unmarshal(recorder.Body.Bytes(), &receipt) != nil {
			detail := strings.TrimSpace(recorder.Body.String())
			if detail == "" {
				detail = http.StatusText(recorder.Code)
			}
			return liveReceipt{}, errors.New("Failed to deliver speech to Shelley: " + detail)
		}
		return receipt, nil
	}
	c := newLiveCall(sessionID, conversationID, initialSeq, deliver, s.logger)
	c.archive = func(ctx context.Context, data liveTranscriptData) error {
		_, err := s.insertMessages(ctx, conversationID, []db.CreateMessageParams{{
			ConversationID: conversationID, Type: db.MessageTypeLiveTranscript,
			UserData: data, ExcludedFromContext: true,
		}})
		return err
	}
	s.liveCalls.add(c)
	go s.watchLiveCall(c, initialSeq)
}

// watchLiveCall feeds the call the conversation's rows after seq as they
// are recorded, until the call ends.
func (s *Server) watchLiveCall(c *liveCall, seq int64) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-c.done
		cancel()
	}()
	catchUp := func() bool {
		var rows []generated.Message
		err := s.db.Queries(ctx, func(q *generated.Queries) error {
			var err error
			rows, err = q.ListMessagesSince(ctx, generated.ListMessagesSinceParams{ConversationID: c.conversationID, SequenceID: seq})
			return err
		})
		if err != nil {
			if ctx.Err() == nil {
				c.logger.Error("Live call cannot read the conversation", "error", err)
			}
			return false
		}
		var heard []liveRow
		for _, row := range rows {
			seq = max(seq, row.SequenceID)
			user, text, full, err := liveDialogue(row)
			if err != nil {
				c.logger.Warn("Live call skipped a message", "error", err)
			}
			heard = append(heard, liveRow{seq: row.SequenceID, user: user, text: text, full: full})
		}
		c.observe(heard)
		return true
	}
	for ctx.Err() == nil {
		// Subscribe before reading, so that no row falls in between; a
		// subscriber that falls behind starts over.
		next := s.streamPub.Subscribe(ctx, 0)
		if !catchUp() {
			return
		}
		for {
			event, ok := next()
			if !ok {
				break
			}
			if event.ConversationID == c.conversationID && len(event.Messages) > 0 && !catchUp() {
				return
			}
		}
	}
}

// handleLiveStream streams a call's instructions to its client as SSE.
func (s *Server) handleLiveStream(w http.ResponseWriter, r *http.Request) {
	c := s.liveCalls.get(r.PathValue("id"), r.PathValue("session"))
	if c == nil {
		http.Error(w, "Live call not found", http.StatusNotFound)
		return
	}
	resume := r.URL.Query().Get("after")
	if resume == "" {
		resume = r.Header.Get("Last-Event-ID")
	}
	var after int64
	if resume != "" {
		var err error
		if after, err = strconv.ParseInt(resume, 10, 64); err != nil {
			http.Error(w, "invalid after", http.StatusBadRequest)
			return
		}
	}
	superseded, detach := c.attach()
	defer detach()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	ping := time.NewTicker(livePing)
	defer ping.Stop()
	write := func() (wake, done <-chan struct{}, ok bool) {
		out, wake, done := c.instructions(after)
		for _, in := range out {
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", in.id, in.data); err != nil {
				return nil, nil, false
			}
			after = in.id
		}
		return wake, done, rc.Flush() == nil
	}
	for {
		wake, done, ok := write()
		if !ok {
			return
		}
		select {
		case <-wake:
		case <-done:
			write() // what ended the call
			return
		case <-superseded:
			return
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
		}
	}
}

// handleLiveEvents takes Live's data-channel events for a call.
func (s *Server) handleLiveEvents(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLiveRequestBytes)).Decode(&body); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "Live events too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Invalid live events", http.StatusBadRequest)
		return
	}
	events := make([]liveEvent, len(body.Events))
	for i, raw := range body.Events {
		var ok bool
		if events[i], ok = parseLiveEvent(raw); !ok {
			http.Error(w, "Invalid live events", http.StatusBadRequest)
			return
		}
	}
	if len(events) == 0 {
		http.Error(w, "Invalid live events", http.StatusBadRequest)
		return
	}
	c := s.liveCalls.get(r.PathValue("id"), r.PathValue("session"))
	if c == nil {
		http.Error(w, "Live call not found", http.StatusNotFound)
		return
	}
	c.handle(events)
	w.WriteHeader(http.StatusNoContent)
}

// handleLiveHangUp ends a call; it is idempotent.
func (s *Server) handleLiveHangUp(w http.ResponseWriter, r *http.Request) {
	if c := s.liveCalls.get(r.PathValue("id"), r.PathValue("session")); c != nil {
		c.end("hung up")
	}
	w.WriteHeader(http.StatusNoContent)
}
