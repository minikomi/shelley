package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/models"
	"shelley.exe.dev/transcription"
)

const maxTranscriptionModelTestUpload = 5 << 20

type managedTranscriptionCatalogProvider interface {
	GetManagedTranscriptionModels() []models.TranscriptionModel
}

// TranscriptionModelAPI is the non-secret catalog representation.
type TranscriptionModelAPI struct {
	ModelID           string                        `json:"model_id"`
	DisplayName       string                        `json:"display_name"`
	Protocol          transcription.Protocol        `json:"protocol"`
	APIProfile        transcription.APIProfile      `json:"api_profile"`
	RequestEncoding   transcription.RequestEncoding `json:"request_encoding"`
	Provider          string                        `json:"provider"`
	Endpoint          string                        `json:"endpoint"`
	ModelName         string                        `json:"model_name"`
	SupportsPrompted  bool                          `json:"supports_prompted"`
	SupportsTimecodes bool                          `json:"supports_timecodes"`
	Managed           bool                          `json:"managed"`
	Source            string                        `json:"source"`
	HasAPIKey         bool                          `json:"has_api_key"`
}

type TranscriptionDefaultAPI struct {
	ModelID   string `json:"model_id"`
	Available bool   `json:"available"`
}

type TranscriptionCatalogAPI struct {
	Models   []TranscriptionModelAPI                        `json:"models"`
	Defaults map[transcription.Role]TranscriptionDefaultAPI `json:"defaults"`
}

type CreateTranscriptionModelRequest struct {
	DisplayName       string                        `json:"display_name"`
	Protocol          transcription.Protocol        `json:"protocol"`
	APIProfile        transcription.APIProfile      `json:"api_profile"`
	RequestEncoding   transcription.RequestEncoding `json:"request_encoding"`
	Provider          string                        `json:"provider"`
	Endpoint          string                        `json:"endpoint"`
	APIKey            string                        `json:"api_key"`
	ModelName         string                        `json:"model_name"`
	SupportsPrompted  bool                          `json:"supports_prompted"`
	SupportsTimecodes bool                          `json:"supports_timecodes"`
}

type UpdateTranscriptionModelRequest struct {
	DisplayName       string                         `json:"display_name"`
	Protocol          transcription.Protocol         `json:"protocol"`
	APIProfile        *transcription.APIProfile      `json:"api_profile,omitempty"`
	RequestEncoding   *transcription.RequestEncoding `json:"request_encoding,omitempty"`
	Provider          string                         `json:"provider"`
	Endpoint          string                         `json:"endpoint"`
	APIKey            *string                        `json:"api_key,omitempty"`
	ModelName         string                         `json:"model_name"`
	SupportsPrompted  *bool                          `json:"supports_prompted,omitempty"`
	SupportsTimecodes *bool                          `json:"supports_timecodes,omitempty"`
}

type DuplicateTranscriptionModelRequest struct {
	DisplayName string `json:"display_name,omitempty"`
}

type SetTranscriptionDefaultRequest struct {
	ModelID string `json:"model_id"`
}

// DraftTranscriptionModel is submitted as the multipart "model" JSON field by
// the test-draft endpoint. ModelID is optional; when present, omitted fields
// inherit the stored model, including its secret API key.
type DraftTranscriptionModel struct {
	ModelID           string                         `json:"model_id,omitempty"`
	DisplayName       string                         `json:"display_name"`
	Protocol          transcription.Protocol         `json:"protocol"`
	APIProfile        *transcription.APIProfile      `json:"api_profile,omitempty"`
	RequestEncoding   *transcription.RequestEncoding `json:"request_encoding,omitempty"`
	Provider          string                         `json:"provider"`
	Endpoint          string                         `json:"endpoint"`
	APIKey            string                         `json:"api_key"`
	ModelName         string                         `json:"model_name"`
	SupportsPrompted  *bool                          `json:"supports_prompted,omitempty"`
	SupportsTimecodes *bool                          `json:"supports_timecodes,omitempty"`
}

type TranscriptionCapabilityTestResult struct {
	Success      bool            `json:"success"`
	Message      string          `json:"message"`
	Transcript   string          `json:"transcript,omitempty"`
	HasTimecodes bool            `json:"has_timecodes,omitempty"`
	Timecodes    json.RawMessage `json:"timecodes,omitempty"`
}

