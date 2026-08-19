package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// performDistillation and its default-strategy helpers were removed when we
// consolidated on compaction (see performPiDistillation in distill_pi.go).

func (s *Server) insertDistillError(ctx context.Context, conversationID, errMsg string) {
	s.insertDistillStatus(ctx, conversationID, "error")

	// Insert an error message so the user knows what happened
	errorMessage := llm.Message{
		Role:      llm.MessageRoleAssistant,
		ErrorType: llm.ErrorTypeLLMRequest,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: errMsg},
		},
	}
	if err := s.recordMessage(ctx, conversationID, errorMessage, llm.Usage{}, nil); err != nil {
		s.logger.Error("Failed to record distill error message", "conversationID", conversationID, "error", err)
	}
}

// terminalDistillStatusMessage builds a NEW immutable status message carrying
// the terminal distill_status ("complete" or "error"). It copies the
// descriptive fields (source_slug, distill_method, new_generation) from the
// in_progress status message so the terminal message renders the same way.
// Messages are immutable after creation, so instead of mutating the in_progress
// message we emit a second message; the UI collapses the pair to just the
// terminal one. The message is ExcludedFromContext (a UI-only status marker).
// Returns false if no in_progress status message exists.
func (s *Server) terminalDistillStatusMessage(ctx context.Context, conversationID, status string) (llm.Message, map[string]string, bool) {
	messages, err := s.db.ListMessages(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to list messages", "conversationID", conversationID, "error", err)
		return llm.Message{}, nil, false
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.UserData == nil {
			continue
		}
		var userData map[string]string
		if err := json.Unmarshal([]byte(*msg.UserData), &userData); err != nil {
			continue
		}
		if userData["distill_status"] == "" {
			continue
		}
		terminalData := map[string]string{"distill_status": status}
		for _, k := range []string{"source_slug", "new_generation", "distill_method"} {
			if v := userData[k]; v != "" {
				terminalData[k] = v
			}
		}
		return llm.Message{
			Role:                llm.MessageRoleAssistant,
			Content:             []llm.Content{{Type: llm.ContentTypeText, Text: "Distillation " + status}},
			ExcludedFromContext: true,
		}, terminalData, true
	}
	return llm.Message{}, nil, false
}

// insertDistillStatus inserts a NEW immutable terminal status message
// ("complete" or "error") and broadcasts it to SSE subscribers via the normal
// new-message path, so it streams to clients and lands in cache with a fresh
// sequence_id. The earlier in_progress message is left untouched.
func (s *Server) insertDistillStatus(ctx context.Context, conversationID, status string) {
	message, userData, ok := s.terminalDistillStatusMessage(ctx, conversationID, status)
	if !ok {
		return
	}
	if err := s.recordMessage(ctx, conversationID, message, llm.Usage{}, nil, userData); err != nil {
		s.logger.Error("Failed to insert distill status", "conversationID", conversationID, "error", err)
	}
}

// truncateUTF8 truncates s to approximately maxBytes without splitting a UTF-8 character.
// If truncation occurs, "..." is appended.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return "..."
	}
	// Walk backward from maxBytes to find a valid rune boundary.
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes] + "..."
}

// distillMethodCompact is the conversation-shrinking strategy: it uses the
// compaction algorithm (modeled on the pi coding agent) to summarize older
// messages and keep recent ones verbatim. distillMethodCheckpoint is the same
// mechanism with the checkpoint summarizer (see compact_checkpoint.go),
// selected by FlagAutomaticCompaction; it is a label on the message rather than
// a separate code path, so the UI can say "checkpoint" and the summary can
// carry provenance. distillMethodDefault is the legacy "default" briefing
// strategy value, retained only so the endpoint keeps accepting it for
// compatibility (it is coerced to compaction).
const (
	distillMethodDefault    = "default"
	distillMethodCompact    = "compact"
	distillMethodCheckpoint = "checkpoint"
)

// compactionSummaryTimeout bounds a single summarization call. Past this the
// attempt fails and the conversation is left uncompacted, which is the right
// trade: compaction is an optimization, and a summarizer that has not produced
// a first byte in two minutes is not going to save the turn it was meant to fit
// inside.
const compactionSummaryTimeout = 120 * time.Second

