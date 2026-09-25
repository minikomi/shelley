package server

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/models"
	"shelley.exe.dev/transcription"
)

func createTestTranscriptionModel(t *testing.T, database *db.DB, id string, prompted, timecoded bool) {
	t.Helper()
	_, err := database.CreateTranscriptionModel(t.Context(), generated.CreateTranscriptionModelParams{
		ModelID: id, DisplayName: id, Protocol: "openai", Provider: "openai",
		Endpoint: "https://api.example/v1/audio/transcriptions", ApiKey: id + "-secret", ModelName: id,
		SupportsPrompted: prompted, SupportsTimecodes: timecoded, Source: "custom",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptionModelRefsPinEffectiveProfilesAndAcceptLegacyRefs(t *testing.T) {
	model := transcription.Model{
		ID: "standard", Protocol: transcription.ProtocolOpenAI, APIProfile: transcription.APIProfileAuto,
		Provider: "openai", Endpoint: "https://example.test/v1/audio/transcriptions",
		Model: "gpt-4o-mini-transcribe", SupportsPrompted: true,
	}
	ref := transcriptionModelRef(model)
	if ref.APIProfile != transcription.APIProfileOpenAIJSON {
		t.Fatalf("profile = %q", ref.APIProfile)
	}
	if !transcriptionModelMatchesRef(model, ref) {
		t.Fatal("new ref did not match unchanged model")
	}
	legacy := model.Ref()
	legacy.APIProfile = ""
	if !transcriptionModelMatchesRef(model, legacy) {
		t.Fatal("legacy ref did not survive profile introduction")
	}
	model.APIProfile = transcription.APIProfileOpenAIWhisper
	if transcriptionModelMatchesRef(model, ref) {
		t.Fatal("effective profile change did not invalidate new ref")
	}
}

func TestQueuedTranscriptionRequiresDefaultWhenSelectableModelsExist(t *testing.T) {
	server, database, _ := newTestServer(t)
	createTestTranscriptionModel(t, database, "available", true, false)
	selected, err := server.selectQueuedTranscriptionModels(t.Context(), false)
	if err == nil || !strings.Contains(err.Error(), "default is not configured") || selected != nil {
		t.Fatalf("selection = %+v, err = %v", selected, err)
	}
}

func TestQueuedTranscriptionSelectionPinsIDsAndNeverFallsBack(t *testing.T) {
	server, database, _ := newTestServer(t)
	createTestTranscriptionModel(t, database, "prompted-one", true, false)
	createTestTranscriptionModel(t, database, "prompted-two", true, false)
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTranscript, "prompted-one"); err != nil {
		t.Fatal(err)
	}
	selected, err := server.selectQueuedTranscriptionModels(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if selected[transcription.RoleTranscript].ID != "prompted-one" {
		t.Fatalf("selected = %+v", selected)
	}

	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, queued, err := database.CreateQueuedTranscription(t.Context(), parent.ConversationID, db.QueuedMessage{
		ID: "pinned", CreatedAt: time.Now().UTC(), Model: "predictable",
		Kind: db.QueuedMessageKindTranscription, State: db.QueuedMessageStateWorking,
		Transcription: &db.QueuedTranscription{MediaPath: "/tmp/pinned.webm", Models: selected},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTranscript, "prompted-two"); err != nil {
		t.Fatal(err)
	}

	provider := &transcriptionProviderFunc{
		protocol: transcription.ProtocolOpenAI,
		fn: func(request transcription.Request) (transcription.Result, error) {
			return transcription.Result{Transcript: request.Model.ID}, nil
		},
	}
	if err := server.RegisterTranscriptionProvider(provider); err != nil {
		t.Fatal(err)
	}
	result, err := server.transcribeSelected(t.Context(), queued.Transcription.MediaPath, "prompt", false, queued.Transcription.Models)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "prompted-one" || len(provider.calls) != 1 || provider.calls[0].Model.ID != "prompted-one" {
		t.Fatalf("result=%+v calls=%+v", result, provider.calls)
	}
	stored, err := database.GetTranscriptionModel(t.Context(), "prompted-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateTranscriptionModel(t.Context(), generated.UpdateTranscriptionModelParams{
		DisplayName: stored.DisplayName, Protocol: stored.Protocol, Provider: stored.Provider, Endpoint: "https://changed.example/v1/audio/transcriptions",
		ApiKey: stored.ApiKey, ModelName: stored.ModelName, SupportsPrompted: stored.SupportsPrompted,
		SupportsTimecodes: stored.SupportsTimecodes, Source: stored.Source, ModelID: stored.ModelID,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = server.transcribeSelected(t.Context(), queued.Transcription.MediaPath, "prompt", false, queued.Transcription.Models)
	var modelErr *transcription.ModelError
	if !errors.As(err, &modelErr) || modelErr.ModelID != "prompted-one" || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("changed selected model error = %v", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("changed selected model executed: calls=%+v", provider.calls)
	}

	if deleted, err := database.DeleteTranscriptionModel(t.Context(), "prompted-one"); err != nil || !deleted {
		t.Fatalf("delete = %v, %v", deleted, err)
	}
	_, err = server.transcribeSelected(t.Context(), queued.Transcription.MediaPath, "prompt", false, queued.Transcription.Models)
	modelErr = nil
	if !errors.As(err, &modelErr) || modelErr.ModelID != "prompted-one" || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("missing selected model error = %v", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("missing selected model fell back: calls=%+v", provider.calls)
	}
}

func TestLegacyQueuedSelectionIsNotReroutedWhenCatalogAppears(t *testing.T) {
	server, database, _ := newTestServer(t)
	selected, err := server.selectQueuedTranscriptionModels(t.Context(), false)
	if err != nil || selected != nil {
		t.Fatalf("legacy selection=%+v err=%v", selected, err)
	}
	createTestTranscriptionModel(t, database, "new-prompted", true, false)
	createTestTranscriptionModel(t, database, "new-timecoded", false, true)
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTranscript, "new-prompted"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTimecoded, "new-timecoded"); err != nil {
		t.Fatal(err)
	}
	completed, err := server.completeQueuedTranscriptionModels(t.Context(), selected, true)
	if err != nil || completed != nil {
		t.Fatalf("legacy selection was rerouted: %+v, %v", completed, err)
	}
}

func TestAudioSelectionIgnoresStaleTimecodedDefaultAndVideoPreflightsBothRoles(t *testing.T) {
	server, database, _ := newTestServer(t)
	createTestTranscriptionModel(t, database, "prompted", true, false)
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTranscript, "prompted"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTimecoded, "missing-timecoded"); err != nil {
		t.Fatal(err)
	}
	selected, err := server.selectQueuedTranscriptionModels(t.Context(), false)
	if err != nil || len(selected) != 1 || selected[transcription.RoleTranscript].ID != "prompted" {
		t.Fatalf("audio selection=%+v err=%v", selected, err)
	}
	if _, err := server.completeQueuedTranscriptionModels(t.Context(), selected, true); err == nil || !strings.Contains(err.Error(), "missing-timecoded") {
		t.Fatalf("video completion error = %v", err)
	}
	provider := &transcriptionProviderFunc{
		protocol: transcription.ProtocolOpenAI,
		fn: func(transcription.Request) (transcription.Result, error) {
			return transcription.Result{Transcript: "must not run"}, nil
		},
	}
	if err := server.RegisterTranscriptionProvider(provider); err != nil {
		t.Fatal(err)
	}
	if _, err := server.transcribeSelected(t.Context(), "/tmp/video.webm", "prompt", true, selected); err == nil || !strings.Contains(err.Error(), "no selected model") {
		t.Fatalf("video preflight error = %v", err)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("provider ran before video roles were resolved: %+v", provider.calls)
	}
}

func TestInitializeTranscriptionModelDefaultsPersistsLegacyEquivalentChoices(t *testing.T) {
	server, database, _ := newTestServer(t)
	server.llmManager = &transcriptionCatalogTestManager{
		testLLMManager: server.llmManager.(*testLLMManager),
		managed: []models.TranscriptionModel{
			{ID: "team-gpt", DisplayName: "Team GPT", Protocol: transcription.ProtocolOpenAI, Provider: "openai", Endpoint: "https://llm.team.example/v1/audio/transcriptions", Model: openAITranscriptionModel, SupportsPrompted: true, Managed: true, Source: "llm.team.example"},
			{ID: "personal-gpt", DisplayName: "Personal GPT", Protocol: transcription.ProtocolOpenAI, Provider: "openai", Endpoint: "https://llm.int.example/v1/audio/transcriptions", Model: openAITranscriptionModel, SupportsPrompted: true, Managed: true, Source: "llm.int.example"},
			{ID: "personal-whisper", DisplayName: "Personal Whisper", Protocol: transcription.ProtocolOpenAI, Provider: "openai", Endpoint: "https://llm.int.example/v1/audio/transcriptions", Model: openAITimestampedTranscriptionModel, SupportsTimecodes: true, Managed: true, Source: "llm.int.example"},
		},
	}
	if err := server.InitializeTranscriptionModelDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := database.ListTranscriptionModelDefaults(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row.Role] = row.ModelID
	}
	if got[string(transcription.RoleTranscript)] != "personal-gpt" || got[string(transcription.RoleTimecoded)] != "personal-whisper" {
		t.Fatalf("initialized defaults = %+v", got)
	}
	selected, err := server.selectQueuedTranscriptionModels(t.Context(), true)
	if err != nil || selected[transcription.RoleTranscript].ID != "personal-gpt" || selected[transcription.RoleTimecoded].ID != "personal-whisper" {
		t.Fatalf("selection=%+v err=%v", selected, err)
	}
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTranscript, "team-gpt"); err != nil {
		t.Fatal(err)
	}
	if err := server.InitializeTranscriptionModelDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err = database.ListTranscriptionModelDefaults(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]string{}
	for _, row := range rows {
		got[row.Role] = row.ModelID
	}
	if got[string(transcription.RoleTranscript)] != "team-gpt" {
		t.Fatalf("explicit default was overwritten: %+v", got)
	}
}

func TestSelectedTranscriptionUsesIndependentRolesAndProviderNeutralAudit(t *testing.T) {
	server, database, _ := newTestServer(t)
	createTestTranscriptionModel(t, database, "prompted", true, false)
	createTestTranscriptionModel(t, database, "timecoded", false, true)
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTranscript, "prompted"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTimecoded, "timecoded"); err != nil {
		t.Fatal(err)
	}
	selected, err := server.selectQueuedTranscriptionModels(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	provider := &transcriptionProviderFunc{
		protocol: transcription.ProtocolOpenAI,
		fn: func(request transcription.Request) (transcription.Result, error) {
			if request.Role == transcription.RoleTranscript {
				return transcription.Result{Transcript: "spoken words"}, nil
			}
			return transcription.Result{Timecodes: json.RawMessage(`{"words":[]}`)}, nil
		},
	}
	if err := server.RegisterTranscriptionProvider(provider); err != nil {
		t.Fatal(err)
	}
	mediaPath := t.TempDir() + "/recording.webm"
	if err := os.WriteFile(mediaPath, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := server.transcribeSelected(t.Context(), mediaPath, "context", true, selected)
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "prompted" || result.TimestampsModel != "timecoded" || result.TimestampsPath == "" {
		t.Fatalf("result = %+v", result)
	}
	if len(provider.calls) != 2 || provider.calls[0].Role != transcription.RoleTranscript || provider.calls[1].Role != transcription.RoleTimecoded {
		t.Fatalf("calls = %+v", provider.calls)
	}

	_, toolUse, err := transcriptionToolUse(mediaPath, "context", true, selected)
	if err != nil {
		t.Fatal(err)
	}
	if toolUse.Content[0].ToolName != "audio_transcription" || strings.Contains(string(toolUse.Content[0].ToolInput), "secret") {
		t.Fatalf("tool use = %+v", toolUse.Content[0])
	}
	var input struct {
		Models map[transcription.Role]transcription.ModelRef `json:"models"`
	}
	if err := json.Unmarshal(toolUse.Content[0].ToolInput, &input); err != nil {
		t.Fatal(err)
	}
	if input.Models[transcription.RoleTranscript].ID != "prompted" || input.Models[transcription.RoleTimecoded].ID != "timecoded" {
		t.Fatalf("audit models = %+v", input.Models)
	}
}
