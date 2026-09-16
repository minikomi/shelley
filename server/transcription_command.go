package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

const (
	transcriptionTimeout = 15 * time.Minute
	transcriptionKind    = "transcription"
)

type mediaCommandRunner func(context.Context, string, ...string) ([]byte, error)

type transcriptionMedia struct {
	HasVideo        bool
	DurationSeconds float64
}

type recordingMetadata struct {
	Path       string `json:"path"`
	DurationMS int64  `json:"duration_ms"`
}

type transcriptionJob struct {
	parentID string
	childID  string
	cancel   context.CancelFunc
	done     chan struct{}
}

var errQueuedTranscriptionSuperseded = errors.New("queued transcription was cancelled or superseded")

func parseTranscriptionCommand(message string) (path, context string, ok bool) {
	message = strings.TrimLeft(message, " \t\r\n")
	const command = "/transcription"
	if message == command {
		return "", "", true
	}
	if !strings.HasPrefix(message, command) || len(message) == len(command) {
		return "", "", false
	}
	switch message[len(command)] {
	case ' ', '\t':
		remainder := strings.TrimSpace(message[len(command):])
		pathLine, context, _ := strings.Cut(remainder, "\n")
		return strings.TrimSpace(pathLine), strings.TrimSpace(context), true
	case '\r', '\n':
		return "", "", true
	default:
		return "", "", false
	}
}

func validateTranscriptionPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("/transcription requires an absolute recording path")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("recording path must be absolute")
	}

	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return "", fmt.Errorf("recording path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("recording path must be a regular file")
	}

	root, err := filepath.EvalSymlinks(browse.UploadDir)
	if err != nil {
		return "", fmt.Errorf("resolve upload directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("resolve recording path: %w", err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", fmt.Errorf("compare recording path with upload directory: %w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("recording path must be inside %s", browse.UploadDir)
	}
	return clean, nil
}

func runMediaCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func probeTranscriptionMedia(ctx context.Context, path string, run mediaCommandRunner) (transcriptionMedia, error) {
	out, err := run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type,duration", "-of", "json", path)
	if err != nil {
		return transcriptionMedia{}, err
	}
	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Duration  string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return transcriptionMedia{}, fmt.Errorf("parse ffprobe output: %w", err)
	}

	media := transcriptionMedia{}
	var durations []string
	if probe.Format.Duration != "" {
		durations = append(durations, probe.Format.Duration)
	}
	for _, stream := range probe.Streams {
		if stream.CodecType != "video" {
			continue
		}
		media.HasVideo = true
		if stream.Duration != "" {
			durations = append(durations, stream.Duration)
		}
	}
	if !media.HasVideo {
		return media, nil
	}
	for _, raw := range durations {
		seconds, err := strconv.ParseFloat(raw, 64)
		if err == nil && seconds > 0 && !math.IsInf(seconds, 0) && !math.IsNaN(seconds) {
			media.DurationSeconds = seconds
			return media, nil
		}
	}

	metadataJSON, err := os.ReadFile(path + ".json")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return transcriptionMedia{}, errors.New("video duration is unavailable")
		}
		return transcriptionMedia{}, fmt.Errorf("read recording metadata: %w", err)
	}
	var metadata recordingMetadata
	if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
		return transcriptionMedia{}, fmt.Errorf("parse recording metadata: %w", err)
	}
	if metadata.Path != path || metadata.DurationMS <= 0 {
		return transcriptionMedia{}, errors.New("recording metadata does not contain a valid duration")
	}
	media.DurationSeconds = float64(metadata.DurationMS) / 1000
	return media, nil
}