type TranscriptionModelTestAPI struct {
	Model   TranscriptionModelAPI                                    `json:"model"`
	Results map[transcription.Role]TranscriptionCapabilityTestResult `json:"results"`
}

func (s *Server) RegisterTranscriptionProvider(provider transcription.Provider) error {
	if provider == nil {
		return errors.New("transcription provider is required")
	}
	protocol := provider.Protocol()
	if err := transcription.ValidateProtocol(protocol); err != nil {
		return err
	}
	s.transcriptionProviderMu.Lock()
	defer s.transcriptionProviderMu.Unlock()
	if _, exists := s.transcriptionProviders[protocol]; exists {
		return fmt.Errorf("transcription provider %q is already registered", protocol)
	}
	s.transcriptionProviders[protocol] = provider
	return nil
}

func (s *Server) runTranscriptionProvider(ctx context.Context, request transcription.Request) (transcription.Result, error) {
	if !request.Model.Supports(request.Role) {
		return transcription.Result{}, &transcription.ModelError{
			Role: request.Role, ModelID: request.Model.ID,
			Err: fmt.Errorf("model does not support role %q", request.Role),
		}
	}
	s.transcriptionProviderMu.RLock()
	provider := s.transcriptionProviders[request.Model.Protocol]
	s.transcriptionProviderMu.RUnlock()
	if provider == nil {
		return transcription.Result{}, &transcription.ModelError{
			Role: request.Role, ModelID: request.Model.ID,
			Err: fmt.Errorf("protocol %q has no registered provider", request.Model.Protocol),
		}
	}
	if request.Role != transcription.RoleTranscript || !request.Model.SupportsPrompted {
		request.Prompt = ""
	}
	result, err := provider.Transcribe(ctx, request)
	if err != nil {
		safeErr := redactTranscriptionProviderError(err, request.Model)
		if !errors.Is(safeErr, context.Canceled) && !errors.Is(safeErr, context.DeadlineExceeded) {
			s.logger.Error("Transcription provider request failed",
				"protocol", request.Model.Protocol,
				"model_id", request.Model.ID,
				"role", request.Role,
				"error", safeErr,
			)
		}
		return transcription.Result{}, &transcription.ModelError{Role: request.Role, ModelID: request.Model.ID, Err: safeErr}
	}
	if err := transcription.ValidateResult(request.Role, result); err != nil {
		return transcription.Result{}, &transcription.ModelError{Role: request.Role, ModelID: request.Model.ID, Err: err}
	}
	return result, nil
}

func redactTranscriptionProviderError(err error, model transcription.Model) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	message := err.Error()
	if secret := model.APIKey; secret != "" && secret != "implicit" {
		for _, decode := range []func(string) (string, error){url.QueryUnescape, url.PathUnescape} {
			if decoded, decodeErr := decode(message); decodeErr == nil && strings.Contains(decoded, secret) {
				message = decoded
			}
		}
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return errors.New(message)
}

func transcriptionModelFromDB(model generated.TranscriptionModel) transcription.Model {
	return transcription.Model{
		ID:                model.ModelID,
		DisplayName:       model.DisplayName,
		Protocol:          transcription.Protocol(model.Protocol),
		APIProfile:        transcription.APIProfile(model.ApiProfile),
		RequestEncoding:   transcription.RequestEncoding(model.RequestEncoding),
		Provider:          model.Provider,
		Endpoint:          model.Endpoint,
		APIKey:            model.ApiKey,
		Model:             model.ModelName,
		SupportsPrompted:  model.SupportsPrompted,
		SupportsTimecodes: model.SupportsTimecodes,
		Managed:           model.Managed,
		Source:            model.Source,
	}
}

func toTranscriptionModelAPI(model transcription.Model) TranscriptionModelAPI {
	return TranscriptionModelAPI{
		ModelID:           model.ID,
		DisplayName:       model.DisplayName,
		Protocol:          model.Protocol,
		APIProfile:        model.APIProfile,
		RequestEncoding:   model.RequestEncoding,
		Provider:          model.Provider,
		Endpoint:          model.Endpoint,
		ModelName:         model.Model,
		SupportsPrompted:  model.SupportsPrompted,
		SupportsTimecodes: model.SupportsTimecodes,
		Managed:           model.Managed,
		Source:            model.Source,
		HasAPIKey:         model.APIKey != "" && model.APIKey != "implicit",
	}
}

