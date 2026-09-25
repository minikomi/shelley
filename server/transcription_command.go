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
	"unicode/utf8"

	"github.com/google/uuid"

	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/transcription"
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
	parentID  string
	attemptID string
	cancel    context.CancelFunc
	done      chan struct{}
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
	selectedModels, err := s.selectQueuedTranscriptionModels(ctx, false)
	if err != nil {
		var configurationError *transcriptionSelectionConfigurationError
		if errors.As(err, &configurationError) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		s.logger.Error("Failed to resolve transcription defaults", "conversationID", manager.conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	userData, err := marshalTurnUserData(ctx)
	if err != nil {
		s.logger.Error("Failed to marshal transcription user data", "conversationID", manager.conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	queued := db.QueuedMessage{
		ID:        uuid.NewString(),
		CreatedAt: time.Now().UTC(),
		Model:     modelID,
		UserEmail: userEmailFromContext(ctx),
		UserData:  userData,
		Kind:      db.QueuedMessageKindTranscription,
		State:     db.QueuedMessageStateWorking,
		Transcription: &db.QueuedTranscription{
			MediaPath: mediaPath,
			Context:   strings.TrimSpace(transcriptionContext),
			Models:    selectedModels,
		},
	}
	queued, err = manager.QueueTranscription(ctx, s, queued)
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

func (s *Server) launchQueuedTranscription(parentID string, queued db.QueuedMessage) {
	if queued.Transcription == nil {
		s.logger.Error("Cannot launch invalid queued transcription", "parent", parentID, "queued_id", queued.ID)
		return
	}

	s.transcriptionMu.Lock()
	if _, ok := s.transcriptionJobs[queued.ID]; ok {
		s.transcriptionMu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), transcriptionTimeout)
	done := make(chan struct{})
	attemptID := uuid.NewString()
	s.transcriptionJobs[queued.ID] = transcriptionJob{parentID: parentID, attemptID: attemptID, cancel: cancel, done: done}
	s.transcriptionMu.Unlock()

	go func() {
		defer cancel()
		defer close(done)
		defer func() {
			s.transcriptionMu.Lock()
			if current, ok := s.transcriptionJobs[queued.ID]; ok && current.attemptID == attemptID {
				delete(s.transcriptionJobs, queued.ID)
			}
			s.transcriptionMu.Unlock()
		}()
		s.runQueuedTranscription(ctx, parentID, queued, attemptID)
	}()
}

func validateCurrentQueuedTranscription(queued *db.QueuedMessage) error {
	if queued.Kind != db.QueuedMessageKindTranscription ||
		queued.State != db.QueuedMessageStateWorking ||
		queued.Transcription == nil {
		return errQueuedTranscriptionSuperseded
	}
	return nil
}

func (s *Server) updateCurrentQueuedTranscription(ctx context.Context, parentID, queuedID, attemptID string, update func(*db.QueuedMessage)) (db.QueuedMessage, error) {
	s.transcriptionMu.Lock()
	if current, ok := s.transcriptionJobs[queuedID]; !ok || current.attemptID != attemptID {
		s.transcriptionMu.Unlock()
		return db.QueuedMessage{}, errQueuedTranscriptionSuperseded
	}
	conv, queued, err := s.db.UpdateQueuedMessage(ctx, parentID, queuedID, func(current *db.QueuedMessage) error {
		if err := validateCurrentQueuedTranscription(current); err != nil {
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

func (s *Server) runQueuedTranscription(ctx context.Context, parentID string, queued db.QueuedMessage, attemptID string) {
	mediaPath := queued.Transcription.MediaPath
	if !s.queuedTranscriptionIsCurrent(ctx, parentID, queued, attemptID) {
		return
	}

	media, err := probeTranscriptionMedia(ctx, mediaPath, s.mediaRun)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, attemptID, nil, fmt.Errorf("inspect recording: %w", err))
		return
	}
	selectedModels, err := s.completeQueuedTranscriptionModels(ctx, queued.Transcription.Models, media.HasVideo)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, attemptID, nil, err)
		return
	}
	if !sameTranscriptionModelRefs(selectedModels, queued.Transcription.Models) {
		updated, err := s.updateCurrentQueuedTranscription(context.Background(), parentID, queued.ID, attemptID, func(current *db.QueuedMessage) {
			current.Transcription.Models = selectedModels
		})
		if err != nil {
			s.logQueuedTranscriptionError("Failed to persist transcription model selection", parentID, queued.ID, err)
			return
		}
		queued = updated
	}
	if media.HasVideo && queued.Transcription.ContactSheetPath == "" {
		contactSheetPath, err := createVideoContactSheet(ctx, mediaPath, media, s.mediaRun)
		if err != nil {
			s.failQueuedTranscription(parentID, queued.ID, attemptID, nil, fmt.Errorf("create contact sheet: %w", err))
			return
		}
		// Persist the sheet before transcription so a restart reuses it.
		updated, err := s.updateCurrentQueuedTranscription(context.Background(), parentID, queued.ID, attemptID, func(current *db.QueuedMessage) {
			current.Transcription.ContactSheetPath = contactSheetPath
		})
		if err != nil {
			s.logQueuedTranscriptionError("Failed to persist transcription contact sheet", parentID, queued.ID, err)
			return
		}
		queued = updated
	}

	prompt, err := s.transcriptionPrompt(ctx, parentID)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, attemptID, nil, fmt.Errorf("build transcription prompt: %w", err))
		return
	}
	toolUseID, toolUse, err := transcriptionToolUse(mediaPath, prompt, media.HasVideo, queued.Transcription.Models)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, attemptID, nil, err)
		return
	}
	audit := []llm.Message{toolUse}
	auditJSON, err := json.Marshal(audit)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, attemptID, nil, err)
		return
	}
	updated, err := s.updateCurrentQueuedTranscription(context.Background(), parentID, queued.ID, attemptID, func(current *db.QueuedMessage) {
		current.Transcription.Audit = auditJSON
	})
	if err != nil {
		s.logQueuedTranscriptionError("Failed to persist transcription tool call", parentID, queued.ID, err)
		return
	}
	queued = updated

	started := time.Now()
	var result transcriptionResult
	if len(queued.Transcription.Models) == 0 {
		result, err = s.transcriber.Transcribe(ctx, mediaPath, prompt, media.HasVideo)
	} else {
		result, err = s.transcribeSelected(ctx, mediaPath, prompt, media.HasVideo, queued.Transcription.Models)
	}
	finished := time.Now()
	toolResult, auditErr := transcriptionToolResult(toolUseID, result, started, finished, err, queued.Transcription.Models)
	if auditErr != nil {
		s.failQueuedTranscription(parentID, queued.ID, attemptID, audit, auditErr)
		return
	}
	audit = append(audit, toolResult)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, attemptID, audit, err)
		return
	}
	s.finalizeQueuedTranscription(parentID, queued, attemptID, result, audit)
}

