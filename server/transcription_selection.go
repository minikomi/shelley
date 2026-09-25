package server

import (
	"context"
	"fmt"
	"os"
	"strings"

	"shelley.exe.dev/transcription"
)

type transcriptionSelectionConfigurationError struct{ err error }

func (e *transcriptionSelectionConfigurationError) Error() string { return e.err.Error() }
func (e *transcriptionSelectionConfigurationError) Unwrap() error { return e.err }

func transcriptionSelectionConfigurationErrorf(format string, args ...any) error {
	return &transcriptionSelectionConfigurationError{err: fmt.Errorf(format, args...)}
}

// selectQueuedTranscriptionModels snapshots only the roles known to be required
// at selection time. Audio selection never consults the timecoded default.
func (s *Server) selectQueuedTranscriptionModels(ctx context.Context, requiresTimecodes bool) (map[transcription.Role]transcription.ModelRef, error) {
	defaults, err := s.db.ListTranscriptionModelDefaults(ctx)
	if err != nil {
		return nil, err
	}
	catalog, err := s.transcriptionCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if len(defaults) == 0 && len(catalog) == 0 {
		return nil, nil
	}
	configured := make(map[transcription.Role]string, len(defaults))
	for _, item := range defaults {
		role := transcription.Role(item.Role)
		if err := transcription.ValidateRole(role); err != nil {
			return nil, err
		}
		configured[role] = item.ModelID
	}
	required := []transcription.Role{transcription.RoleTranscript}
	if requiresTimecodes {
		required = append(required, transcription.RoleTimecoded)
	}
	selected := make(map[transcription.Role]transcription.ModelRef, len(required))
	for _, role := range required {
		modelID := configured[role]
		if modelID == "" {
			return nil, transcriptionSelectionConfigurationErrorf("%s transcription default is not configured", role)
		}
		model, err := findTranscriptionModel(catalog, modelID)
		if err != nil {
			return nil, transcriptionSelectionConfigurationErrorf("configured %s default: %v", role, err)
		}
		if !model.Supports(role) {
			return nil, transcriptionSelectionConfigurationErrorf("configured %s default model %q no longer supports that role", role, model.ID)
		}
		selected[role] = transcriptionModelRef(model)
	}
	return selected, nil
}

// completeQueuedTranscriptionModels adds a timecoded selection only after media
// probing establishes that video requires it. Existing refs are never replaced.
func (s *Server) completeQueuedTranscriptionModels(ctx context.Context, selected map[transcription.Role]transcription.ModelRef, requiresTimecodes bool) (map[transcription.Role]transcription.ModelRef, error) {
	if len(selected) == 0 {
		return nil, nil
	}
	completed := make(map[transcription.Role]transcription.ModelRef, len(selected)+1)
	for role, ref := range selected {
		completed[role] = ref
	}
	if _, err := s.resolveQueuedTranscriptionModel(ctx, transcription.RoleTranscript, completed); err != nil {
		return nil, err
	}
	if !requiresTimecodes {
		return completed, nil
	}
	if _, ok := completed[transcription.RoleTimecoded]; ok {
		return completed, nil
	}
	defaults, err := s.db.ListTranscriptionModelDefaults(ctx)
	if err != nil {
		return nil, err
	}
	var modelID string
	for _, item := range defaults {
		if transcription.Role(item.Role) == transcription.RoleTimecoded {
			modelID = item.ModelID
			break
		}
	}
	if modelID == "" {
		return nil, transcriptionSelectionConfigurationErrorf("timecoded transcription default is not configured")
	}
	catalog, err := s.transcriptionCatalog(ctx)
	if err != nil {
		return nil, err
	}
	model, err := findTranscriptionModel(catalog, modelID)
	if err != nil {
		return nil, transcriptionSelectionConfigurationErrorf("configured timecoded default: %v", err)
	}
	if !model.Supports(transcription.RoleTimecoded) {
		return nil, transcriptionSelectionConfigurationErrorf("configured timecoded default model %q no longer supports that role", model.ID)
	}
	completed[transcription.RoleTimecoded] = transcriptionModelRef(model)
	return completed, nil
}

