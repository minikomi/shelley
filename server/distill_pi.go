package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
)

// This file implements a second distillation strategy modeled on the
// open-source pi coding agent's compaction algorithm
// (github.com/badlogic/pi-mono, packages/agent/src/harness/compaction).
//
// Unlike the default Shelley distillation — which collapses the entire
// conversation into a single hand-written-style "briefing" message — the pi
// strategy splits the conversation at a cut point: older messages are
// summarized with a structured checkpoint prompt, while recent messages
// (≈ keepRecentTokens worth) are copied VERBATIM into the new generation so
// the agent retains exact recent tool calls, results, and edits. The summary
// is inserted as the first context message, wrapped so the LLM understands it
// replaces compacted history.

// piDistillSettings mirrors pi's CompactionSettings defaults.
type piDistillSettings struct {
	// reserveTokens caps the summary output budget (0.8 * reserveTokens).
	reserveTokens int
	// keepRecentTokens is the approximate recent-context budget kept verbatim.
	keepRecentTokens int
}

var defaultPiDistillSettings = piDistillSettings{
	reserveTokens:    16384,
	keepRecentTokens: 20000,
}

// piSummarizationSystemPrompt and the prompt bodies are ported verbatim from
// pi's compaction.ts so behavior matches the upstream implementation.
const piSummarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const piSummarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// checkpointSummarizationPrompt is the topic-based checkpoint variant of the
// summarization prompt, derived from AUTOMATIC_COMPACTION_PROPOSAL.md and its
// prompt experiments. Unlike the pi task report, it organizes by durable topic
// and preserves decisions, rationale, and user preferences. Selected by the
// checkpoint-compaction feature flag.
const checkpointSummarizationPrompt = `The messages above are a conversation to summarize. Create a topic-based context checkpoint that another LLM will use to continue the work.

Organize the checkpoint by durable topic, not as a chronological task report. For each topic use this EXACT format:

## <Topic> — <done|active|blocked>
- **Context:** Durable current state.
- **Decisions:** Choices already made.
- **Rationale:** Why those choices matter.
- **Anchors:** Exact file paths, symbols, branches, commands, errors, commits, or artifacts.
- **Continuation:** Remaining work, only when applicable.

Each transcript line is prefixed with its message sequence number, like "[seq:42]". Cite that exact marker inline, attached to the claim it supports — a decision, an error, a user instruction — so a reader can retrieve the original text later. Use ONLY sequence numbers that actually appear in the transcript; never invent or guess one.

Requirements:
- Preserve user requirements, preferences, decisions, and rationale.
- Preserve exact file paths, function names, commands, and error messages.
- Describe current state; omit routine verification history unless it changes what happens next.
- Omit bullet lines that do not apply to a topic, but every topic needs **Context:**.
- Do not invent file lists or references.`

// validateCheckpointSummary rejects malformed checkpoint output (a refusal,
// prose without structure, ...) so a bad summary never becomes the new
// generation's opening context. Requires at least one topic heading and one
// Context line, per the checkpoint format.
func validateCheckpointSummary(summary string) error {
	if !strings.Contains(summary, "## ") {
		return fmt.Errorf("checkpoint summary has no topic headings")
	}
	if !strings.Contains(summary, "**Context:**") {
		return fmt.Errorf("checkpoint summary has no Context section")
	}
	return nil
}

// piCompactionSummaryPrefix/Suffix wrap the summary when it is presented to the
// LLM as the opening context message, matching pi's COMPACTION_SUMMARY_PREFIX.
const piCompactionSummaryPrefix = `The conversation history before this point was compacted into the following summary:

<summary>
`

const piCompactionSummarySuffix = `
</summary>`

