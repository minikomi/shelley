package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

type midTurnWorkerService struct {
	llm.Service
	do func(context.Context, *llm.Request) (*llm.Response, error)
}

func (s midTurnWorkerService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return s.do(ctx, req)
}

func midTurnCompactionFixture(t *testing.T) (*compactTestConversation, *ConversationManager, uint64, claudetool.CompactInPlaceInput) {
	t.Helper()
	c := newCompactTestConversation(t, db.ConversationOptions{})
	c.turn("hello")
	c.turn("echo: current task")
	cm, err := c.srv.getOrCreateConversationManager(t.Context(), c.id, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cm.ResetLoop)
	cm.mu.Lock()
	generation := cm.loopGeneration
	cm.mu.Unlock()
	id := claudetool.IndexID(fmt.Sprint(c.seqWith("Well, hi there!")))
	in := claudetool.CompactInPlaceInput{
		Action: "compact", Brief: "GREETED",
		Collapse: []claudetool.CompactCollapse{{From: id, To: id}},
	}
	return c, cm, generation, in
}

func TestCompactWorkersPreflight(t *testing.T) {
	for _, kind := range []string{"invalid range", "missing brief", "long brief", "skip with collapse"} {
		t.Run(kind, func(t *testing.T) {
			c, cm, generation, in := midTurnCompactionFixture(t)
			switch kind {
			case "invalid range":
				in.Collapse[0].From = "missing"
			case "missing brief":
				in.Brief = ""
			case "long brief":
				in.Brief = strings.Repeat("word ", 151)
			case "skip with collapse":
				in.Action = "skip"
			}
			service := midTurnWorkerService{Service: c.ps, do: func(context.Context, *llm.Request) (*llm.Response, error) {
				t.Error("invalid plan incurred an LLM call")
				return nil, errors.New("unexpected call")
			}}
			_, err := cm.compactMidTurn(llm.WithLLMService(t.Context(), service), generation, in)
			if err == nil || c.records() != 0 {
				t.Fatalf("invalid plan: error=%v, records=%d", err, c.records())
			}
		})
	}
}

func TestCompactWorkersFailureDoesNotApply(t *testing.T) {
	for _, kind := range []string{"request error", "bad JSON", "cancel", "reset loop"} {
		t.Run(kind, func(t *testing.T) {
			c, cm, generation, in := midTurnCompactionFixture(t)
			started, release := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			service := midTurnWorkerService{Service: c.ps, do: func(ctx context.Context, _ *llm.Request) (*llm.Response, error) {
				close(started)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
				}
				if kind == "request error" {
					return nil, errors.New("worker unavailable")
				}
				if kind == "bad JSON" {
					return &llm.Response{Content: llm.TextContent("bad JSON")}, nil
				}
				return &llm.Response{Content: llm.TextContent(`{"summaries":[{"id":1,"note":"GREETED"}]}`)}, nil
			}}
			done := make(chan error, 1)
			go func() {
				_, err := cm.compactMidTurn(llm.WithLLMService(ctx, service), generation, in)
				done <- err
			}()
			<-started
			if !cm.loopLifecycleMu.TryLock() {
				cancel()
				t.Fatal("lifecycle lock held during worker request")
			}
			cm.loopLifecycleMu.Unlock()
			switch kind {
			case "cancel":
				cancel()
			case "reset loop":
				cm.ResetLoop()
			}
			close(release)
			if err := <-done; err == nil || c.records() != 0 {
				t.Fatalf("failed worker: error=%v, records=%d", err, c.records())
			}
			items, _, err := cm.loadContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var history []llm.Message
			for _, item := range items {
				history = append(history, item.message)
			}
			if !strings.Contains(requestDump(&llm.Request{Messages: history}), "Well, hi there!") {
				t.Fatal("failed compaction changed source history")
			}
		})
	}
}

func TestCompactWorkersInheritTurnEffort(t *testing.T) {
	c := newCompactTestConversation(t, db.ConversationOptions{ThinkingLevel: "high"})
	c.turn("hello")
	c.turn("echo: current task")
	id := fmt.Sprint(c.seqWith("Well, hi there!"))
	c.turn(`compact_in_place: {"action":"compact","brief":"GREETED","collapse":[{"from":"` + id + `","to":"` + id + `"}]}`)
	if c.records() != 1 {
		out, _ := c.lastToolOutput()
		t.Fatalf("compaction failed: %s", out)
	}
	workers := 0
	for _, request := range c.ps.GetRecentRequests() {
		if len(request.System) != 1 || request.System[0].Text != compactionWorkerPrompt {
			continue
		}
		workers++
		if request.ThinkingLevel != llm.ThinkingLevelHigh || len(request.Tools) != 0 {
			t.Fatalf("worker effort=%v, tools=%d", request.ThinkingLevel, len(request.Tools))
		}
	}
	if workers != 1 {
		t.Fatalf("worker requests=%d, want 1", workers)
	}
}