// queuedTranscriptionIsCurrent reports whether the durable item is still in
// the working state; anything else means it was cancelled or already settled.
func (s *Server) queuedTranscriptionIsCurrent(ctx context.Context, parentID string, queued db.QueuedMessage, attemptID string) bool {
	s.transcriptionMu.Lock()
	defer s.transcriptionMu.Unlock()
	job, ok := s.transcriptionJobs[queued.ID]
	if !ok || job.attemptID != attemptID {
		return false
	}
	current, err := s.db.GetQueuedMessage(ctx, parentID, queued.ID)
	return err == nil && validateCurrentQueuedTranscription(&current) == nil
}

func sameTranscriptionModelRefs(a, b map[transcription.Role]transcription.ModelRef) bool {
	if len(a) != len(b) {
		return false
	}
	for role, ref := range a {
		if b[role] != ref {
			return false
		}
	}
	return true
}

func transcriptionAuditModels(selected map[transcription.Role]transcription.ModelRef, timestamps bool) map[transcription.Role]transcription.ModelRef {
	if len(selected) == 0 {
		return nil
	}
	models := make(map[transcription.Role]transcription.ModelRef, 2)
	if model, ok := selected[transcription.RoleTranscript]; ok {
		models[transcription.RoleTranscript] = model
	}
	if timestamps {
		if model, ok := selected[transcription.RoleTimecoded]; ok {
			models[transcription.RoleTimecoded] = model
		}
	}
	return models
}