func normalizeTranscriptionModel(model transcription.Model) (transcription.Model, error) {
	model.ID = strings.TrimSpace(model.ID)
	model.DisplayName = strings.TrimSpace(model.DisplayName)
	model.Provider = strings.TrimSpace(model.Provider)
	model.Endpoint = strings.TrimSpace(model.Endpoint)
	model.Model = strings.TrimSpace(model.Model)
	if err := transcription.ValidateProtocol(model.Protocol); err != nil {
		return transcription.Model{}, err
	}
	profile, err := transcription.NormalizeAPIProfile(model.APIProfile, model.Protocol)
	if err != nil {
		return transcription.Model{}, err
	}
	model.APIProfile = profile
	encoding, err := transcription.NormalizeRequestEncoding(model.RequestEncoding, model.Protocol)
	if err != nil {
		return transcription.Model{}, err
	}
	model.RequestEncoding = encoding
	if model.Provider == "" {
		model.Provider = string(model.Protocol)
	}
	if model.DisplayName == "" || model.Provider == "" || model.Endpoint == "" || model.Model == "" {
		return transcription.Model{}, errors.New("display_name, protocol, provider, endpoint, and model_name are required")
	}
	if model.Protocol == transcription.ProtocolDeepgram && model.SupportsPrompted {
		return transcription.Model{}, errors.New("Deepgram does not support free-form context prompts")
	}
	effectiveProfile := model.APIProfile
	if effectiveProfile == transcription.APIProfileAuto && model.Protocol == transcription.ProtocolOpenAI {
		effectiveProfile = transcription.InferOpenAIProfile(model.Model)
	}
	if model.Protocol == transcription.ProtocolOpenAI {
		encoding, err := openAITranscriptionRequestEncoding(model.RequestEncoding, model.Model)
		if err != nil {
			return transcription.Model{}, err
		}
		if encoding == transcription.RequestEncodingBase64JSON && model.SupportsPrompted {
			return transcription.Model{}, errors.New("base64 JSON transcription does not support context prompts")
		}
	}
	switch effectiveProfile {
	case transcription.APIProfileOpenAIJSON:
		if model.SupportsTimecodes {
			return transcription.Model{}, errors.New("openai-json API profile does not support timecodes")
		}
	case transcription.APIProfileOpenAIDiarized:
		if model.SupportsPrompted {
			return transcription.Model{}, errors.New("openai-diarized API profile does not support context prompts")
		}
	}
	parsed, err := url.Parse(model.Endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return transcription.Model{}, errors.New("endpoint must be an absolute http or https URL")
	}
	if parsed.User != nil {
		return transcription.Model{}, errors.New("endpoint must not contain credentials")
	}
	if parsed.RawQuery != "" {
		return transcription.Model{}, errors.New("endpoint must not contain query parameters; configure credentials with api_key")
	}
	if parsed.Fragment != "" {
		return transcription.Model{}, errors.New("endpoint must not contain a fragment")
	}
	if model.Protocol == transcription.ProtocolDeepgram && strings.EqualFold(parsed.Hostname(), "api.deepgram.com") && (parsed.Path == "" || parsed.Path == "/") {
		parsed.Path = "/v1/listen"
		model.Endpoint = parsed.String()
	}
	if model.Protocol == transcription.ProtocolDeepgram && !model.Managed && (strings.TrimSpace(model.APIKey) == "" || model.APIKey == "implicit") {
		return transcription.Model{}, errors.New("custom Deepgram transcription models require an API key")
	}
	return model, nil
}

func (s *Server) transcriptionCatalog(ctx context.Context) ([]transcription.Model, error) {
	var catalog []transcription.Model
	if provider, ok := s.llmManager.(managedTranscriptionCatalogProvider); ok {
		for _, model := range provider.GetManagedTranscriptionModels() {
			catalog = append(catalog, model)
		}
	}
	rows, err := s.db.ListTranscriptionModels(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		catalog = append(catalog, transcriptionModelFromDB(row))
	}
	seen := make(map[string]bool, len(catalog))
	for i := range catalog {
		normalized, err := normalizeTranscriptionModel(catalog[i])
		if err != nil {
			return nil, fmt.Errorf("invalid transcription model %q: %w", catalog[i].ID, err)
		}
		catalog[i] = normalized
		if catalog[i].ID == "" {
			return nil, errors.New("transcription catalog contains an empty model id")
		}
		if seen[catalog[i].ID] {
			return nil, fmt.Errorf("duplicate transcription model id %q", catalog[i].ID)
		}
		seen[catalog[i].ID] = true
	}
	return catalog, nil
}