func createVideoContactSheet(ctx context.Context, mediaPath string, media transcriptionMedia, run mediaCommandRunner) (string, error) {
	if !media.HasVideo || media.DurationSeconds <= 0 {
		return "", errors.New("contact sheet requires video with a positive duration")
	}
	const frameCount = 12
	fps := float64(frameCount) / media.DurationSeconds
	start := media.DurationSeconds / (2 * frameCount)
	filter := fmt.Sprintf(
		"fps=fps=%.9f:start_time=%.9f,scale=w=320:h=180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2:black,tile=4x3:padding=4:margin=4",
		fps, start,
	)

	contactPath := mediaPath + ".contact-sheet.jpg"
	tempPath := contactPath + ".tmp-" + uuid.NewString() + ".jpg"
	defer os.Remove(tempPath)
	if _, err := run(ctx, "ffmpeg", "-nostdin", "-v", "error", "-y", "-i", mediaPath, "-vf", filter, "-frames:v", "1", "-q:v", "3", tempPath); err != nil {
		return "", err
	}
	info, err := os.Stat(tempPath)
	if err != nil {
		return "", fmt.Errorf("stat generated contact sheet: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("ffmpeg did not create a regular contact sheet file")
	}
	if err := os.Rename(tempPath, contactPath); err != nil {
		return "", fmt.Errorf("install contact sheet: %w", err)
	}
	return contactPath, nil
}

func transcriptionPrompt(mediaPath, contactSheetPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Transcribe the recording at %s. The server has already validated the file and inspected its media streams. ", strconv.Quote(mediaPath))
	if contactSheetPath != "" {
		fmt.Fprintf(&b, "This recording contains video; its JPEG contact sheet is at %s. ", strconv.Quote(contactSheetPath))
	}
	b.WriteString("Return only the user's spoken words.")
	return b.String()
}

// validateTranscriptionCommand reports whether message is a built-in
// /transcription command and, if so, validates its uploaded media path.
func validateTranscriptionCommand(message string) (mediaPath, context string, isTranscription bool, err error) {
	path, context, ok := parseTranscriptionCommand(message)
	if !ok {
		return "", "", false, nil
	}
	mediaPath, err = validateTranscriptionPath(path)
	return mediaPath, context, true, err
}

// queueTranscription durably queues an uploaded recording on an
// already-promoted parent and starts its detached worker. The job context is
// owned by the server, not by this request, so navigation or disconnect does
// nothing.
func (s *Server) queueTranscription(ctx context.Context, w http.ResponseWriter, manager *ConversationManager, mediaPath, transcriptionContext, modelID string) {
	queued := db.QueuedMessage{
		ID:        uuid.NewString(),
		CreatedAt: time.Now().UTC(),
		Model:     modelID,
		UserEmail: userEmailFromContext(ctx),
		Kind:      db.QueuedMessageKindTranscription,
		State:     db.QueuedMessageStateWorking,
		Transcription: &db.QueuedTranscription{
			MediaPath: mediaPath,
			Context:   strings.TrimSpace(transcriptionContext),
		},
	}
	cwd := manager.Cwd()
	queued, err := manager.QueueTranscription(ctx, s, queued, &cwd)
	if err != nil {
		s.logger.Error("Failed to queue transcription", "conversationID", manager.conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	s.launchQueuedTranscription(manager.conversationID, queued)
	writeQueuedTranscription(w)
}

func writeQueuedTranscription(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "queued"})
}

func transcriptionChildOptions() (slug string, opts db.ConversationOptions) {
	return "transcription-" + uuid.NewString(), db.ConversationOptions{Kind: transcriptionKind, ThinkingLevel: "low"}
}

func (s *Server) launchQueuedTranscription(parentID string, queued db.QueuedMessage) {
	if queued.Transcription == nil || queued.Transcription.ChildConversationID == "" {
		s.logger.Error("Cannot launch invalid queued transcription", "parent", parentID, "queued_id", queued.ID)
		return
	}

	s.transcriptionMu.Lock()
	if existing, ok := s.transcriptionJobs[queued.ID]; ok {
		if existing.childID == queued.Transcription.ChildConversationID {
			s.transcriptionMu.Unlock()
			return
		}
		existing.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), transcriptionTimeout)
	done := make(chan struct{})
	s.transcriptionJobs[queued.ID] = transcriptionJob{parentID: parentID, childID: queued.Transcription.ChildConversationID, cancel: cancel, done: done}
	s.transcriptionMu.Unlock()

	go func() {
		defer cancel()
		defer close(done)
		defer func() {
			s.transcriptionMu.Lock()
			if current, ok := s.transcriptionJobs[queued.ID]; ok && current.childID == queued.Transcription.ChildConversationID {
				delete(s.transcriptionJobs, queued.ID)
			}
			s.transcriptionMu.Unlock()
		}()
		s.runQueuedTranscription(ctx, parentID, queued)
	}()
}