func transcriptionToolUse(mediaPath, prompt string, timestamps bool, selected map[transcription.Role]transcription.ModelRef) (string, llm.Message, error) {
	toolUseID := "transcription_" + uuid.NewString()
	toolName := "audio_transcription"
	toolInputFields := map[string]any{
		"file":         mediaPath,
		"prompt_chars": utf8.RuneCountInString(prompt),
	}
	if len(selected) == 0 {
		toolName = "openai_audio_transcription"
		toolInputFields["model"] = textTranscription.Model
		toolInputFields["response_format"] = textTranscription.ResponseFormat
		if timestamps {
			toolInputFields["timestamps_model"] = timestampTranscription.Model
			toolInputFields["timestamps_response_format"] = timestampTranscription.ResponseFormat
			toolInputFields["timestamp_granularities"] = timestampTranscription.TimestampGranularities
		}
	} else {
		toolInputFields["models"] = transcriptionAuditModels(selected, timestamps)
	}
	toolInput, err := json.Marshal(toolInputFields)
	if err != nil {
		return "", llm.Message{}, err
	}
	return toolUseID, llm.Message{
		Role:                llm.MessageRoleAssistant,
		ExcludedFromContext: true,
		Content: []llm.Content{{
			ID:        toolUseID,
			Type:      llm.ContentTypeToolUse,
			ToolName:  toolName,
			ToolInput: toolInput,
		}},
	}, nil
}

func transcriptionToolResult(toolUseID string, result transcriptionResult, started, finished time.Time, failure error, selected map[transcription.Role]transcription.ModelRef) (llm.Message, error) {
	toolOutput := map[string]any{
		"duration_ms": finished.Sub(started).Milliseconds(),
	}
	requestedTimecodes := result.TimestampsModel != ""
	if failure != nil {
		var modelError *transcription.ModelError
		if errors.As(failure, &modelError) && modelError.Role == transcription.RoleTimecoded {
			requestedTimecodes = true
		}
	}
	if len(selected) > 0 {
		toolOutput["models"] = transcriptionAuditModels(selected, requestedTimecodes)
	}
	if failure != nil {
		if len(selected) == 0 {
			var modelError *transcriptionModelError
			if errors.As(failure, &modelError) {
				toolOutput["model"] = modelError.Model
			}
		} else {
			var modelError *transcription.ModelError
			if errors.As(failure, &modelError) {
				toolOutput["failed_role"] = modelError.Role
				toolOutput["failed_model_id"] = modelError.ModelID
			}
		}
		toolOutput["error"] = queuedTranscriptionError(failure)
	} else {
		if len(selected) == 0 {
			toolOutput["model"] = result.Model
		}
		toolOutput["text"] = result.Text
		if result.TimestampsModel != "" && len(selected) == 0 {
			toolOutput["timestamps_model"] = result.TimestampsModel
		}
		if result.TimestampsPath != "" {
			toolOutput["timestamps_path"] = result.TimestampsPath
		}
	}
	outputJSON, err := json.Marshal(toolOutput)
	if err != nil {
		return llm.Message{}, err
	}
	return llm.Message{
		Role:                llm.MessageRoleUser,
		ExcludedFromContext: true,
		Content: []llm.Content{{
			Type:             llm.ContentTypeToolResult,
			ToolUseID:        toolUseID,
			ToolError:        failure != nil,
			ToolUseStartTime: &started,
			ToolUseEndTime:   &finished,
			ToolResult: []llm.Content{{
				Type: llm.ContentTypeText,
				Text: string(outputJSON),
			}},
		}},
	}, nil
}

func transcriptionParentMessage(text, mediaPath, contactSheetPath, timestampsPath, metadataPath, transcriptionContext string) llm.Message {
	parts := make([]string, 0, 7)
	if transcriptionContext = strings.TrimSpace(transcriptionContext); transcriptionContext != "" {
		parts = append(parts, transcriptionContext)
	}
	parts = append(parts, strings.TrimSpace(text))
	if contactSheetPath != "" {
		parts = append(
			parts,
			"Screen recording: ["+mediaPath+"]",
			"Contact sheet: ["+contactSheetPath+"]",
		)
	}
	if timestampsPath != "" {
		parts = append(parts, "Transcript timestamps: ["+timestampsPath+"]")
	}
	if metadataPath != "" {
		parts = append(parts, "Recording metadata: ["+metadataPath+"]")
	}
	return llm.UserStringMessage(strings.Join(parts, "\n\n"))
}

// queuedUserDataWithMessageText keeps FTS search text aligned with the final
// transcription. messages_fts reads the Text key from user_data instead of the
// message body whenever provenance metadata is present.
func queuedUserDataWithMessageText(raw json.RawMessage, text string) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if _, ok := data["sender_conversation_id"]; !ok {
		return raw, nil
	}
	data["Text"] = text
	return json.Marshal(data)
}