// steeringSection formats optional user-provided guidance that steers what the
// distillation/summary should emphasize. Appended to the summarizer's input.
func steeringSection(instructions string) string {
	return "\n\n## User Guidance\n\nThe user provided the following guidance on what to preserve or emphasize in this distillation. Follow it closely:\n\n" + instructions
}

func (s *Server) runDistillNewGeneration(ctx context.Context, conversationID, sourceSlug, modelID, instructions string, checkpoint bool, sourceGeneration int64, messages []generated.Message) {
	defer func() {
		s.mu.Lock()
		manager, ok := s.activeConversations[conversationID]
		s.mu.Unlock()
		if ok {
			manager.SetDistilling(false)
			manager.drainPendingMessages(s)
		}
	}()

	s.performPiDistillation(ctx, conversationID, sourceSlug, modelID, instructions, checkpoint, sourceGeneration, messages)
	// The new generation's messages carry no usage data yet, so the UI's
	// context-usage bar would keep showing the pre-distillation size until the
	// next agent turn. Broadcast an estimate of the new generation's context
	// size so the bar resets immediately.
	s.broadcastEstimatedContextSize(ctx, conversationID)
	go s.notifySubscribers(ctx, conversationID)
}

// broadcastEstimatedContextSize estimates the latest generation's context
// window usage (char/4 heuristic over context-eligible messages) and pushes it
// to stream subscribers. Used right after distillation, when the new
// generation has no real usage data yet, so the UI bar resets instead of
// showing the stale pre-distillation value.
func (s *Server) broadcastEstimatedContextSize(ctx context.Context, conversationID string) {
	s.mu.Lock()
	manager, ok := s.activeConversations[conversationID]
	s.mu.Unlock()
	if !ok {
		return
	}

	messages, err := s.db.ListMessages(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to list messages for context estimate", "conversationID", conversationID, "error", err)
		return
	}

	// Estimate over the conversation's CURRENT generation, not the max
	// generation present in the table: a rolled-back compaction leaves
	// abandoned higher-generation rows behind, and estimating those would
	// show a near-empty context for an intact conversation.
	conv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to load conversation for context estimate", "conversationID", conversationID, "error", err)
		return
	}
	latestGen := conv.CurrentGeneration

	var estimate int64
	for i := range messages {
		m := messages[i]
		if m.Generation != latestGen || m.ExcludedFromContext {
			continue
		}
		llmMsg, cerr := convertToLLMMessage(m)
		if cerr != nil {
			continue
		}
		estimate += int64(estimatePiMessageTokens(llmMsg))
	}
	if estimate <= 0 {
		return
	}

	var conversation generated.Conversation
	if derr := s.db.Queries(ctx, func(q *generated.Queries) error {
		var qerr error
		conversation, qerr = q.GetConversation(ctx, conversationID)
		return qerr
	}); derr != nil {
		s.logger.Error("Failed to get conversation for context estimate", "conversationID", conversationID, "error", derr)
		return
	}

	manager.broadcastStream(StreamResponse{
		Conversation:      &conversation,
		ContextWindowSize: uint64(estimate),
	})
}

// DistillNewGenerationRequest represents the request to distill into the same conversation's next generation.
type DistillNewGenerationRequest struct {
	SourceConversationID string `json:"source_conversation_id"`
	Model                string `json:"model,omitempty"`
	Cwd                  string `json:"cwd,omitempty"`
	// Method selects the distillation strategy: "default" (single briefing
	// message) or "compact" (summarize-old + keep-recent-verbatim).
	// Empty defaults to "default".
	Method string `json:"method,omitempty"`
	// Instructions is optional free-form user guidance that steers what the
	// distillation should preserve or emphasize.
	Instructions string `json:"instructions,omitempty"`
}

// compactionError carries the HTTP status the distill endpoint should report
// for a setup failure. startCompaction is called both from that endpoint and
// from the automatic trigger, so it returns errors rather than writing
// responses; the trigger just logs them.
type compactionError struct {
	status int
	err    error
}

func (e *compactionError) Error() string { return e.err.Error() }
func (e *compactionError) Unwrap() error { return e.err }