func validateCurrentQueuedTranscription(queued *db.QueuedMessage, childID string) error {
	if queued.Kind != db.QueuedMessageKindTranscription ||
		queued.State != db.QueuedMessageStateWorking ||
		queued.Transcription == nil ||
		queued.Transcription.ChildConversationID != childID {
		return errQueuedTranscriptionSuperseded
	}
	return nil
}

func (s *Server) updateCurrentQueuedTranscription(ctx context.Context, parentID, queuedID, childID string, update func(*db.QueuedMessage)) (db.QueuedMessage, error) {
	s.transcriptionMu.Lock()
	conv, queued, err := s.db.UpdateQueuedMessage(ctx, parentID, queuedID, func(current *db.QueuedMessage) error {
		if err := validateCurrentQueuedTranscription(current, childID); err != nil {
			return err
		}
		update(current)
		return nil
	})
	s.transcriptionMu.Unlock()
	if err != nil {
		return db.QueuedMessage{}, err
	}
	go s.notifySubscribers(context.Background(), parentID)
	if conv != nil {
		go s.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: conv})
	}
	return queued, nil
}

func (s *Server) runQueuedTranscription(ctx context.Context, parentID string, queued db.QueuedMessage) {
	childID := queued.Transcription.ChildConversationID
	mediaPath := queued.Transcription.MediaPath
	if !s.queuedTranscriptionIsCurrent(ctx, parentID, queued) {
		return
	}

	media, err := probeTranscriptionMedia(ctx, mediaPath, s.mediaRun)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, childID, fmt.Errorf("inspect recording: %w", err))
		return
	}
	if media.HasVideo && queued.Transcription.ContactSheetPath == "" {
		contactSheetPath, err := createVideoContactSheet(ctx, mediaPath, media, s.mediaRun)
		if err != nil {
			s.failQueuedTranscription(parentID, queued.ID, childID, fmt.Errorf("create contact sheet: %w", err))
			return
		}
		// Persist the sheet before transcription so a restart reuses it.
		updated, err := s.updateCurrentQueuedTranscription(context.Background(), parentID, queued.ID, childID, func(current *db.QueuedMessage) {
			current.Transcription.ContactSheetPath = contactSheetPath
		})
		if err != nil {
			s.logQueuedTranscriptionError("Failed to persist transcription contact sheet", parentID, queued.ID, err)
			return
		}
		queued = updated
	}

	toolUseID, err := s.startTranscriptionChild(ctx, childID, mediaPath, queued.Transcription.ContactSheetPath)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, childID, err)
		return
	}
	started := time.Now()
	result, err := s.transcriber.Transcribe(ctx, mediaPath)
	finished := time.Now()
	if recordErr := s.finishTranscriptionChild(context.Background(), childID, toolUseID, result, started, finished, err); recordErr != nil {
		s.failQueuedTranscription(parentID, queued.ID, childID, recordErr)
		return
	}
	s.settleQueuedTranscription(parentID, queued)
}

// queuedTranscriptionIsCurrent reports whether the durable item still names
// this worker's child in the working state; anything else means it was
// cancelled, retried, or already settled.
func (s *Server) queuedTranscriptionIsCurrent(ctx context.Context, parentID string, queued db.QueuedMessage) bool {
	current, err := s.db.GetQueuedMessage(ctx, parentID, queued.ID)
	return err == nil && validateCurrentQueuedTranscription(&current, queued.Transcription.ChildConversationID) == nil
}