func findTranscriptionModel(catalog []transcription.Model, modelID string) (transcription.Model, error) {
	for _, model := range catalog {
		if model.ID == modelID {
			return model, nil
		}
	}
	return transcription.Model{}, fmt.Errorf("transcription model %q is not available", modelID)
}

// InitializeTranscriptionModelDefaults persists the legacy OpenAI choices for
// upgraded installations. It never overwrites an explicit selection.
func (s *Server) InitializeTranscriptionModelDefaults(ctx context.Context) error {
	catalog, err := s.transcriptionCatalog(ctx)
	if err != nil {
		return err
	}
	rows, err := s.db.ListTranscriptionModelDefaults(ctx)
	if err != nil {
		return err
	}
	configured := make(map[transcription.Role]bool, len(rows))
	for _, row := range rows {
		configured[transcription.Role(row.Role)] = true
	}
	for _, item := range []struct {
		role      transcription.Role
		wireModel string
	}{
		{role: transcription.RoleTranscript, wireModel: openAITranscriptionModel},
		{role: transcription.RoleTimecoded, wireModel: openAITimestampedTranscriptionModel},
	} {
		if configured[item.role] {
			continue
		}
		model, ok := legacyEquivalentTranscriptionDefault(catalog, item.role, item.wireModel)
		if !ok {
			continue
		}
		if _, err := s.db.InitializeTranscriptionModelDefault(ctx, item.role, model.ID); err != nil {
			return fmt.Errorf("initialize %s transcription default: %w", item.role, err)
		}
	}
	return nil
}

func legacyEquivalentTranscriptionDefault(catalog []transcription.Model, role transcription.Role, wireModel string) (transcription.Model, bool) {
	var selected transcription.Model
	selectedKey := ""
	for _, model := range catalog {
		if !model.Managed || model.Protocol != transcription.ProtocolOpenAI || model.Model != wireModel || !model.Supports(role) {
			continue
		}
		priority := "1"
		if endpoint, err := url.Parse(model.Endpoint); err == nil && strings.HasPrefix(endpoint.Hostname(), "llm.int.") {
			priority = "0"
		}
		key := priority + "\x00" + model.Endpoint + "\x00" + model.ID
		if selectedKey == "" || key < selectedKey {
			selected = model
			selectedKey = key
		}
	}
	return selected, selectedKey != ""
}

func (s *Server) transcriptionDefaults(ctx context.Context, catalog []transcription.Model) (map[transcription.Role]TranscriptionDefaultAPI, error) {
	rows, err := s.db.ListTranscriptionModelDefaults(ctx)
	if err != nil {
		return nil, err
	}
	defaults := make(map[transcription.Role]TranscriptionDefaultAPI, len(rows))
	for _, row := range rows {
		role := transcription.Role(row.Role)
		if err := transcription.ValidateRole(role); err != nil {
			return nil, err
		}
		model, err := findTranscriptionModel(catalog, row.ModelID)
		defaults[role] = TranscriptionDefaultAPI{ModelID: row.ModelID, Available: err == nil && model.Supports(role)}
	}
	return defaults, nil
}

func (s *Server) handleListTranscriptionModels(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.transcriptionCatalog(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load transcription models: %v", err), http.StatusInternalServerError)
		return
	}
	defaults, err := s.transcriptionDefaults(r.Context(), catalog)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load transcription defaults: %v", err), http.StatusInternalServerError)
		return
	}
	apiModels := make([]TranscriptionModelAPI, len(catalog))
	for i, model := range catalog {
		apiModels[i] = toTranscriptionModelAPI(model)
	}
	writeJSON(w, http.StatusOK, TranscriptionCatalogAPI{Models: apiModels, Defaults: defaults})
}

