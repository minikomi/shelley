package server

import (
	"context"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// TestStreamFlusherAssignsMonotonicSeq verifies that each partial update the
// streamFlusher broadcasts carries a monotonically increasing per-conversation
// sequence number while adjacent deltas of the same type are coalesced.
func TestStreamFlusherAssignsMonotonicSeq(t *testing.T) {
	t.Parallel()
	server, database, _ := newTestServer(t)

	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	manager, err := server.getOrCreateConversationManager(context.Background(), conversation.ConversationID, "")
	if err != nil {
		t.Fatalf("failed to get conversation manager: %v", err)
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	next := manager.subpub.Subscribe(subCtx, -1)

	deltas := make(chan llm.StreamDelta, 16)
	go func() {
		for {
			data, ok := next()
			if !ok {
				return
			}
			if data.StreamDelta != nil {
				deltas <- *data.StreamDelta
			}
		}
	}()

	// Use a long interval so the periodic timer never fires on its own; only
	// the explicit Flush below emits the batched text delta. This keeps the
	// expected sequence deterministic.
	sf := newStreamFlusher(manager, time.Hour)

	sf.Push(llm.StreamDelta{Type: "thinking", Text: "hmm", Index: 0})
	sf.Push(llm.StreamDelta{Type: "text", Text: "hello ", Index: 1})
	sf.Push(llm.StreamDelta{Type: "text", Text: "world", Index: 1})
	sf.Flush()
	sf.Push(llm.StreamDelta{Type: "thinking", Text: "done", Index: 0})
	sf.Flush()

	var got []llm.StreamDelta
	for len(got) < 3 {
		select {
		case delta := <-deltas:
			got = append(got, delta)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for stream deltas; got %v", got)
		}
	}

	want := []llm.StreamDelta{
		{Type: "thinking", Text: "hmm", Index: 0, Seq: 1},
		{Type: "text", Text: "hello world", Index: 1, Seq: 2},
		{Type: "thinking", Text: "done", Index: 0, Seq: 3},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delta[%d] = %+v, want %+v (all: %v)", i, got[i], want[i], got)
		}
	}
}