// checkpointCompactionSummarySuffix follows the checkpoint summary. It teaches
// the reading model to resolve [seq:N] pointers with sqlite+bash: shelley
// exports SHELLEY_DB and SHELLEY_CONVERSATION_ID to every bash command, and
// the summarized messages remain in the messages table (older generation), so
// exact original text is one query away. This is the retrieval half of
// checkpointing without needing dedicated tools.
const checkpointCompactionSummarySuffix = `
</summary>

Older messages are not lost. Each [seq:N] or [seq:N-M] pointer above cites an original message by sequence number; to read the exact original text, query shelley's database from bash:

sqlite3 "$SHELLEY_DB" "SELECT sequence_id, type, json_extract(llm_data,'$.Content[0].Text') FROM messages WHERE conversation_id='$SHELLEY_CONVERSATION_ID' AND sequence_id BETWEEN N AND M ORDER BY sequence_id;"

Tool calls/results store their payload deeper in the llm_data JSON; select the whole llm_data column for those rows. To search older history instead of following a pointer, filter with AND llm_data LIKE '%term%' in place of the sequence range.`

// historyPointerPattern matches [seq:N] and [seq:N-M] pointers in a checkpoint
// summary.
var historyPointerPattern = regexp.MustCompile(`\[seq:(\d+)(?:-(\d+))?\]`)

// sanitizeCheckpointPointers strips any [seq:N] / [seq:N-M] pointer that does
// not name a real message in this conversation. A pointer is the reader's only
// route back to original text, so a fabricated one is worse than no citation
// at all: they follow it and land somewhere unrelated, with nothing to signal
// the citation was invented. Validity is existence in the conversation, not
// membership in the summarized span.
func sanitizeCheckpointPointers(summary string, valid map[int64]bool) string {
	return historyPointerPattern.ReplaceAllStringFunc(summary, func(match string) string {
		sub := historyPointerPattern.FindStringSubmatch(match)
		a, err := strconv.ParseInt(sub[1], 10, 64)
		if err != nil || !valid[a] {
			return "[unverifiable pointer removed]"
		}
		if sub[2] != "" {
			b, err := strconv.ParseInt(sub[2], 10, 64)
			if err != nil || !valid[b] || b < a {
				return "[unverifiable pointer removed]"
			}
		}
		return match
	})
}

// estimatePiMessageTokens ports pi's character/4 heuristic for one message.
func estimatePiMessageTokens(msg llm.Message) int {
	chars := 0
	for _, c := range msg.Content {
		switch c.Type {
		case llm.ContentTypeText:
			chars += len(c.Text)
		case llm.ContentTypeThinking, llm.ContentTypeRedactedThinking:
			chars += len(c.Thinking)
		case llm.ContentTypeToolUse:
			chars += len(c.ToolName) + len(c.ToolInput)
		case llm.ContentTypeToolResult:
			for _, r := range c.ToolResult {
				chars += len(r.Text)
			}
		}
	}
	// ceil(chars / 4)
	return (chars + 3) / 4
}

// isToolResultMessage reports whether a message carries only tool_result
// content. Such messages are never valid cut points: they must stay paired
// with the assistant tool_use that produced them.
func isToolResultMessage(msg llm.Message) bool {
	hasToolResult := false
	for _, c := range msg.Content {
		if c.Type == llm.ContentTypeToolResult {
			hasToolResult = true
		} else if c.Type != llm.ContentTypeText {
			// Other content alongside tool_result is unusual; treat presence
			// of a non-tool-result, non-text block as making this not a pure
			// tool-result message.
			return false
		}
	}
	return hasToolResult
}

// findPiCutPoint ports pi's findCutPoint to a flat message list. It returns the
// index of the first message to KEEP verbatim. Messages [0, cut) are
// summarized; [cut, len) are kept. The cut never lands on a tool_result
// message, so kept history never starts with an orphaned tool result.
func findPiCutPoint(messages []llm.Message, keepRecentTokens int) int {
	// Collect valid cut points (non-tool-result messages).
	var cutPoints []int
	for i, m := range messages {
		if !isToolResultMessage(m) {
			cutPoints = append(cutPoints, i)
		}
	}
	if len(cutPoints) == 0 {
		// No valid cut point: keep everything, summarize nothing.
		return 0
	}

	cutIndex := cutPoints[0]
	accumulated := 0
	for i := len(messages) - 1; i >= 0; i-- {
		accumulated += estimatePiMessageTokens(messages[i])
		if accumulated >= keepRecentTokens {
			// Pick the first valid cut point at or after i.
			for _, c := range cutPoints {
				if c >= i {
					cutIndex = c
					break
				}
			}
			break
		}
	}
	return cutIndex
}