func (s *Server) resolveQueuedTranscriptionModel(ctx context.Context, role transcription.Role, selected map[transcription.Role]transcription.ModelRef) (transcription.Model, error) {
	ref, ok := selected[role]
	if !ok {
		return transcription.Model{}, &transcription.ModelError{
			Role: role, Err: fmt.Errorf("no selected model was persisted for this role"),
		}
	}
	catalog, err := s.transcriptionCatalog(ctx)
	if err != nil {
		return transcription.Model{}, &transcription.ModelError{Role: role, ModelID: ref.ID, Err: err}
	}
	model, err := findTranscriptionModel(catalog, ref.ID)
	if err != nil {
		return transcription.Model{}, &transcription.ModelError{Role: role, ModelID: ref.ID, Err: err}
	}
	if !model.Supports(role) {
		return transcription.Model{}, &transcription.ModelError{
			Role: role, ModelID: ref.ID, Err: fmt.Errorf("selected model no longer supports role %q", role),
		}
	}
	if !transcriptionModelMatchesRef(model, ref) {
		return transcription.Model{}, &transcription.ModelError{
			Role: role, ModelID: ref.ID, Err: fmt.Errorf("selected model identity changed after it was queued"),
		}
	}
	return model, nil
}

func transcriptionModelRef(model transcription.Model) transcription.ModelRef {
	ref := model.Ref()
	if model.Protocol == transcription.ProtocolOpenAI && ref.APIProfile == transcription.APIProfileAuto {
		ref.APIProfile = transcription.InferOpenAIProfile(model.Model)
	}
	if model.Protocol == transcription.ProtocolOpenAI &&
		(ref.RequestEncoding == "" || ref.RequestEncoding == transcription.RequestEncodingAuto) {
		ref.RequestEncoding, _ = openAITranscriptionRequestEncoding(ref.RequestEncoding, model.Model)
	}
	return ref
}

func transcriptionModelMatchesRef(model transcription.Model, ref transcription.ModelRef) bool {
	current := transcriptionModelRef(model)
	if ref.APIProfile == "" {
		current.APIProfile = ""
	}
	if ref.RequestEncoding == "" {
		current.RequestEncoding = ""
	}
	return current == ref
}

func (s *Server) transcribeSelected(ctx context.Context, mediaPath, prompt string, timestamps bool, selected map[transcription.Role]transcription.ModelRef) (transcriptionResult, error) {
	transcriptModel, err := s.resolveQueuedTranscriptionModel(ctx, transcription.RoleTranscript, selected)
	if err != nil {
		return transcriptionResult{}, err
	}
	var timecodedModel transcription.Model
	if timestamps {
		timecodedModel, err = s.resolveQueuedTranscriptionModel(ctx, transcription.RoleTimecoded, selected)
		if err != nil {
			return transcriptionResult{}, err
		}
	}
	transcript, err := s.runTranscriptionProvider(ctx, transcription.Request{
		Model: transcriptModel, Role: transcription.RoleTranscript, RecordingPath: mediaPath, Prompt: prompt,
	})
	if err != nil {
		return transcriptionResult{}, err
	}
	result := transcriptionResult{Text: strings.TrimSpace(transcript.Transcript), Model: transcriptModel.Model}
	if !timestamps {
		return result, nil
	}

	timecoded, err := s.runTranscriptionProvider(ctx, transcription.Request{
		Model: timecodedModel, Role: transcription.RoleTimecoded, RecordingPath: mediaPath,
	})
	if err != nil {
		return transcriptionResult{}, err
	}
	timestampsPath := mediaPath + ".timestamps.json"
	if err := os.WriteFile(timestampsPath, timecoded.Timecodes, 0o600); err != nil {
		return transcriptionResult{}, &transcription.ModelError{
			Role: transcription.RoleTimecoded, ModelID: timecodedModel.ID,
			Err: fmt.Errorf("write transcription timestamps: %w", err),
		}
	}
	result.TimestampsModel = timecodedModel.Model
	result.TimestampsPath = timestampsPath
	return result, nil
}