func compactionErrorf(status int, format string, args ...any) *compactionError {
	return &compactionError{status: status, err: fmt.Errorf(format, args...)}
}

// compactionHTTPStatus returns the status to report for err, defaulting to 500.
func compactionHTTPStatus(err error) int {
	var ce *compactionError
	if errors.As(err, &ce) {
		return ce.status
	}
	return http.StatusInternalServerError
}

// startCompaction performs the compaction setup sequence and launches the
// summarization goroutine. It returns the conversation as of the new
// generation.
//
// This is shared by the /compact endpoint and the automatic trigger, and it
// must stay shared: every failure path below has to undo the generation bump
// (see rollbackCompactionFailure), and a second copy of that logic is how a
// conversation ends up stranded on an empty generation with its context — and
// any fork of it — wiped. The order is load-bearing too: validate the model
// before force-writing it onto the conversation, and take the distilling lock
// before any mutation, so a rejected concurrent attempt has no side effects.
//
// cwd and instructions may be empty. checkpoint selects the summarizer.
func (s *Server) startCompaction(ctx context.Context, conversationID, modelID, cwd, instructions string, checkpoint bool) (generated.Conversation, error) {
	sourceConv, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get source conversation", "conversationID", conversationID, "error", err)
		return generated.Conversation{}, compactionErrorf(http.StatusNotFound, "source conversation not found: %w", err)
	}
	// Capture the generation we are compacting FROM, before incrementing, to
	// select the right messages to summarize and copy.
	sourceGeneration := sourceConv.CurrentGeneration
	messages, err := s.db.ListMessages(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get messages", "conversationID", conversationID, "error", err)
		return generated.Conversation{}, compactionErrorf(http.StatusInternalServerError, "failed to get messages: %w", err)
	}

	if modelID == "" && sourceConv.Model != nil {
		modelID = *sourceConv.Model
	}
	if modelID == "" {
		modelID = s.effectiveDefaultModel(s.getModelList())
	}
	// Validate before mutating: the model is force-written onto the
	// conversation below, so an unknown model would otherwise brick the
	// conversation (every subsequent chat rejects the stored model).
	if _, err := s.llmManager.GetService(modelID); err != nil {
		return generated.Conversation{}, compactionErrorf(http.StatusBadRequest, "unknown model %q: %w", modelID, err)
	}

	manager, err := s.getOrCreateConversationManager(ctx, conversationID, "")
	if err != nil {
		s.logger.Error("Failed to create conversation manager for compaction", "conversationID", conversationID, "error", err)
		return generated.Conversation{}, compactionErrorf(http.StatusInternalServerError, "failed to create conversation manager: %w", err)
	}
	// Acquire the distilling state before any mutation so a rejected
	// concurrent request has no side effects.
	if !manager.BeginDistillingSetup() {
		return generated.Conversation{}, compactionErrorf(http.StatusConflict, "compaction already in progress")
	}
	setupComplete := false
	defer func() {
		if !setupComplete {
			manager.SetDistilling(false)
		}
	}()

	if cwd != "" && (sourceConv.Cwd == nil || *sourceConv.Cwd != cwd) {
		if err := s.db.UpdateConversationCwd(ctx, conversationID, cwd); err != nil {
			s.logger.Error("Failed to update cwd for new generation", "error", err)
			return generated.Conversation{}, compactionErrorf(http.StatusInternalServerError, "failed to update cwd: %w", err)
		}
	}
	if sourceConv.Model == nil || *sourceConv.Model != modelID {
		if err := s.db.ForceUpdateConversationModel(ctx, conversationID, modelID); err != nil {
			s.logger.Error("Failed to update model for new generation", "error", err)
			return generated.Conversation{}, compactionErrorf(http.StatusInternalServerError, "failed to update model: %w", err)
		}
	}

	conversation, err := db.WithTxRes(s.db, ctx, func(q *generated.Queries) (generated.Conversation, error) {
		return q.IncrementConversationGeneration(ctx, conversationID)
	})
	if err != nil {
		s.logger.Error("Failed to increment generation", "conversationID", conversationID, "error", err)
		return generated.Conversation{}, compactionErrorf(http.StatusInternalServerError, "failed to increment generation: %w", err)
	}
	manager.ResetLoop()

	method := distillMethodCompact
	statusText := "Compacting conversation…"
	if checkpoint {
		method = distillMethodCheckpoint
		statusText = "Checkpointing conversation…"
	}
	sourceSlug := "unknown"
	if sourceConv.Slug != nil {
		sourceSlug = *sourceConv.Slug
	}
	statusMsg, err := s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           db.MessageTypeAgent,
		LLMData: llm.Message{
			Role:    llm.MessageRoleAssistant,
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: statusText}},
		},
		UserData: map[string]string{
			"distill_status": "in_progress",
			"source_slug":    sourceSlug,
			"new_generation": "true",
			"distill_method": method,
		},
		ExcludedFromContext: true,
	})
	if err != nil {
		s.logger.Error("Failed to create status message", "conversationID", conversationID, "error", err)
		// WithoutCancel: a client disconnect mid-setup must not strand the
		// conversation on the just-created empty generation.
		s.rollbackCompactionFailure(context.WithoutCancel(ctx), s.logger, conversationID, "Compaction failed during setup", sourceGeneration)
		return generated.Conversation{}, compactionErrorf(http.StatusInternalServerError, "failed to create status message: %w", err)
	}
	go s.notifySubscribersNewMessage(context.WithoutCancel(ctx), conversationID, statusMsg)

	if err := manager.Hydrate(ctx); err != nil {
		s.logger.Error("Failed to hydrate new generation", "conversationID", conversationID, "error", err)
		// WithoutCancel: a client disconnect mid-setup must not strand the
		// conversation on the just-created empty generation.
		s.rollbackCompactionFailure(context.WithoutCancel(ctx), s.logger, conversationID, "Compaction failed during setup", sourceGeneration)
		return generated.Conversation{}, compactionErrorf(http.StatusInternalServerError, "failed to hydrate new generation: %w", err)
	}
	if fresh, ferr := s.db.GetConversationByID(ctx, conversationID); ferr == nil {
		conversation = *fresh
	}
	if currentMessages, merr := s.db.ListMessages(ctx, conversationID); merr == nil {
		for i := range currentMessages {
			msg := &currentMessages[i]
			if msg.Generation == conversation.CurrentGeneration && msg.Type == string(db.MessageTypeSystem) && msg.UserData == nil {
				go s.notifySubscribersNewMessage(context.WithoutCancel(ctx), conversationID, msg)
			}
		}
	}
	go s.notifySubscribers(context.WithoutCancel(ctx), conversationID)
	setupComplete = true
	manager.FinishDistillingSetup()

	ctxNoCancel := context.WithoutCancel(ctx)
	go func() {
		s.runDistillNewGeneration(ctxNoCancel, conversationID, sourceSlug, modelID, instructions, checkpoint, sourceGeneration, messages)
	}()
	return conversation, nil
}