// serializePiConversation renders messages into the plain-text transcript pi
// feeds to the summarization model. Ported from pi's serializeConversation.
// includeThinking controls whether hidden assistant thinking is included; the
// checkpoint strategy omits it (the checkpoint captures observable state, and
// thinking bloats the summarizer input).
//
// seqs, when non-nil, is a parallel slice of message sequence ids; each
// message's lines are then prefixed with a [seq:N] marker so the checkpoint
// summarizer can cite retrievable pointers.
func serializePiConversation(messages []llm.Message, includeThinking bool, seqs []int64) string {
	const toolResultMaxChars = 2000
	var parts []string

	for i, msg := range messages {
		prefix := ""
		if seqs != nil {
			prefix = fmt.Sprintf("[seq:%d] ", seqs[i])
		}
		switch msg.Role {
		case llm.MessageRoleUser:
			// A user message may carry tool results (Shelley stores tool
			// results as user-role messages) or ordinary text.
			if isToolResultMessage(msg) {
				var text string
				for _, c := range msg.Content {
					for _, r := range c.ToolResult {
						if r.Type == llm.ContentTypeText {
							text += r.Text
						}
					}
				}
				if text != "" {
					parts = append(parts, prefix+"[Tool result]: "+truncateForSummary(text, toolResultMaxChars))
				}
				continue
			}
			var text strings.Builder
			for _, c := range msg.Content {
				if c.Type == llm.ContentTypeText {
					text.WriteString(c.Text)
				}
			}
			if text.Len() > 0 {
				parts = append(parts, prefix+"[User]: "+text.String())
			}
		case llm.MessageRoleAssistant:
			var textParts, thinkingParts, toolCalls []string
			for _, c := range msg.Content {
				switch c.Type {
				case llm.ContentTypeText:
					textParts = append(textParts, c.Text)
				case llm.ContentTypeThinking:
					thinkingParts = append(thinkingParts, c.Thinking)
				case llm.ContentTypeToolUse:
					toolCalls = append(toolCalls, fmt.Sprintf("%s(%s)", c.ToolName, string(c.ToolInput)))
				}
			}
			if includeThinking && len(thinkingParts) > 0 {
				parts = append(parts, prefix+"[Assistant thinking]: "+strings.Join(thinkingParts, "\n"))
			}
			if len(textParts) > 0 {
				parts = append(parts, prefix+"[Assistant]: "+strings.Join(textParts, "\n"))
			}
			if len(toolCalls) > 0 {
				parts = append(parts, prefix+"[Assistant tool calls]: "+strings.Join(toolCalls, "; "))
			}
		}
	}

	return strings.Join(parts, "\n\n")
}

func truncateForSummary(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	truncated := truncateUTF8(text, maxChars)
	return fmt.Sprintf("%s\n\n[... %d more characters truncated]", truncated, len(text)-maxChars)
}

// extractPiFileOps walks summarized assistant messages and records file paths
// touched by read/patch tools, mirroring pi's file-operation tracking so the
// summary can list read vs. modified files.
func extractPiFileOps(messages []llm.Message) (readFiles, modifiedFiles []string) {
	read := map[string]bool{}
	modified := map[string]bool{}
	for _, msg := range messages {
		if msg.Role != llm.MessageRoleAssistant {
			continue
		}
		for _, c := range msg.Content {
			if c.Type != llm.ContentTypeToolUse || len(c.ToolInput) == 0 {
				continue
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal(c.ToolInput, &args); err != nil {
				continue
			}
			path := jsonStringField(args, "path")
			if path == "" {
				continue
			}
			// Shelley tool names that carry a "path" argument. There is no
			// plain "read" tool (file reads go through bash); "patch" is the
			// only file-mutating tool with a path.
			switch c.ToolName {
			case "read_image":
				read[path] = true
			case "patch":
				modified[path] = true
			}
		}
	}
	for f := range read {
		if !modified[f] {
			readFiles = append(readFiles, f)
		}
	}
	for f := range modified {
		modifiedFiles = append(modifiedFiles, f)
	}
	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)
	return readFiles, modifiedFiles
}

