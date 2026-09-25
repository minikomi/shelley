package db

import (
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/transcription"
)

func TestTranscriptionModelsAreStoredSeparatelyFromChatModels(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()

	if _, err := database.CreateModel(t.Context(), generated.CreateModelParams{
		ModelID: "chat", DisplayName: "Chat", ProviderType: "openai", Endpoint: "https://chat.example/v1",
		ApiKey: "chat-secret", ModelName: "same-wire-name",
	}); err != nil {
		t.Fatal(err)
	}
	created, err := database.CreateTranscriptionModel(t.Context(), generated.CreateTranscriptionModelParams{
		ModelID: "transcription", DisplayName: "Transcription", Protocol: "deepgram", Provider: "deepgram",
		Endpoint: "https://api.deepgram.com/v1/listen", ApiKey: "transcription-secret", ModelName: "same-wire-name",
		SupportsTimecodes: true, Source: "custom",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ModelID != "transcription" || created.SupportsPrompted || !created.SupportsTimecodes {
		t.Fatalf("created = %+v", created)
	}
	rows, err := database.ListTranscriptionModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ModelID != "transcription" || rows[0].ApiKey != "transcription-secret" {
		t.Fatalf("transcription rows = %+v", rows)
	}
}

func TestTranscriptionModelsAllowNoOptionalCapabilities(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()

	created, err := database.CreateTranscriptionModel(t.Context(), generated.CreateTranscriptionModelParams{
		ModelID: "plain", DisplayName: "Plain", Protocol: "openai", Provider: "openai",
		Endpoint: "https://api.example/v1/audio/transcriptions", ApiKey: "secret", ModelName: "plain",
		ApiProfile: string(transcription.APIProfileOpenAIJSON), RequestEncoding: string(transcription.RequestEncodingMultipart),
		Source: "custom",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.SupportsPrompted || created.SupportsTimecodes {
		t.Fatalf("created = %+v", created)
	}
	updated, err := database.UpdateTranscriptionModel(t.Context(), generated.UpdateTranscriptionModelParams{
		DisplayName: created.DisplayName, Protocol: created.Protocol, Provider: created.Provider,
		Endpoint: created.Endpoint, ApiKey: created.ApiKey, ModelName: created.ModelName,
		ApiProfile: created.ApiProfile, RequestEncoding: created.RequestEncoding,
		Source: created.Source, ModelID: created.ModelID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.SupportsPrompted || updated.SupportsTimecodes {
		t.Fatalf("updated = %+v", updated)
	}
	rows, err := database.ListTranscriptionModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ModelID != created.ModelID || rows[0].SupportsPrompted || rows[0].SupportsTimecodes {
		t.Fatalf("rows = %+v", rows)
	}
	if deleted, err := database.DeleteTranscriptionModel(t.Context(), created.ModelID); err != nil || !deleted {
		t.Fatalf("delete both-false model = %v, %v", deleted, err)
	}
}

func TestTranscriptionModelsRejectDeepgramPromptedCapability(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	_, err := database.CreateTranscriptionModel(t.Context(), generated.CreateTranscriptionModelParams{
		ModelID: "invalid-deepgram", DisplayName: "Invalid Deepgram", Protocol: "deepgram", Provider: "deepgram",
		Endpoint: "https://api.deepgram.com/v1/listen", ApiKey: "secret", ModelName: "nova-3",
		SupportsPrompted: true, SupportsTimecodes: true, Source: "custom",
	})
	if err == nil {
		t.Fatal("Deepgram prompted capability was persisted")
	}
}

func TestTranscriptionModelAPIProfilePersistsAndNormalizes(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()

	created, err := database.CreateTranscriptionModel(t.Context(), generated.CreateTranscriptionModelParams{
		ModelID: "whisper", DisplayName: "Whisper", Protocol: "openai", Provider: "openai",
		Endpoint: "https://api.openai.com/v1/audio/transcriptions", ApiKey: "secret", ModelName: "whisper-1",
		ApiProfile: string(transcription.APIProfileOpenAIWhisper), RequestEncoding: string(transcription.RequestEncodingMultipart),
		SupportsPrompted: true, SupportsTimecodes: true, Source: "custom",
	})
	if err != nil || created.ApiProfile != string(transcription.APIProfileOpenAIWhisper) ||
		created.RequestEncoding != string(transcription.RequestEncodingMultipart) {
		t.Fatalf("created = %+v, %v", created, err)
	}
	updated, err := database.UpdateTranscriptionModel(t.Context(), generated.UpdateTranscriptionModelParams{
		DisplayName: created.DisplayName, Protocol: created.Protocol, Provider: created.Provider, Endpoint: created.Endpoint,
		ApiKey: created.ApiKey, ModelName: created.ModelName, ApiProfile: string(transcription.APIProfileOpenAIJSON),
		RequestEncoding: string(transcription.RequestEncodingBase64JSON),
		Source:          created.Source, ModelID: created.ModelID,
	})
	if err != nil || updated.ApiProfile != string(transcription.APIProfileOpenAIJSON) ||
		updated.RequestEncoding != string(transcription.RequestEncodingBase64JSON) {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
	deepgram, err := database.CreateTranscriptionModel(t.Context(), generated.CreateTranscriptionModelParams{
		ModelID: "deepgram", DisplayName: "Deepgram", Protocol: "deepgram", Provider: "deepgram",
		Endpoint: "https://api.deepgram.com/v1/listen", ApiKey: "secret", ModelName: "nova-3",
		ApiProfile: string(transcription.APIProfileOpenAIDiarized), SupportsTimecodes: true, Source: "custom",
	})
	if err != nil || deepgram.ApiProfile != string(transcription.APIProfileAuto) {
		t.Fatalf("deepgram = %+v, %v", deepgram, err)
	}
	if _, err := database.CreateTranscriptionModel(t.Context(), generated.CreateTranscriptionModelParams{
		ModelID: "invalid-profile", DisplayName: "Invalid", Protocol: "deepgram", Provider: "deepgram",
		Endpoint: "https://api.deepgram.com/v1/listen", ApiKey: "secret", ModelName: "nova-3",
		ApiProfile: "nonsense", SupportsTimecodes: true, Source: "custom",
	}); err == nil {
		t.Fatal("invalid Deepgram API profile was accepted")
	}
}

func TestTranscriptionDefaultsUseIndependentSettingsAndCanNameMissingModels(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()

	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTranscript, "missing-transcript-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTimecoded, "missing-timecoded-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SetTranscriptionModelDefault(t.Context(), transcription.RoleTranscript, "replacement-id"); err != nil {
		t.Fatal(err)
	}
	if inserted, err := database.InitializeTranscriptionModelDefault(t.Context(), transcription.RoleTranscript, "ignored-id"); err != nil || inserted {
		t.Fatalf("initialize existing default = %v, %v", inserted, err)
	}
	defaults, err := database.ListTranscriptionModelDefaults(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults) != 2 {
		t.Fatalf("defaults = %+v", defaults)
	}
	got := map[string]string{}
	for _, item := range defaults {
		got[item.Role] = item.ModelID
	}
	if got["transcript"] != "replacement-id" || got["timecoded"] != "missing-timecoded-id" {
		t.Fatalf("defaults = %+v", got)
	}
	settings, err := database.GetAllSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) != 2 ||
		settings["transcription.default.transcript"] != "replacement-id" ||
		settings["transcription.default.timecoded"] != "missing-timecoded-id" {
		t.Fatalf("settings = %+v", settings)
	}
	if deleted, err := database.DeleteTranscriptionModelDefault(t.Context(), transcription.RoleTranscript); err != nil || !deleted {
		t.Fatalf("delete existing default = %v, %v", deleted, err)
	}
	if deleted, err := database.DeleteTranscriptionModelDefault(t.Context(), transcription.RoleTranscript); err != nil || deleted {
		t.Fatalf("delete missing default = %v, %v", deleted, err)
	}
	invalidRole := transcription.Role("invalid")
	if _, err := database.SetTranscriptionModelDefault(t.Context(), invalidRole, "model"); err == nil {
		t.Fatal("invalid role was accepted")
	}
	if inserted, err := database.InitializeTranscriptionModelDefault(t.Context(), invalidRole, "model"); err == nil || inserted {
		t.Fatalf("initialize invalid role = %v, %v", inserted, err)
	}
	if deleted, err := database.DeleteTranscriptionModelDefault(t.Context(), invalidRole); err == nil || deleted {
		t.Fatalf("delete invalid role = %v, %v", deleted, err)
	}
}

func TestRetryQueuedTranscriptionPreservesPinnedModelRefs(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	parent, err := database.CreateConversation(t.Context(), nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, queued, err := database.CreateQueuedTranscription(t.Context(), parent.ConversationID, QueuedMessage{
		ID: "queued", CreatedAt: time.Now().UTC(), Model: "chat-model",
		Kind: QueuedMessageKindTranscription, State: QueuedMessageStateWorking,
		Transcription: &QueuedTranscription{
			MediaPath: "/tmp/recording.webm",
			Models: map[transcription.Role]transcription.ModelRef{
				transcription.RoleTranscript: (transcription.Model{
					ID: "stable-transcript", Protocol: transcription.ProtocolOpenAI, Provider: "openai",
					Endpoint: "https://api.example/v1/audio/transcriptions", Model: "wire",
				}).Ref(),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.UpdateQueuedMessage(t.Context(), parent.ConversationID, queued.ID, func(item *QueuedMessage) error {
		item.State = QueuedMessageStateFailed
		item.Error = "failed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, retried, err := database.RetryQueuedTranscription(t.Context(), parent.ConversationID, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := retried.Transcription.Models[transcription.RoleTranscript]; got.ID != "stable-transcript" || got.Revision == "" {
		t.Fatalf("retried transcript model ref = %+v", got)
	}
}
