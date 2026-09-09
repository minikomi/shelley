package server

import (
	"context"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

type thinkingDropService struct{ llm.Service }

func (s thinkingDropService) Do(context.Context, *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Model: "claude-opus-5", Role: llm.MessageRoleAssistant,
		StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("answer"),
		InputTransformations: []llm.InputTransformation{{
			Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "model_binding_mismatch",
		}},
	}, nil
}

func TestThinkingDropDoesNotPersistWarningsAcrossLoopReset(t *testing.T) {
	server, database, predictable := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cwd := t.TempDir()
	conversation, err := database.CreateConversation(ctx, nil, true, &cwd, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := server.getOrCreateConversationManager(ctx, conversation.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopLoop()
	done := make(chan struct{}, 1)
	record := manager.recordMessage
	manager.recordMessage = func(ctx context.Context, message llm.Message, usage llm.Usage, other []llm.PurposedUsage) error {
		if err := record(ctx, message, usage, other); err != nil {
			return err
		}
		if message.EndOfTurn {
			done <- struct{}{}
		}
		return nil
	}
	service := thinkingDropService{predictable}
	for turn := range 3 {
		if turn == 2 {
			manager.ResetLoop()
		}
		manager.mu.Lock()
		previous := manager.loop
		manager.mu.Unlock()
		if _, err := manager.AcceptUserMessage(ctx, service, "predictable", llm.UserStringMessage("question")); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("turn did not complete:", ctx.Err())
		}
		manager.mu.Lock()
		current := manager.loop
		manager.mu.Unlock()
		if current == nil || (turn == 1 && current != previous) || (turn == 2 && previous != nil) {
			t.Fatalf("unexpected loop lifecycle on turn %d", turn)
		}
		messages, err := database.ListMessages(ctx, conversation.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range messages {
			if message.Type == string(db.MessageTypeWarning) {
				t.Fatalf("turn %d: unexpected persisted drop banner", turn)
			}
		}
	}
}
