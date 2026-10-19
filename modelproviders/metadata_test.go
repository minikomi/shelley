package modelproviders

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
	"shelley.exe.dev/models/modelsdev"
)

func TestProviderMetadataOverridesFieldsWithoutPromotingAdvisoryEvidence(t *testing.T) {
	yes, no := true, false
	var descriptor discoveredModel
	if err := json.Unmarshal([]byte(`{
		"id":"unfamiliar","display_name":"Provider name","max_input_tokens":32100,"max_tokens":1024,
		"capabilities":{"image_input":{"supported":false},"thinking":{"supported":null}}
	}`), &descriptor); err != nil {
		t.Fatal(err)
	}
	metadata := descriptor.enrich(&modelsdev.Metadata{
		Match: "advisory", Name: "Catalog name", Images: &yes, ToolCall: &no, Reasoning: &yes,
	})
	if metadata.Name != "Provider name" || metadata.Images == nil || *metadata.Images ||
		metadata.ContextLimit != 32100 || metadata.OutputLimit != 1024 {
		t.Fatalf("provider fields lost: %+v", metadata)
	}
	if !metadata.Authoritative("images") || metadata.Authoritative("tool_call") ||
		metadata.Authoritative("reasoning") || metadata.Match != "advisory" {
		t.Fatalf("provider override promoted unrelated advisory fields: %+v", metadata)
	}
	built, err := models.BuildProviderModel("account", descriptor.ID, "route", metadata.Name,
		"anthropic", "https://example.test/api", "fixture", nil, metadata)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := models.NewManager(&models.Config{Models: []models.Built{built}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetService("route"); err != nil {
		t.Fatalf("advisory tool_call=false excluded provider route: %v", err)
	}
}

func TestAnthropicDiscoveredMetadataReachesServiceAndLimits(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "fixture" {
			t.Error("explicit provider key missing")
		}
		if r.Method == http.MethodGet {
			if r.URL.Path != "/tenant/api/models" {
				t.Errorf("discovery path = %q", r.URL.Path)
			}
			fmt.Fprint(w, `{"data":[{"id":"new-model","display_name":"New Model","lifecycle":"active",
				"max_input_tokens":32100,"max_tokens":1024,"capabilities":{
				"image_input":{"supported":true},"thinking":{"supported":true,
				"types":{"adaptive":{"supported":true}}},"effort":{"low":{"supported":true}}}}],
				"has_more":false}`)
			return
		}
		if r.URL.Path != "/tenant/api/messages" {
			t.Errorf("inference path = %q", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		if payload["model"] != "new-model" || payload["max_tokens"] != float64(1024) {
			t.Errorf("wire model/limit = %v/%v", payload["model"], payload["max_tokens"])
		}
		if _, ok := payload["thinking"]; ok {
			t.Error("support without a known wire vocabulary enabled reasoning controls")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"new-model\",\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer fixture.Close()
	descriptors, err := discover(t.Context(), fixture.Client(), "anthropic", fixture.URL+"/tenant/api", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 {
		t.Fatalf("discovered %d models, want 1", len(descriptors))
	}
	metadata := descriptors[0].enrich(nil)
	built, err := models.BuildProviderModel("account", descriptors[0].ID, "route", metadata.Name,
		"anthropic", fixture.URL+"/tenant/api", "fixture", fixture.Client(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := models.NewManager(&models.Config{Models: []models.Built{built}})
	if err != nil {
		t.Fatal(err)
	}
	id := registry.GetAvailableModels()[0]
	info := registry.GetModelInfo(id)
	if info.DisplayName != "New Model" || info.Capabilities.ContextLimit != 32100 ||
		info.Capabilities.Lifecycle != "active" || !info.Capabilities.Authoritative("images") {
		t.Fatalf("discovery metadata lost: %+v", info)
	}
	service, err := registry.GetService(id)
	if err != nil {
		t.Fatal(err)
	}
	if !service.SupportsImages() || llm.SupportsReasoning(service) {
		t.Fatal("reported images or conservative reasoning policy lost")
	}
	if _, err := service.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{llm.UserStringMessage("hello")}, ThinkingLevel: llm.ThinkingLevelHigh,
		OnStream: func(llm.StreamDelta) {},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderReasoningRestrictionsReachAnthropicWire(t *testing.T) {
	var descriptor discoveredModel
	if err := json.Unmarshal([]byte(`{"id":"claude-opus-4-6","capabilities":{
		"thinking":{"supported":true,"types":{"adaptive":{"supported":false},"enabled":{"supported":true}}},
		"effort":{"low":{"supported":false},"high":{"supported":true}}
	}}`), &descriptor); err != nil {
		t.Fatal(err)
	}
	yes := true
	metadata := descriptor.enrich(&modelsdev.Metadata{
		Match: "exact", Reasoning: &yes,
		ReasoningLevels: []llm.ThinkingLevel{llm.ThinkingLevelLow, llm.ThinkingLevelHigh},
	})
	var called atomic.Bool
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		var payload struct {
			MaxTokens int `json:"max_tokens"`
			Thinking  struct {
				Type   string `json:"type"`
				Budget int    `json:"budget_tokens"`
			} `json:"thinking"`
			OutputConfig json.RawMessage `json:"output_config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		if payload.Thinking.Type != "enabled" || len(payload.OutputConfig) > 0 ||
			payload.Thinking.Budget < 1024 || payload.Thinking.Budget >= payload.MaxTokens {
			t.Errorf("provider reasoning restrictions lost: %+v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-4-6\",\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer fixture.Close()
	built, err := models.BuildProviderModel("account", descriptor.ID, "route", "Known",
		"anthropic", fixture.URL, "fixture", fixture.Client(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	if got := llm.ServiceDefaultReasoningLevel(built.Service); got != "high" {
		t.Fatalf("default level = %q, want authoritative high", got)
	}
	if _, err := built.Service.Do(t.Context(), &llm.Request{
		Messages: []llm.Message{llm.UserStringMessage("hello")}, ThinkingLevel: llm.ThinkingLevelOff,
		OnStream: func(llm.StreamDelta) {},
	}); err != nil {
		t.Fatal(err)
	}
	if !called.Load() {
		t.Fatal("fixture did not receive inference")
	}
}