// settleQueuedTranscription reads the child's finished turn and moves the item
// to ready or failed. A child that is still mid-turn is stopped and failed.
func (s *Server) settleQueuedTranscription(parentID string, queued db.QueuedMessage) {
	childID := queued.Transcription.ChildConversationID
	text, done, err := s.completedTranscriptionChild(context.Background(), childID)
	switch {
	case !done:
		s.stopTranscriptionChild(childID)
		s.failQueuedTranscription(parentID, queued.ID, childID, errors.New("transcription worker did not finish"))
	case err != nil:
		s.failQueuedTranscription(parentID, queued.ID, childID, err)
	default:
		s.finalizeQueuedTranscription(parentID, queued, text)
	}
}

func (s *Server) startTranscriptionChild(ctx context.Context, childID, mediaPath, contactSheetPath string) (string, error) {
	messages, err := s.db.ListMessages(ctx, childID)
	if err != nil {
		return "", err
	}
	hasPrompt := false
	for _, message := range messages {
		if message.Type == string(db.MessageTypeUser) && message.LlmData != nil {
			var value llm.Message
			if json.Unmarshal([]byte(*message.LlmData), &value) == nil {
				for _, content := range value.Content {
					if content.Type == llm.ContentTypeText {
						hasPrompt = true
					}
				}
			}
		}
		if message.Type != string(db.MessageTypeAgent) || message.LlmData == nil {
			continue
		}
		var value llm.Message
		if json.Unmarshal([]byte(*message.LlmData), &value) != nil {
			continue
		}
		for _, content := range value.Content {
			if content.Type == llm.ContentTypeToolUse && content.ToolName == "openai_audio_transcription" {
				return content.ID, nil
			}
		}
	}

	toolUseID := "transcription_" + uuid.NewString()
	toolInput, err := json.Marshal(map[string]string{
		"endpoint": openAITranscriptionEndpoint,
		"file":     mediaPath,
		"model":    openAITranscriptionModel,
	})
	if err != nil {
		return "", err
	}
	params := make([]db.CreateMessageParams, 0, 2)
	if !hasPrompt {
		params = append(params, db.CreateMessageParams{
			ConversationID: childID,
			Type:           db.MessageTypeUser,
			LLMData:        llm.UserStringMessage(transcriptionPrompt(mediaPath, contactSheetPath)),
			MarkAgentStart: true,
			BumpTimestamp:  true,
		})
	}
	params = append(params, db.CreateMessageParams{
		ConversationID: childID,
		Type:           db.MessageTypeAgent,
		LLMData: llm.Message{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{{
				ID:        toolUseID,
				Type:      llm.ContentTypeToolUse,
				ToolName:  "openai_audio_transcription",
				ToolInput: toolInput,
			}},
		},
		MarkAgentStart: hasPrompt,
		BumpTimestamp:  true,
	})
	_, err = s.db.CreateMessages(ctx, params)
	return toolUseID, err
}

func (s *Server) finishTranscriptionChild(ctx context.Context, childID, toolUseID string, result transcriptionResult, started, finished time.Time, failure error) error {
	toolOutput := map[string]any{
		"duration_ms": finished.Sub(started).Milliseconds(),
		"model":       openAITranscriptionModel,
	}
	toolError := failure != nil
	finalType := db.MessageTypeAgent
	finalText := result.Text
	if failure != nil {
		toolOutput["error"] = queuedTranscriptionError(failure)
		finalType = db.MessageTypeError
		finalText = queuedTranscriptionError(failure)
	} else {
		toolOutput["text"] = result.Text
		if result.Model != "" {
			toolOutput["model"] = result.Model
		}
	}
	outputJSON, err := json.Marshal(toolOutput)
	if err != nil {
		return err
	}
	_, err = s.db.CreateMessages(ctx, []db.CreateMessageParams{
		{
			ConversationID: childID,
			Type:           db.MessageTypeUser,
			LLMData: llm.Message{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{{
					Type:             llm.ContentTypeToolResult,
					ToolUseID:        toolUseID,
					ToolError:        toolError,
					ToolUseStartTime: &started,
					ToolUseEndTime:   &finished,
					ToolResult: []llm.Content{{
						Type: llm.ContentTypeText,
						Text: string(outputJSON),
					}},
				}},
			},
			BumpTimestamp: true,
		},
		{
			ConversationID: childID,
			Type:           finalType,
			LLMData: llm.Message{
				Role:      llm.MessageRoleAssistant,
				Content:   []llm.Content{{Type: llm.ContentTypeText, Text: finalText}},
				EndOfTurn: true,
			},
			LLMAPIURL:     openAITranscriptionEndpoint,
			ModelName:     openAITranscriptionModel,
			MarkAgentDone: true,
			BumpTimestamp: true,
		},
	})
	return err
}

