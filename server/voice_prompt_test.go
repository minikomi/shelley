package server

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models"
)

// voiceCatalog is a configured catalog: models from the free gateway, one
// from the user's own OpenAI key, and a custom one.
var voiceCatalog = map[string]models.ModelInfo{
	"model-a":           knownModel("gpt-6.1-sol", "exe.dev"),
	"model-b":           knownModel("claude-opus-5.5", "exe.dev"),
	"model-c":           knownModel("kimi-k3-fireworks", "exe.dev"),
	"gpt-5.6-terra":     knownModel("gpt-5.6-terra", "exe.dev"),
	"claude-sonnet-5.5": knownModel("claude-sonnet-5.5", "exe.dev"),
	"claude-haiku-5.5":  knownModel("claude-haiku-5.5", "exe.dev"),
	"gpt-6.1-sol-own":   knownModel("gpt-6.1-sol", "openai.key"),
	"gpt-5.6-terra-own": knownModel("gpt-5.6-terra", "openai.key"),
	"my-opus":           knownModel("claude-opus-5.5", models.SourceCustomLabel),
}

func knownModel(catalogID, source string) models.ModelInfo {
	m := models.ByID(catalogID)
	return models.ModelInfo{Provider: m.Provider, APIModelName: m.APIModelName, Source: source, BaseURL: "https://" + source, ReleaseDate: "2026-01-01"}
}

// voiceModels serves voiceCatalog from one backend that records which model
// was asked and answers with reply or err.
type voiceModels struct {
	mu    sync.Mutex
	asked []string
	reply string
	err   error
}

type voiceModel struct {
	llm.Service // a predictable service, for everything but Do
	*voiceModels
	id string
}

func (m voiceModel) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, m.id)
	if m.err != nil {
		return nil, m.err
	}
	return &llm.Response{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: []llm.Content{llm.StringContent(m.reply)}}, nil
}

func (m *voiceModels) GetService(id string) (llm.Service, error) {
	if !m.HasModel(id) {
		return nil, os.ErrNotExist
	}
	return voiceModel{predictable.NewService(), m, id}, nil
}

func (m *voiceModels) GetWorkhorseService(id string) (llm.Service, error) { return m.GetService(id) }

func (m *voiceModels) GetAvailableModels() []string {
	return slices.Sorted(maps.Keys(voiceCatalog))
}
func (m *voiceModels) HasModel(id string) bool { _, ok := voiceCatalog[id]; return ok }
func (m *voiceModels) GetModelInfo(id string) *models.ModelInfo {
	if info, ok := voiceCatalog[id]; ok {
		return &info
	}
	return nil
}
func (m *voiceModels) RefreshCustomModels() error { return nil }

// A voice prompt is written by a middle-tier model of the profile model's
// provider and source, so the same account pays, or else by the profile's
// model; from the system prompt alone, without reasoning or tools. What is
// wrong with the result is safe to show; a provider's errors are not.
func TestVoicePromptGenerate(t *testing.T) {
	t.Parallel()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	catalog := &voiceModels{reply: " You are Juniper. Speak warmly.\n"}
	srv := NewServer(database, catalog, claudetool.ToolSetConfig{},
		slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})), false, "model-a", "")
	generate := func(body string) (int, string) {
		w := httptest.NewRecorder()
		srv.handleGenerateVoicePrompt(w, httptest.NewRequest(http.MethodPost, "/api/profiles/voice-prompt/generate", strings.NewReader(body)))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	for model, want := range map[string]string{
		"":                  "gpt-5.6-terra",     // the server's default, model-a
		"model-b":           "claude-sonnet-5.5", // not haiku
		"claude-sonnet-5.5": "claude-sonnet-5.5", // already middle-tier
		"model-c":           "model-c",           // its provider has no preferred family
		"gpt-6.1-sol-own":   "gpt-5.6-terra-own", // the same account, not the gateway's terra
		"my-opus":           "my-opus",           // a custom source names no account
	} {
		code, got := generate(`{"model":"` + model + `","system_prompt":"You are Juniper, a cozy assistant."}`)
		if code != http.StatusOK || got != `{"voice_prompt":"You are Juniper. Speak warmly."}` {
			t.Fatalf("%q: %d %s", model, code, got)
		}
		if asked := catalog.asked[len(catalog.asked)-1]; asked != want {
			t.Errorf("%q: written by %s, want %s", model, asked, want)
		}
	}
	if code, got := generate(`{"model":"nope"}`); code != http.StatusBadRequest {
		t.Errorf("unknown model: %d %s", code, got)
	}

	catalog.err = errors.New("PROVIDER-SECRET")
	if code, got := generate(`{}`); code != http.StatusBadGateway || got != "Could not generate a voice prompt" {
		t.Errorf("provider error: %d %s", code, got)
	}
	catalog.err, catalog.reply = nil, " "
	if code, got := generate(`{}`); code != http.StatusBadGateway || got != "voice prompt: the model wrote none" {
		t.Errorf("empty: %d %s", code, got)
	}
}

// Saved accounts are identified by SourceID, not by their mutable label: two
// accounts may share a label, endpoint and models, and the prompt for one must
// never be written with the other's key.
func TestVoicePromptModelSavedAccountsMatchBySourceIDNotLabel(t *testing.T) {
	t.Parallel()
	const endpoint = "https://api.example.test/v1"
	route := func(id, sourceID, catalogID string) models.Built {
		m := models.ByID(catalogID)
		return models.Built{
			ID: id, Provider: m.Provider, APIModelName: m.APIModelName, ReleaseDate: "2026-01-01",
			Source: "Shared label", SourceID: sourceID, BaseURL: endpoint, Service: predictable.NewService(),
		}
	}
	manager, err := models.NewManager(&models.Config{Models: []models.Built{
		// Account A's terra is listed first, so a label-based match from
		// account B would pick it.
		route("a/terra", "A", "gpt-5.6-terra"),
		route("a/sol", "A", "gpt-6.1-sol"),
		route("b/sol", "B", "gpt-6.1-sol"),
		route("b/terra", "B", "gpt-5.6-terra"),
		route("c/sol", "C", "gpt-6.1-sol"), // same label, no terra of its own
		route("legacy/sol", "", "gpt-6.1-sol"),
		route("legacy/terra", "", "gpt-5.6-terra"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{llmManager: manager}
	for model, want := range map[string]string{
		"a/sol":      "a/terra",
		"b/sol":      "b/terra",
		"c/sol":      "c/sol", // never borrows A's or B's credentials
		"legacy/sol": "legacy/terra",
		"b/terra":    "b/terra",
	} {
		if got := s.voicePromptModel(model); got != want {
			t.Errorf("voicePromptModel(%q) = %q, want %q", model, got, want)
		}
	}
}
