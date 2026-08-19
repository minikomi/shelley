package server

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// enableAutomaticCompaction turns the flag on for one test's database.
func enableAutomaticCompaction(t *testing.T, database *db.DB) {
	t.Helper()
	if err := database.SetFeatureFlagOverride(context.Background(), FlagAutomaticCompaction.Name, "true"); err != nil {
		t.Fatalf("failed to enable %s: %v", FlagAutomaticCompaction.Name, err)
	}
}

// turnEndMsg builds an end-of-turn agent message reporting usedTokens of
// context, which is what the trigger reads to decide.
func turnEndMsg(t *testing.T, database *db.DB, conversationID string, usedTokens uint64) *generated.Message {
	t.Helper()
	msg, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeAgent,
		LLMData:        llm.Message{Role: llm.MessageRoleAssistant, EndOfTurn: true, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "done"}}},
		UsageData:      llm.Usage{InputTokens: usedTokens, OutputTokens: 1},
	})
	if err != nil {
		t.Fatalf("failed to create turn-end message: %v", err)
	}
	return msg
}

// generationOf reports the conversation's current generation. A compaction
// increments it, so it is the observable signal that one ran.
func generationOf(t *testing.T, database *db.DB, conversationID string) int64 {
	t.Helper()
	conv, err := database.GetConversationByID(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("failed to load conversation: %v", err)
	}
	return conv.CurrentGeneration
}

// newTriggerTestConversation returns a server whose manager is active, with a
// small context window so a modest token count crosses the threshold.
func newTriggerTestConversation(t *testing.T, window int) (*Server, *db.DB, string) {
	t.Helper()
	server, database, ps := newTestServer(t)
	server.logger = testInfoLogger()
	ps.SetTokenContextWindow(window)
	model := "predictable"
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, &model, db.ConversationOptions{})
	if err != nil {
		t.Fatalf("failed to create conversation: %v", err)
	}
	conversationID := conversation.ConversationID
	// An active manager is required: the trigger reads the model and the
	// pending-work state from it.
	if _, err := server.getOrCreateConversationManager(context.Background(), conversationID, ""); err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	return server, database, conversationID
}

// TestTriggerDoesNothingWhenFlagOff is the premise of the whole feature: with
// the flag off, nothing about today's behavior changes.
func TestTriggerDoesNothingWhenFlagOff(t *testing.T) {
	t.Parallel()
	server, database, conversationID := newTriggerTestConversation(t, 1000)
	before := generationOf(t, database, conversationID)

	// Far over any threshold — only the flag is stopping this.
	server.maybeScheduleCompaction(context.Background(), conversationID, turnEndMsg(t, database, conversationID, 100000))

	if got := generationOf(t, database, conversationID); got != before {
		t.Errorf("compaction ran with the flag off: generation %d -> %d", before, got)
	}
}

func TestTriggerDoesNothingUnderThreshold(t *testing.T) {
	t.Parallel()
	server, database, conversationID := newTriggerTestConversation(t, 1000)
	enableAutomaticCompaction(t, database)
	before := generationOf(t, database, conversationID)

	// 500 of 1000 is under the 0.7 threshold.
	server.maybeScheduleCompaction(context.Background(), conversationID, turnEndMsg(t, database, conversationID, 500))

	if got := generationOf(t, database, conversationID); got != before {
		t.Errorf("compaction ran under threshold: generation %d -> %d", before, got)
	}
}

// TestTriggerStartsOverThreshold is the happy path: over threshold and idle,
// the trigger hands the conversation to the shared compaction starter. The
// starter is a channel send in this test, so this proves scheduling without a
// polling sleep or a real LLM call; startCompaction itself is exercised by the
// normal distillation endpoint tests.
func TestTriggerStartsOverThreshold(t *testing.T) {
	t.Parallel()
	server, database, conversationID := newTriggerTestConversation(t, 1000)
	enableAutomaticCompaction(t, database)
	started := make(chan struct {
		conversationID string
		modelID        string
	}, 1)
	server.automaticCompactionStarter = func(_ context.Context, gotConversationID, gotModelID string) {
		started <- struct {
			conversationID string
			modelID        string
		}{gotConversationID, gotModelID}
	}

	// 900 of 1000 is over the 0.7 threshold.
	server.maybeScheduleCompaction(context.Background(), conversationID, turnEndMsg(t, database, conversationID, 900))
	got := <-started
	if got.conversationID != conversationID || got.modelID != "predictable" {
		t.Errorf("starter = (%q, %q), want (%q, %q)", got.conversationID, got.modelID, conversationID, "predictable")
	}
}

// TestTriggerSkipsWhenWorkIsQueued: a turn ending is not the conversation going
// idle. Compacting with work queued would summarize a conversation about to keep
// growing, and stall that work behind a summarization call.
func TestTriggerSkipsWhenWorkIsQueued(t *testing.T) {
	t.Parallel()
	server, database, conversationID := newTriggerTestConversation(t, 1000)
	enableAutomaticCompaction(t, database)

	manager, err := server.getOrCreateConversationManager(context.Background(), conversationID, "")
	if err != nil {
		t.Fatalf("failed to get manager: %v", err)
	}
	// Queue a batch so HasPendingWork reports true.
	manager.mu.Lock()
	manager.pendingBatches = append(manager.pendingBatches, pendingBatch{})
	manager.mu.Unlock()
	if !manager.HasPendingWork() {
		t.Fatal("HasPendingWork should report queued work")
	}
	before := generationOf(t, database, conversationID)

	server.maybeScheduleCompaction(context.Background(), conversationID, turnEndMsg(t, database, conversationID, 900))

	if got := generationOf(t, database, conversationID); got != before {
		t.Errorf("compaction ran with work queued: generation %d -> %d", before, got)
	}
}

// TestTriggerSkipsWhileDistilling: a manual /compact already holds this state,
// and a second writer must not race it.
func TestTriggerSkipsWhileDistilling(t *testing.T) {
	t.Parallel()
	server, database, conversationID := newTriggerTestConversation(t, 1000)
	enableAutomaticCompaction(t, database)

	manager, err := server.getOrCreateConversationManager(context.Background(), conversationID, "")
	if err != nil {
		t.Fatalf("failed to get manager: %v", err)
	}
	if !manager.BeginDistillingSetup() {
		t.Fatal("failed to acquire distilling state")
	}
	before := generationOf(t, database, conversationID)

	server.maybeScheduleCompaction(context.Background(), conversationID, turnEndMsg(t, database, conversationID, 900))

	if got := generationOf(t, database, conversationID); got != before {
		t.Errorf("compaction ran while already distilling: generation %d -> %d", before, got)
	}
}

// TestTriggerIgnoresMessagesWithoutUsage: user and tool messages report no
// usage, and 0 must not read as "under threshold, all is well" on a message
// that simply never carried the number.
func TestTriggerIgnoresMessagesWithoutUsage(t *testing.T) {
	t.Parallel()
	server, database, conversationID := newTriggerTestConversation(t, 1000)
	enableAutomaticCompaction(t, database)
	before := generationOf(t, database, conversationID)

	msg, err := database.CreateMessage(context.Background(), db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeUser,
		LLMData:        llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}}},
	})
	if err != nil {
		t.Fatalf("failed to create message: %v", err)
	}
	server.maybeScheduleCompaction(context.Background(), conversationID, msg)

	if got := generationOf(t, database, conversationID); got != before {
		t.Errorf("compaction ran off a message with no usage data: generation %d -> %d", before, got)
	}
}

// testInfoLogger logs at Info so the trigger's scheduling decisions are visible
// when a trigger test fails.
func testInfoLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