func (s *Server) completedTranscriptionChild(ctx context.Context, childID string) (string, bool, error) {
	messages, err := s.db.ListMessages(ctx, childID)
	if err != nil {
		return "", false, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		message := &messages[i]
		if message.Type != string(db.MessageTypeAgent) && message.Type != string(db.MessageTypeError) {
			continue
		}
		if !isAgentEndOfTurn(message) {
			return "", false, nil
		}
		if message.LlmData == nil {
			return "", true, errors.New("transcription worker returned no result")
		}
		var value llm.Message
		if err := json.Unmarshal([]byte(*message.LlmData), &value); err != nil {
			return "", true, err
		}
		var parts []string
		for _, content := range value.Content {
			if content.Type == llm.ContentTypeText && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, content.Text)
			}
		}
		text := strings.TrimSpace(llm.StripInlineCitationMarkers(strings.Join(parts, "\n")))
		if message.Type == string(db.MessageTypeError) {
			if text == "" {
				text = "transcription worker failed"
			}
			return "", true, errors.New(text)
		}
		if text == "" {
			return "", true, errors.New("transcription worker returned an empty transcript")
		}
		return text, true, nil
	}
	return "", false, nil
}

func transcriptionParentMessage(text, mediaPath, contactSheetPath, transcriptionContext string) llm.Message {
	parts := make([]string, 0, 5)
	if transcriptionContext = strings.TrimSpace(transcriptionContext); transcriptionContext != "" {
		parts = append(parts, transcriptionContext)
	}
	parts = append(parts, strings.TrimSpace(text))
	if contactSheetPath != "" {
		parts = append(parts, "["+mediaPath+"]", "["+contactSheetPath+"]")
	}
	return llm.UserStringMessage(strings.Join(parts, "\n\n"))
}

func (s *Server) finalizeQueuedTranscription(parentID string, queued db.QueuedMessage, text string) {
	childID := queued.Transcription.ChildConversationID
	message := transcriptionParentMessage(text, queued.Transcription.MediaPath, queued.Transcription.ContactSheetPath, queued.Transcription.Context)
	llmJSON, err := json.Marshal(message)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, childID, err)
		return
	}
	audit, err := s.transcriptionChildAuditMessages(context.Background(), childID)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, childID, err)
		return
	}
	var auditJSON json.RawMessage
	if len(audit) > 0 {
		auditJSON, err = json.Marshal(audit)
		if err != nil {
			s.failQueuedTranscription(parentID, queued.ID, childID, err)
			return
		}
	}
	ready, err := s.updateCurrentQueuedTranscription(context.Background(), parentID, queued.ID, childID, func(current *db.QueuedMessage) {
		current.State = db.QueuedMessageStateReady
		current.Llm = llmJSON
		current.Transcription.Audit = auditJSON
		current.Error = ""
	})
	if err != nil {
		s.logQueuedTranscriptionError("Failed to finalize queued transcription", parentID, queued.ID, err)
		return
	}
	manager, err := s.getOrCreateConversationManager(context.Background(), parentID, "")
	if err != nil {
		s.logger.Error("Failed to resume parent queue after transcription", "parent", parentID, "queued_id", queued.ID, "error", err)
		return
	}
	messages, err := readyTranscriptionMessages(ready)
	if err != nil {
		s.logger.Error("Failed to decode finalized transcription", "parent", parentID, "queued_id", queued.ID, "error", err)
		return
	}
	manager.ResolveQueuedTranscription(s, ready.ID, messages, ready.Model, ready.UserEmail)
}