// handleDistillNewGeneration handles POST /api/conversations/distill-new-generation.
// It keeps the visible conversation, marks old messages as previous generation,
// and inserts the distillation into the next generation.
func (s *Server) handleDistillNewGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	var req DistillNewGenerationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.SourceConversationID == "" {
		http.Error(w, "source_conversation_id is required", http.StatusBadRequest)
		return
	}

	// We have consolidated on compaction as the single strategy. The legacy
	// "default" distillation method is retained only for request
	// compatibility: any method value (including empty or "default") is
	// coerced to compaction. Reject only clearly bogus method strings.
	if req.Method != "" && req.Method != distillMethodDefault && req.Method != distillMethodCompact {
		http.Error(w, fmt.Sprintf("unknown distill method %q", req.Method), http.StatusBadRequest)
		return
	}
	// The flag decides the summarizer, and is read per-compaction rather than
	// cached, so toggling it takes effect on the next /compact with no restart.
	checkpoint := s.featureFlagEnabled(ctx, FlagAutomaticCompaction)

	conversation, err := s.startCompaction(ctx, req.SourceConversationID, req.Model, req.Cwd, req.Instructions, checkpoint)
	if err != nil {
		http.Error(w, err.Error(), compactionHTTPStatus(err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":             "created",
		"conversation_id":    req.SourceConversationID,
		"current_generation": conversation.CurrentGeneration,
	})
}