func (s *Server) finalizeQueuedTranscription(parentID string, queued db.QueuedMessage, attemptID string, result transcriptionResult, audit []llm.Message) {
	var metadataPath string
	if result.TimestampsPath != "" {
		metadataPath = queued.Transcription.MediaPath + ".json"
		if info, err := os.Stat(metadataPath); err != nil || !info.Mode().IsRegular() {
			metadataPath = ""
		}
	}
	message := transcriptionParentMessage(
		result.Text,
		queued.Transcription.MediaPath,
		queued.Transcription.ContactSheetPath,
		result.TimestampsPath,
		metadataPath,
		queued.Transcription.Context,
	)
	llmJSON, err := json.Marshal(message)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, attemptID, audit, err)
		return
	}
	auditJSON, err := json.Marshal(audit)
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, attemptID, audit, err)
		return
	}
	userData, err := queuedUserDataWithMessageText(queued.UserData, messageText(message))
	if err != nil {
		s.failQueuedTranscription(parentID, queued.ID, attemptID, audit, fmt.Errorf("update transcription user data: %w", err))
		return
	}
	ready, err := s.updateCurrentQueuedTranscription(context.Background(), parentID, queued.ID, attemptID, func(current *db.QueuedMessage) {
		current.State = db.QueuedMessageStateReady
		current.Llm = llmJSON
		current.Transcription.Audit = auditJSON
		current.UserData = userData
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
	manager.ResolveQueuedTranscription(s, ready.ID, messages, ready.Model, ready.UserEmail, ready.UserData)
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

func (s *Server) failQueuedTranscription(parentID, queuedID, attemptID string, audit []llm.Message, failure error) {
	var auditJSON json.RawMessage
	if len(audit) > 0 {
		var err error
		auditJSON, err = json.Marshal(audit)
		if err != nil {
			s.logger.Error("Failed to marshal transcription audit", "parent", parentID, "queued_id", queuedID, "error", err)
		}
	}
	_, err := s.updateCurrentQueuedTranscription(context.Background(), parentID, queuedID, attemptID, func(current *db.QueuedMessage) {
		current.State = db.QueuedMessageStateFailed
		current.Error = queuedTranscriptionError(failure)
		current.Transcription.Audit = auditJSON
	})
	if err != nil {
		s.logQueuedTranscriptionError("Failed to mark queued transcription failed", parentID, queuedID, err)
	}
}

// cancelQueuedTranscriptions cancels the detached workers of the matching
// queue items (every item of the parent when queuedID is empty), runs persist
// under the same mutex so no worker state transition can interleave with the
// removal.
func (s *Server) cancelQueuedTranscriptions(ctx context.Context, parentID, queuedID string, persist func() error) error {
	s.transcriptionMu.Lock()
	for id, job := range s.transcriptionJobs {
		if job.parentID == parentID && (queuedID == "" || id == queuedID) {
			job.cancel()
			delete(s.transcriptionJobs, id)
		}
	}
	err := persist()
	s.transcriptionMu.Unlock()
	return err
}

func (s *Server) handleRetryQueued(w http.ResponseWriter, r *http.Request, parentID string) {
	queuedID := r.URL.Query().Get("queued_id")
	if queuedID == "" {
		http.Error(w, "queued_id is required", http.StatusBadRequest)
		return
	}
	if _, err := s.getOrCreateConversationManager(r.Context(), parentID, r.Header.Get("X-ExeDev-Email")); err != nil {
		s.logger.Error("Failed to initialize transcription parent for retry", "parent", parentID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	s.transcriptionMu.Lock()
	updatedParent, queued, err := s.db.RetryQueuedTranscription(r.Context(), parentID, queuedID)
	if err != nil {
		s.transcriptionMu.Unlock()
		switch {
		case errors.Is(err, db.ErrQueuedMessageNotFound):
			http.Error(w, "Queued message not found", http.StatusNotFound)
		case errors.Is(err, db.ErrQueuedMessageNotRetryable):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			s.logger.Error("Failed to retry queued transcription", "parent", parentID, "queued_id", queuedID, "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}
	if job, ok := s.transcriptionJobs[queuedID]; ok {
		job.cancel()
		delete(s.transcriptionJobs, queuedID)
	}
	s.transcriptionMu.Unlock()

	// The failed in-memory blocker remains in the same FIFO position; only its
	// durable state changed.
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
				manager.ResolveQueuedTranscription(s, queued.ID, messages, queued.Model, queued.UserEmail, queued.UserData)
			case db.QueuedMessageStateWorking:
				s.launchQueuedTranscription(conversation.ConversationID, queued)
			}
		}
	}
}