func (s *Server) transcriptionChildAuditMessages(ctx context.Context, childID string) ([]llm.Message, error) {
	rows, err := s.db.ListMessages(ctx, childID)
	if err != nil {
		return nil, err
	}
	var toolUse llm.Message
	var toolUseID string
	for _, row := range rows {
		if row.LlmData == nil {
			continue
		}
		var message llm.Message
		if err := json.Unmarshal([]byte(*row.LlmData), &message); err != nil {
			return nil, err
		}
		for _, content := range message.Content {
			switch {
			case content.Type == llm.ContentTypeToolUse && content.ToolName == "openai_audio_transcription":
				toolUse = message
				toolUse.ExcludedFromContext = true
				toolUseID = content.ID
			case toolUseID != "" && content.Type == llm.ContentTypeToolResult && content.ToolUseID == toolUseID:
				message.ExcludedFromContext = true
				return []llm.Message{toolUse, message}, nil
			}
		}
	}
	return nil, nil
}

func readyTranscriptionMessages(queued db.QueuedMessage) ([]llm.Message, error) {
	var transcript llm.Message
	if err := json.Unmarshal(queued.Llm, &transcript); err != nil {
		return nil, err
	}
	var messages []llm.Message
	if queued.Transcription != nil && len(queued.Transcription.Audit) > 0 {
		if err := json.Unmarshal(queued.Transcription.Audit, &messages); err != nil {
			return nil, err
		}
		for i := range messages {
			messages[i].ExcludedFromContext = true
		}
	}
	return append(messages, transcript), nil
}

// logQueuedTranscriptionError logs a state-transition failure unless the item
// was simply cancelled or superseded, which is an expected outcome.
func (s *Server) logQueuedTranscriptionError(msg, parentID, queuedID string, err error) {
	if errors.Is(err, db.ErrQueuedMessageNotFound) || errors.Is(err, errQueuedTranscriptionSuperseded) {
		return
	}
	s.logger.Error(msg, "parent", parentID, "queued_id", queuedID, "error", err)
}

func queuedTranscriptionError(err error) string {
	runes := []rune(strings.TrimSpace(err.Error()))
	if len(runes) > 1000 {
		runes = runes[:1000]
	}
	return string(runes)
}

func (s *Server) failQueuedTranscription(parentID, queuedID, childID string, failure error) {
	_, err := s.updateCurrentQueuedTranscription(context.Background(), parentID, queuedID, childID, func(current *db.QueuedMessage) {
		current.State = db.QueuedMessageStateFailed
		current.Error = queuedTranscriptionError(failure)
	})
	if err != nil {
		s.logQueuedTranscriptionError("Failed to mark queued transcription failed", parentID, queuedID, err)
	}
}

// cancelQueuedTranscriptions cancels the detached workers of the matching
// queue items (every item of the parent when queuedID is empty), runs persist
// under the same mutex so no worker state transition can interleave with the
// removal, and finally tears down the workers' still-running children. Both
// the durable queue and the in-memory registry are consulted so a worker
// launched concurrently with a cancel-all cannot escape.
func (s *Server) cancelQueuedTranscriptions(ctx context.Context, parentID, queuedID string, persist func() error) error {
	queuedMessages, err := s.db.GetQueuedMessages(ctx, parentID)
	if err != nil {
		return err
	}
	children := make(map[string]bool)
	for _, queued := range queuedMessages {
		if (queuedID == "" || queued.ID == queuedID) && queued.Kind == db.QueuedMessageKindTranscription && queued.Transcription != nil {
			children[queued.Transcription.ChildConversationID] = true
		}
	}
	s.transcriptionMu.Lock()
	for id, job := range s.transcriptionJobs {
		if job.parentID == parentID && (queuedID == "" || id == queuedID) {
			job.cancel()
			delete(s.transcriptionJobs, id)
			children[job.childID] = true
		}
	}
	err = persist()
	s.transcriptionMu.Unlock()
	if err != nil {
		return err
	}
	for childID := range children {
		if childID != "" {
			s.stopTranscriptionChild(childID)
		}
	}
	return nil
}