func jsonStringField(args map[string]json.RawMessage, key string) string {
	raw, ok := args[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func formatPiFileOperations(readFiles, modifiedFiles []string) string {
	var sections []string
	if len(readFiles) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(readFiles, "\n")+"\n</read-files>")
	}
	if len(modifiedFiles) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(modifiedFiles, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

// piContextMessage pairs the LLM form of a source message with the original DB
// row, so the pi flow can (a) resolve distillation-summary content for
// summarization and (b) preserve user_data when copying messages verbatim into
// the new generation.
type piContextMessage struct {
	llm    llm.Message
	source generated.Message
}

// piContextMessages converts the source generation's context-eligible messages
// into llm.Messages (preserving roles and tool structure), filtering out
// system/error/gitinfo/warning/slug messages and anything excluded from context.
// Each returned entry retains its source DB row.
func piContextMessages(sourceGeneration int64, messages []generated.Message) []piContextMessage {
	var out []piContextMessage
	for _, m := range messages {
		if m.Generation != sourceGeneration || m.ExcludedFromContext {
			continue
		}
		switch m.Type {
		case string(db.MessageTypeSystem), string(db.MessageTypeError),
			string(db.MessageTypeGitInfo), string(db.MessageTypeWarning),
			string(db.MessageTypeSlug):
			continue
		}
		llmMsg, err := convertToLLMMessage(m)
		if err != nil {
			continue
		}
		out = append(out, piContextMessage{llm: llmMsg, source: m})
	}
	return out
}

// resolveDistilledContent returns the real distillation summary text for a
// previously-distilled message. The message's llm_data only holds a
// placeholder ("Distillation written to ..."); the actual summary lives in
// user_data (or the editable temp file it points at). Mirrors
// ConversationManager.applyDistillationContentOverride. Returns ok=false when
// the message is not a distilled message.
func resolveDistilledContent(logger logWarner, m generated.Message) (string, bool) {
	if m.UserData == nil {
		return "", false
	}
	var userData map[string]string
	if err := json.Unmarshal([]byte(*m.UserData), &userData); err != nil {
		return "", false
	}
	if userData["distilled"] != "true" {
		return "", false
	}
	content := userData["distillation_content"]
	if filePath := userData["distillation_file"]; filePath != "" {
		if !isDistillationTempFile(filePath) {
			logger.Warn("Distillation file path validation failed", "messageID", m.MessageID, "path", filePath)
		} else if fileContent, err := os.ReadFile(filePath); err == nil {
			content = string(fileContent)
		} else {
			logger.Warn("Failed to read editable distillation file; using stored content", "messageID", m.MessageID, "path", filePath, "error", err)
		}
	}
	return content, true
}

// logWarner is the subset of *slog.Logger used by resolveDistilledContent.
type logWarner interface {
	Warn(msg string, args ...any)
}

// resolvePiSummarizationText returns the message text to feed the summarizer,
// substituting the real summary for any distilled-message placeholder.
func resolvePiSummarizationText(logger logWarner, entry piContextMessage) llm.Message {
	content, ok := resolveDistilledContent(logger, entry.source)
	if !ok {
		return entry.llm
	}
	msg := entry.llm
	// Copy the content slice so we don't mutate the shared message.
	newContent := make([]llm.Content, len(msg.Content))
	copy(newContent, msg.Content)
	replaced := false
	for i := range newContent {
		if newContent[i].Type == llm.ContentTypeText {
			newContent[i].Text = content
			replaced = true
			break
		}
	}
	if !replaced {
		newContent = append(newContent, llm.Content{Type: llm.ContentTypeText, Text: content})
	}
	msg.Content = newContent
	return msg
}

// userDataForCopy extracts the parsed user_data map from a source message so it
// can be preserved when copying the message into the new generation. Returns
// nil when there is none.
func userDataForCopy(m generated.Message) map[string]string {
	if m.UserData == nil {
		return nil
	}
	var userData map[string]string
	if err := json.Unmarshal([]byte(*m.UserData), &userData); err != nil {
		return nil
	}
	return userData
}

// generatePiSummary runs the summarization prompt over the older messages and
// returns the summary text. checkpoint selects the topic-based checkpoint
// prompt: thinking is excluded from the input, transcript lines carry [seq:N]
// markers (from seqs) the summarizer cites as retrievable pointers, the output
// structure is validated, and the derived file-operation tags are omitted (the
// summarizer preserves important paths itself). Otherwise the original pi
// task-report prompt and file tags are used.
func (s *Server) generatePiSummary(ctx context.Context, svc llm.Service, older []llm.Message, seqs []int64, instructions string, checkpoint bool) (string, error) {
	prompt := piSummarizationPrompt
	if checkpoint {
		prompt = checkpointSummarizationPrompt
	} else {
		seqs = nil // pi task report cites nothing; don't add marker noise
	}
	conversationText := serializePiConversation(older, !checkpoint, seqs)
	promptText := fmt.Sprintf("<conversation>\n%s\n</conversation>\n\n%s", conversationText, prompt)
	if steer := strings.TrimSpace(instructions); steer != "" {
		promptText += steeringSection(steer)
	}

	resp, err := svc.Do(ctx, &llm.Request{
		// Summarization is a simple extraction task; disable thinking to cut
		// cost and latency.
		ThinkingLevel: llm.ThinkingLevelOff,
		System: []llm.SystemContent{
			{Text: piSummarizationSystemPrompt, Type: "text"},
		},
		Messages: []llm.Message{
			{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{{Type: llm.ContentTypeText, Text: promptText}},
			},
		},
	})
	if err != nil {
		return "", err
	}

	var summary string
	for _, c := range resp.Content {
		if c.Type == llm.ContentTypeText {
			summary += c.Text
		}
	}
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("summarization returned empty result")
	}

	if checkpoint {
		if err := validateCheckpointSummary(summary); err != nil {
			return "", err
		}
		return summary, nil
	}
	readFiles, modifiedFiles := extractPiFileOps(older)
	summary += formatPiFileOperations(readFiles, modifiedFiles)
	return summary, nil
}

// checkpointSummarizerService returns the service for the first available
// model tagged "checkpoint" (then "checkpoint-backup"), or ("", nil) when
// none exists. Eval sweeps across real conversations picked glm-5.2 for the
// tag: it completed every fixture where kimi-k3 timed out on the large
// transcripts, at lower cost.
func (s *Server) checkpointSummarizerService() (string, llm.Service) {
	for _, tag := range []string{"checkpoint", "checkpoint-backup"} {
		for _, id := range s.llmManager.GetAvailableModels() {
			info := s.llmManager.GetModelInfo(id)
			if info == nil || !hasModelTag(info.Tags, tag) {
				continue
			}
			if svc, err := s.llmManager.GetService(id); err == nil {
				return id, svc
			}
		}
	}
	return "", nil
}

// hasModelTag checks if a comma-separated tag list contains the exact tag.
func hasModelTag(tags, tag string) bool {
	for _, t := range strings.Split(tags, ",") {
		if strings.TrimSpace(t) == tag {
			return true
		}
	}
	return false
}

// rollbackCompactionFailure restores the conversation to its pre-compaction
// generation and inserts a distill error message. The generation counter is
// bumped before summarization runs, so a failure would otherwise leave the
// conversation on an EMPTY new generation — silently wiping its working
// context and making forks of it empty. Rolling back keeps the old (intact)
// generation active; the error message (inserted after the rollback, so it
// lands in the restored generation) tells the user compaction failed. The
// already-written new-generation rows (the "Distilling…" status and a fresh
// system prompt) are left in place: messages are append-only, and they are
// invisible to context once current_generation points back at the old
// generation. The conversation manager's loop is reset so the next turn
// rehydrates from the restored generation.
//
// Two deliberate non-rollbacks: (1) a model/cwd change requested with the
// compaction stays (it was validated, and the user asked for it); (2) the
// abandoned generation's rows are not deleted — a later retry re-increments
// into the same generation number and Hydrate's hasSystemMessage guard
// prevents a duplicate system prompt.
func (s *Server) rollbackCompactionFailure(ctx context.Context, logger *slog.Logger, conversationID, errMsg string, sourceGeneration int64) {
	if err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
		_, err := q.SetConversationGeneration(ctx, generated.SetConversationGenerationParams{
			CurrentGeneration: sourceGeneration,
			ConversationID:    conversationID,
		})
		return err
	}); err != nil {
		logger.Error("Failed to roll back generation after compaction failure", "error", err)
		s.insertDistillError(ctx, conversationID, errMsg)
		return
	}
	s.mu.Lock()
	manager, ok := s.activeConversations[conversationID]
	s.mu.Unlock()
	if ok {
		manager.ResetLoop()
	}
	logger.Info("rolled back generation after compaction failure", "generation", sourceGeneration)
	s.insertDistillError(ctx, conversationID, errMsg+" — the conversation was left uncompacted.")
}