func (s *Server) handleGetTranscriptionModel(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.transcriptionCatalog(r.Context())
	if err != nil {
		http.Error(w, "Failed to load transcription models", http.StatusInternalServerError)
		return
	}
	model, err := findTranscriptionModel(catalog, r.PathValue("model_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toTranscriptionModelAPI(model))
}

func (s *Server) handleCreateTranscriptionModel(w http.ResponseWriter, r *http.Request) {
	var req CreateTranscriptionModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}
	model, err := normalizeTranscriptionModel(transcription.Model{
		ID:                uuid.NewString(),
		DisplayName:       req.DisplayName,
		Protocol:          req.Protocol,
		APIProfile:        req.APIProfile,
		RequestEncoding:   req.RequestEncoding,
		Provider:          req.Provider,
		Endpoint:          req.Endpoint,
		APIKey:            req.APIKey,
		Model:             req.ModelName,
		SupportsPrompted:  req.SupportsPrompted,
		SupportsTimecodes: req.SupportsTimecodes,
		Source:            models.SourceCustomLabel,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	created, err := s.db.CreateTranscriptionModel(r.Context(), generated.CreateTranscriptionModelParams{
		ModelID: model.ID, DisplayName: model.DisplayName, Protocol: string(model.Protocol), Provider: model.Provider,
		Endpoint: model.Endpoint, ApiKey: model.APIKey, ModelName: model.Model, ApiProfile: string(model.APIProfile), RequestEncoding: string(model.RequestEncoding), SupportsPrompted: model.SupportsPrompted,
		SupportsTimecodes: model.SupportsTimecodes, Managed: false, Source: models.SourceCustomLabel,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create transcription model: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toTranscriptionModelAPI(transcriptionModelFromDB(*created)))
}

func (s *Server) handleUpdateTranscriptionModel(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model_id")
	existing, err := s.db.GetTranscriptionModel(r.Context(), modelID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Transcription model not found or is managed", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Failed to load transcription model", http.StatusInternalServerError)
		return
	}
	if existing.Managed {
		http.Error(w, "Managed transcription models cannot be updated", http.StatusConflict)
		return
	}
	var req UpdateTranscriptionModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}
	model := transcriptionModelFromDB(*existing)
	model.DisplayName = strings.TrimSpace(req.DisplayName)
	connectionChanged := req.Protocol != model.Protocol || strings.TrimSpace(req.Endpoint) != model.Endpoint
	if connectionChanged && req.APIKey == nil {
		if model.APIKey != "" && model.APIKey != "implicit" {
			http.Error(w, "api_key is required when changing protocol or endpoint", http.StatusBadRequest)
			return
		}
		model.APIKey = ""
	}
	model.Protocol = req.Protocol
	if req.APIProfile != nil {
		model.APIProfile = *req.APIProfile
	}
	if req.RequestEncoding != nil {
		model.RequestEncoding = *req.RequestEncoding
	}
	model.Provider = strings.TrimSpace(req.Provider)
	model.Endpoint = strings.TrimSpace(req.Endpoint)
	model.Model = strings.TrimSpace(req.ModelName)
	if req.APIKey != nil {
		model.APIKey = *req.APIKey
	}
	if req.SupportsPrompted != nil {
		model.SupportsPrompted = *req.SupportsPrompted
	}
	if req.SupportsTimecodes != nil {
		model.SupportsTimecodes = *req.SupportsTimecodes
	}
	model, err = normalizeTranscriptionModel(model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := s.db.UpdateTranscriptionModel(r.Context(), generated.UpdateTranscriptionModelParams{
		DisplayName: model.DisplayName, Protocol: string(model.Protocol), Provider: model.Provider, Endpoint: model.Endpoint,
		ApiKey: model.APIKey, ModelName: model.Model, ApiProfile: string(model.APIProfile), RequestEncoding: string(model.RequestEncoding), SupportsPrompted: model.SupportsPrompted,
		SupportsTimecodes: model.SupportsTimecodes, Source: models.SourceCustomLabel, ModelID: model.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Transcription model not found or is managed", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update transcription model: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toTranscriptionModelAPI(transcriptionModelFromDB(*updated)))
}

func (s *Server) handleDuplicateTranscriptionModel(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.transcriptionCatalog(r.Context())
	if err != nil {
		http.Error(w, "Failed to load transcription models", http.StatusInternalServerError)
		return
	}
	source, err := findTranscriptionModel(catalog, r.PathValue("model_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	var req DuplicateTranscriptionModelRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
			return
		}
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = source.DisplayName + " (copy)"
	}
	source.ID = uuid.NewString()
	source.DisplayName = displayName
	source.Managed = false
	source.Source = models.SourceCustomLabel
	source, err = normalizeTranscriptionModel(source)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	created, err := s.db.CreateTranscriptionModel(r.Context(), generated.CreateTranscriptionModelParams{
		ModelID: source.ID, DisplayName: source.DisplayName, Protocol: string(source.Protocol), Provider: source.Provider,
		Endpoint: source.Endpoint, ApiKey: source.APIKey, ModelName: source.Model, ApiProfile: string(source.APIProfile), RequestEncoding: string(source.RequestEncoding), SupportsPrompted: source.SupportsPrompted,
		SupportsTimecodes: source.SupportsTimecodes, Managed: source.Managed, Source: source.Source,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to duplicate transcription model: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toTranscriptionModelAPI(transcriptionModelFromDB(*created)))
}

func (s *Server) handleDeleteTranscriptionModel(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model_id")
	catalog, err := s.transcriptionCatalog(r.Context())
	if err != nil {
		http.Error(w, "Failed to load transcription models", http.StatusInternalServerError)
		return
	}
	model, err := findTranscriptionModel(catalog, modelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if model.Managed {
		http.Error(w, "Managed transcription models cannot be deleted", http.StatusConflict)
		return
	}
	deleted, err := s.db.DeleteTranscriptionModel(r.Context(), modelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete transcription model: %v", err), http.StatusInternalServerError)
		return
	}
	if !deleted {
		http.Error(w, "Transcription model not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetTranscriptionDefault(w http.ResponseWriter, r *http.Request) {
	role := transcription.Role(r.PathValue("role"))
	if err := transcription.ValidateRole(role); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	catalog, err := s.transcriptionCatalog(r.Context())
	if err != nil {
		http.Error(w, "Failed to load transcription models", http.StatusInternalServerError)
		return
	}
	defaults, err := s.transcriptionDefaults(r.Context(), catalog)
	if err != nil {
		http.Error(w, "Failed to load transcription defaults", http.StatusInternalServerError)
		return
	}
	value, ok := defaults[role]
	if !ok {
		http.Error(w, "Transcription default is not configured", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) handleSetTranscriptionDefault(w http.ResponseWriter, r *http.Request) {
	role := transcription.Role(r.PathValue("role"))
	if err := transcription.ValidateRole(role); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req SetTranscriptionDefaultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}
	catalog, err := s.transcriptionCatalog(r.Context())
	if err != nil {
		http.Error(w, "Failed to load transcription models", http.StatusInternalServerError)
		return
	}
	model, err := findTranscriptionModel(catalog, req.ModelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !model.Supports(role) {
		http.Error(w, fmt.Sprintf("transcription model %q does not support role %q", model.ID, role), http.StatusConflict)
		return
	}
	if _, err := s.db.SetTranscriptionModelDefault(r.Context(), role, model.ID); err != nil {
		http.Error(w, fmt.Sprintf("Failed to set transcription default: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, TranscriptionDefaultAPI{ModelID: model.ID, Available: true})
}

func (s *Server) handleDeleteTranscriptionDefault(w http.ResponseWriter, r *http.Request) {
	role := transcription.Role(r.PathValue("role"))
	deleted, err := s.db.DeleteTranscriptionModelDefault(r.Context(), role)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !deleted {
		http.Error(w, "Transcription default is not configured", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resolveDraftTranscriptionModel(ctx context.Context, draft DraftTranscriptionModel) (transcription.Model, error) {
	var model transcription.Model
	var stored *transcription.Model
	if draft.ModelID != "" {
		catalog, err := s.transcriptionCatalog(ctx)
		if err != nil {
			return transcription.Model{}, err
		}
		model, err = findTranscriptionModel(catalog, draft.ModelID)
		if err != nil {
			return transcription.Model{}, err
		}
		original := model
		stored = &original
	}
	if model.ID == "" {
		model.ID = "draft"
		model.Source = models.SourceCustomLabel
	}
	connectionChanged := stored != nil &&
		((draft.Protocol != "" && draft.Protocol != stored.Protocol) ||
			(draft.Endpoint != "" && strings.TrimSpace(draft.Endpoint) != stored.Endpoint))
	if connectionChanged && draft.APIKey == "" {
		if stored.Managed || (stored.APIKey != "" && stored.APIKey != "implicit") {
			return transcription.Model{}, errors.New("api_key is required when testing a stored model with a different protocol or endpoint")
		}
		model.APIKey = ""
	}
	if stored != nil && stored.Managed && connectionChanged {
		model.Managed = false
		model.Source = models.SourceCustomLabel
	}
	if draft.DisplayName != "" {
		model.DisplayName = strings.TrimSpace(draft.DisplayName)
	}
	if draft.Protocol != "" {
		model.Protocol = draft.Protocol
	}
	if draft.APIProfile != nil {
		model.APIProfile = *draft.APIProfile
	}
	if draft.RequestEncoding != nil {
		model.RequestEncoding = *draft.RequestEncoding
	}
	if draft.Provider != "" {
		model.Provider = strings.TrimSpace(draft.Provider)
	}
	if draft.Endpoint != "" {
		model.Endpoint = strings.TrimSpace(draft.Endpoint)
	}
	if draft.APIKey != "" {
		model.APIKey = draft.APIKey
	}
	if draft.ModelName != "" {
		model.Model = strings.TrimSpace(draft.ModelName)
	}
	if draft.SupportsPrompted != nil {
		model.SupportsPrompted = *draft.SupportsPrompted
	}
	if draft.SupportsTimecodes != nil {
		model.SupportsTimecodes = *draft.SupportsTimecodes
	}
	model, err := normalizeTranscriptionModel(model)
	if err != nil {
		return transcription.Model{}, err
	}
	return model, nil
}

func saveTranscriptionTestUpload(file multipart.File, filename string) (string, error) {
	ext := filepath.Ext(filepath.Base(filename))
	if len(ext) > 12 {
		ext = ""
	}
	temp, err := os.CreateTemp("", "shelley-transcription-model-test-*"+ext)
	if err != nil {
		return "", err
	}
	path := temp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(path)
		}
	}()
	written, err := io.Copy(temp, io.LimitReader(file, maxTranscriptionModelTestUpload+1))
	closeErr := temp.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written == 0 {
		return "", errors.New("recording is empty")
	}
	if written > maxTranscriptionModelTestUpload {
		return "", fmt.Errorf("recording exceeds %d MiB", maxTranscriptionModelTestUpload>>20)
	}
	success = true
	return path, nil
}

func (s *Server) handleTestDraftTranscriptionModel(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTranscriptionModelTestUpload+(1<<20))
	if err := r.ParseMultipartForm(maxTranscriptionModelTestUpload + (1 << 20)); err != nil {
		http.Error(w, fmt.Sprintf("Invalid multipart request: %v", err), http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	var draft DraftTranscriptionModel
	if err := json.Unmarshal([]byte(r.FormValue("model")), &draft); err != nil {
		http.Error(w, fmt.Sprintf("Invalid model draft: %v", err), http.StatusBadRequest)
		return
	}
	model, err := s.resolveDraftTranscriptionModel(r.Context(), draft)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	path, err := saveTranscriptionTestUpload(file, header.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer os.Remove(path)

	prompt := "Shelley transcription model connection test"
	if values, ok := r.MultipartForm.Value["prompt"]; ok {
		prompt = strings.Join(values, "\n")
	}
	results := make(map[transcription.Role]TranscriptionCapabilityTestResult, 2)
	for _, role := range []transcription.Role{transcription.RoleTranscript, transcription.RoleTimecoded} {
		if !model.Supports(role) {
			continue
		}
		result, err := s.runTranscriptionProvider(r.Context(), transcription.Request{
			Model: model, Role: role, RecordingPath: path, Prompt: prompt,
		})
		if err != nil {
			results[role] = TranscriptionCapabilityTestResult{Success: false, Message: err.Error()}
			continue
		}
		capability := TranscriptionCapabilityTestResult{Success: true, Message: "Test successful"}
		capability.Transcript = strings.TrimSpace(result.Transcript)
		if role == transcription.RoleTimecoded {
			capability.HasTimecodes = len(result.Timecodes) > 0
			capability.Timecodes = result.Timecodes
		}
		results[role] = capability
	}
	writeJSON(w, http.StatusOK, TranscriptionModelTestAPI{Model: toTranscriptionModelAPI(model), Results: results})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