func (s *Server) stopTranscriptionChild(childID string) {
	s.mu.Lock()
	manager := s.activeConversations[childID]
	s.mu.Unlock()
	if manager == nil || !manager.IsAgentWorking() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.CancelConversation(ctx); err != nil {
		s.logger.Error("Failed to cancel transcription child", "child", childID, "error", err)
	}
}

func (s *Server) handleRetryQueued(w http.ResponseWriter, r *http.Request, parentID string) {
	queuedID := r.URL.Query().Get("queued_id")
	if queuedID == "" {
		http.Error(w, "queued_id is required", http.StatusBadRequest)
		return
	}
	parent, err := s.db.GetConversationByID(r.Context(), parentID)
	if err != nil {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if _, err := s.getOrCreateConversationManager(r.Context(), parentID, r.Header.Get("X-ExeDev-Email")); err != nil {
		s.logger.Error("Failed to initialize transcription parent for retry", "parent", parentID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	cwd := derefString(parent.Cwd)
	childSlug, childOptions := transcriptionChildOptions()
	s.transcriptionMu.Lock()
	updatedParent, _, queued, err := s.db.RetryQueuedTranscription(r.Context(), parentID, queuedID, childSlug, &cwd, childOptions)
	s.transcriptionMu.Unlock()
	switch {
	case errors.Is(err, db.ErrQueuedMessageNotFound):
		http.Error(w, "Queued message not found", http.StatusNotFound)
		return
	case errors.Is(err, db.ErrQueuedMessageNotRetryable):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		s.logger.Error("Failed to retry queued transcription", "parent", parentID, "queued_id", queuedID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// The failed in-memory blocker remains in the same FIFO position; only its
	// durable state and child identity changed.
	go s.notifySubscribers(context.Background(), parentID)
	go s.publishConversationListUpdate(ConversationListUpdate{Type: "update", Conversation: updatedParent})
	s.launchQueuedTranscription(parentID, queued)
	writeQueuedTranscription(w)
}

func (s *Server) recoverQueuedTranscriptions(ctx context.Context) {
	conversations, err := s.db.ListConversationsWithQueuedTranscriptions(ctx)
	if err != nil {
		s.logger.Error("Failed to scan queued transcriptions", "error", err)
		return
	}
	for _, conversation := range conversations {
		queuedMessages, err := db.ParseQueuedMessagesStrict(conversation.QueuedMessages)
		if err != nil {
			s.logger.Error("Failed to parse queued transcriptions during recovery", "conversationID", conversation.ConversationID, "error", err)
			continue
		}
		for _, queued := range queuedMessages {
			if queued.Kind != db.QueuedMessageKindTranscription || queued.Transcription == nil {
				continue
			}
			switch queued.State {
			case db.QueuedMessageStateReady:
				messages, err := readyTranscriptionMessages(queued)
				if err != nil {
					s.logger.Error("Failed to restore ready transcription", "conversationID", conversation.ConversationID, "queued_id", queued.ID, "error", err)
					continue
				}
				manager, err := s.getOrCreateConversationManager(ctx, conversation.ConversationID, "")
				if err != nil {
					s.logger.Error("Failed to restore transcription parent", "conversationID", conversation.ConversationID, "error", err)
					continue
				}
				manager.ResolveQueuedTranscription(s, queued.ID, messages, queued.Model, queued.UserEmail)
			case db.QueuedMessageStateWorking:
				childID := queued.Transcription.ChildConversationID
				if _, done, _ := s.completedTranscriptionChild(ctx, childID); done {
					s.settleQueuedTranscription(conversation.ConversationID, queued)
					continue
				}
				s.launchQueuedTranscription(conversation.ConversationID, queued)
			}
		}
	}
}