// performPiDistillation summarizes older history and copies recent messages
// verbatim into the conversation's (already-incremented) new generation.
// method selects the summarizer: distillMethodCompact (pi task report) or
// distillMethodCheckpoint (topic-based checkpoint).
func (s *Server) performPiDistillation(ctx context.Context, conversationID, sourceSlug, modelID, method, instructions string, sourceGeneration int64, messages []generated.Message) string {
	logger := s.logger.With("conversationID", conversationID, "sourceSlug", sourceSlug, "method", method)

	// Tag the ctx so the summarization calls' usage is collected (and so the
	// gateway request logs carry the conversation ID; the HTTP request ctx
	// this derives from carries neither). The collected entries are attached
	// to the summary message below.
	var otherUsage llmhttp.UsageAccumulator
	ctx = llmhttp.WithUsageCollector(ctx, otherUsage.Collect)
	ctx = llmhttp.WithConversationID(llmhttp.WithPurpose(ctx, "compaction"), conversationID)

	svc, err := s.llmManager.GetService(modelID)
	if err != nil {
		logger.Error("Failed to get LLM service for pi distillation", "model", modelID, "error", err)
		// The generation was already incremented; roll back so the old
		// (intact) generation stays active (see rollbackCompactionFailure).
		s.rollbackCompactionFailure(ctx, logger, conversationID, fmt.Sprintf("Compaction failed: model %q unavailable: %v", modelID, err), sourceGeneration)
		return ""
	}

	ctxMsgs := piContextMessages(sourceGeneration, messages)
	if len(ctxMsgs) == 0 {
		logger.Warn("pi distillation found no context messages")
		s.insertDistillStatus(ctx, conversationID, "complete")
		return ""
	}

	keepRecentTokens := defaultPiDistillSettings.keepRecentTokens
	if s.piDistillKeepRecentTokens > 0 {
		keepRecentTokens = s.piDistillKeepRecentTokens
	}
	llmMsgs := make([]llm.Message, len(ctxMsgs))
	for i, entry := range ctxMsgs {
		llmMsgs[i] = entry.llm
	}
	cut := findPiCutPoint(llmMsgs, keepRecentTokens)
	older := ctxMsgs[:cut]
	recent := ctxMsgs[cut:]
	logger.Info("pi cut point computed", "total", len(ctxMsgs), "summarized", len(older), "kept", len(recent))

	// Resolve any previously-distilled placeholder text in the older slice to
	// the real prior summary before summarizing, so re-distillation doesn't
	// feed the summarizer "Distillation written to ..." placeholders.
	olderMsgs := make([]llm.Message, len(older))
	for i, entry := range older {
		olderMsgs[i] = resolvePiSummarizationText(logger, entry)
	}

	var summary string
	var fallbackNotice string
	summaryModelID := modelID
	checkpoint := method == distillMethodCheckpoint
	if checkpoint {
		// Checkpoint summaries prefer a dedicated summarizer model (tag
		// "checkpoint", currently glm-5.2): sweeping real conversations showed
		// the conversation's own model is often slower or refuses, while the
		// tagged model completed every fixture at lower cost. Fall back to the
		// conversation model when no tagged model is available.
		if tid, tsvc := s.checkpointSummarizerService(); tsvc != nil {
			summaryModelID = tid
			svc = tsvc
		}
	}
	olderSeqs := make([]int64, len(older))
	for i, entry := range older {
		olderSeqs[i] = entry.source.SequenceID
	}
	if len(older) > 0 {
		distillCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		summary, err = s.generatePiSummary(distillCtx, svc, olderMsgs, olderSeqs, instructions, checkpoint)
		cancel()
		if err != nil {
			// Some models decline summarization prompts (e.g. fable returns
			// stop_reason=refusal with empty content on tool-heavy
			// transcripts). Retry once with the server's default model before
			// giving up.
			fallbackID := s.effectiveDefaultModel(s.getModelList())
			if fallbackID == "" || fallbackID == summaryModelID {
				logger.Warn("no fallback model available for summarization retry", "model", summaryModelID, "default_model", fallbackID)
			} else {
				fallbackErr := err
				logger.Warn("pi summarization failed; retrying with default model", "error", err, "fallback_model", fallbackID)
				if fallbackSvc, ferr := s.llmManager.GetService(fallbackID); ferr != nil {
					logger.Error("Failed to get fallback LLM service", "model", fallbackID, "error", ferr)
				} else {
					distillCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
					summary, err = s.generatePiSummary(distillCtx, fallbackSvc, olderMsgs, olderSeqs, instructions, checkpoint)
					cancel()
					if err == nil {
						fallbackNotice = fmt.Sprintf("Note: %s failed to summarize the conversation (%v); the summary was generated by %s instead.", summaryModelID, fallbackErr, fallbackID)
						summaryModelID = fallbackID
					}
				}
			}
		}
		if err != nil {
			logger.Error("pi summarization failed", "error", err)
			// The generation was already incremented before this goroutine
			// started, so failing here would leave the new generation empty:
			// the conversation's context (and any fork of it) would be wiped.
			// Roll back to the old generation so the failure is loud but
			// harmless.
			s.rollbackCompactionFailure(ctx, logger, conversationID, fmt.Sprintf("Compaction failed: %v", err), sourceGeneration)
			return ""
		}
	}

	// Insert the summary as the opening context message. Unlike the default
	// distill flow, the compaction summary is NOT editable: it is a generated
	// checkpoint paired with a verbatim recent tail, so editing it in isolation
	// would be misleading. We therefore store the summary text inline in
	// user_data (no editable temp file) and put it directly in the message
	// body so it renders as-is.
	suffix := piCompactionSummarySuffix
	if checkpoint && summary != "" {
		// Strip fabricated [seq:N] pointers (validity = the sequence id names
		// a real message in this conversation) and teach the reader how to
		// resolve real ones with sqlite+bash.
		validSeqs := make(map[int64]bool, len(messages))
		for i := range messages {
			validSeqs[messages[i].SequenceID] = true
		}
		summary = sanitizeCheckpointPointers(summary, validSeqs)
		suffix = checkpointCompactionSummarySuffix
	}
	wrapped := piCompactionSummaryPrefix + summary + suffix
	// Build the summary message (if any) plus the verbatim recent tail, then
	// write them all in ONE transaction. Doing each in its own Tx fired a
	// full conversation-list recompute per message (one per commit hook),
	// which made the stream load visibly slow — you could watch the carried
	// count tick up. A single batch is one commit, one recompute, one SSE frame.
	var batch []recordMessageInput
	if fallbackNotice != "" {
		// A visible note that a different model wrote the summary. Excluded
		// from context: informational, not part of the conversation. Written
		// in the same batch as the summary so it can't outlive a failed write.
		batch = append(batch, recordMessageInput{message: llm.Message{
			Role:                llm.MessageRoleAssistant,
			ExcludedFromContext: true,
			Content:             []llm.Content{{Type: llm.ContentTypeText, Text: fallbackNotice}},
		}})
	}
	if summary != "" {
		// The summary is a user-role message; the kept tail (recent[0]) may also
		// be a user message, producing two consecutive user messages. That is
		// fine: Shelley already emits consecutive user messages when a user
		// queues several turns (loop appends them without merging), and pi's own
		// compaction inserts its summary the same way.
		summaryMessage := llm.Message{
			Role: llm.MessageRoleUser,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: wrapped},
			},
		}
		userData := map[string]string{
			"distilled":            "true",
			"distillation_content": wrapped,
			"distill_method":       method,
			// Provenance: which generation and message range was summarized,
			// and which model actually produced the summary (may be the
			// fallback). The originals stay in the previous generation, so
			// this makes the checkpoint auditable.
			"compacts_source_generation":   fmt.Sprintf("%d", sourceGeneration),
			"compacts_first_sequence_id":   fmt.Sprintf("%d", older[0].source.SequenceID),
			"compacts_through_sequence_id": fmt.Sprintf("%d", older[len(older)-1].source.SequenceID),
			"summarizer_model":             summaryModelID,
		}
		// Attach the summarization calls' usage (primary + fallback) to the
		// summary message so compaction cost is visible in cost reporting.
		batch = append(batch, recordMessageInput{message: summaryMessage, otherUsage: otherUsage.Take(), userData: []interface{}{userData}})
	}

	// Copy recent messages verbatim into the new generation so the agent keeps
	// exact recent tool calls and results. The copies are re-recorded with ZERO
	// usage (usage_data all zeros) and NULL other_usage_data: the original rows
	// in the previous generation keep the real numbers, so the cost was already
	// counted once. Preserve each message's user_data so
	// a previously-distilled message in the kept tail keeps its distilled=true
	// marker — otherwise applyDistillationContentOverride would never fire and
	// its real summary text would be lost. Stamp compaction_carried=true on every
	// copy so the UI can collapse the re-played tail behind a "messages carried
	// forward" band instead of re-rendering each one (slow, jarring scroll).
	for _, entry := range recent {
		ud := userDataForCopy(entry.source)
		if ud == nil {
			ud = map[string]string{}
		}
		ud["compaction_carried"] = "true"
		batch = append(batch, recordMessageInput{message: entry.llm, userData: []interface{}{ud}})
	}

	// Append the terminal "complete" status message as an additional INSERT in
	// the SAME batch that writes the summary + carried tail, so it rides along in
	// one commit (one conversation-list recompute, one SSE frame) rather than
	// paying a second commit. Messages are immutable, so instead of mutating the
	// "Compacting…" in_progress message we emit a second message and let the UI
	// collapse the pair. Fall back to a standalone insert if the batch is empty
	// (recordMessages is a no-op for an empty batch) or the in_progress message
	// can't be located.
	statusMsg, statusData, haveStatus := s.terminalDistillStatusMessage(ctx, conversationID, "complete")
	foldedStatus := haveStatus && len(batch) > 0
	if foldedStatus {
		batch = append(batch, recordMessageInput{
			message:  statusMsg,
			userData: []interface{}{statusData},
		})
	}
	if rerr := s.recordMessages(ctx, conversationID, batch); rerr != nil {
		logger.Error("Failed to record compaction messages", "error", rerr)
		// Same empty-new-generation hazard as a summarization failure.
		s.rollbackCompactionFailure(ctx, logger, conversationID, fmt.Sprintf("Compaction failed: could not record messages: %v", rerr), sourceGeneration)
		return ""
	}
	if !foldedStatus {
		s.insertDistillStatus(ctx, conversationID, "complete")
	}
	logger.Info("pi distillation complete", "summary_length", len(summary), "kept_messages", len(recent))
	return summary
}
