package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"shelley.exe.dev/db/generated"
)

// TestCustomModelWithThinking tests that the custom model test endpoint
// correctly handles responses from Anthropic models with ThinkingLevel enabled.
// When thinking is enabled, the first content block is a thinking block, not text.
func TestCustomModelWithThinking(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping integration test")
	}

	// Create a service with thinking enabled
	service := &ant.Service{
		APIKey:        apiKey,
		Model:         ant.Claude46Opus,
		ThinkingLevel: llm.ThinkingLevelMedium,
	}

	// Send a simple test request
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	request := &llm.Request{
		Messages: []llm.Message{
			{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Say 'test successful' in exactly two words."},
				},
			},
		},
	}

	response, err := service.Do(ctx, request)
	if err != nil {
		t.Fatalf("API call failed: %v", err)
	}

	// Verify response has content
	if len(response.Content) == 0 {
		t.Fatal("Response has no content blocks")
	}

	// The first block should be a thinking block
	if response.Content[0].Type != llm.ContentTypeThinking {
		t.Logf("Warning: Expected first block to be thinking, got %v", response.Content[0].Type)
	}

	// Find the first text block (skipping thinking blocks)
	var foundText bool
	var responseText string
	for _, content := range response.Content {
		if content.Type == llm.ContentTypeText && content.Text != "" {
			responseText = content.Text
			foundText = true
			break
		}
	}

	if !foundText {
		t.Fatal("No text content found in response (only thinking blocks)")
	}

	t.Logf("Successfully received response with thinking enabled: %s", responseText)
}

// TestCustomModelTestEndpoint tests the HTTP endpoint for testing custom models.
// This simulates what happens when a user adds a custom Anthropic model in the UI.
func TestCustomModelTestEndpoint(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping integration test")
	}

	h := NewTestHarness(t)

	// Create a test request that simulates adding a custom Anthropic model
	testReq := struct {
		ProviderType string `json:"provider_type"`
		APIKey       string `json:"api_key"`
		Endpoint     string `json:"endpoint"`
		ModelName    string `json:"model_name"`
	}{
		ProviderType: "anthropic",
		APIKey:       apiKey,
		Endpoint:     "https://api.anthropic.com/v1/messages",
		ModelName:    ant.Claude46Opus,
	}

	body, err := json.Marshal(testReq)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/custom-models/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.server.handleTestModel(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if success, ok := result["success"].(bool); !ok || !success {
		t.Errorf("Test failed: %v", result["message"])
	}

	message, ok := result["message"].(string)
	if !ok {
		t.Fatal("Response missing message field")
	}

	t.Logf("Test endpoint response: %s", message)

	// Verify that we got a non-empty response
	if message == "" || message == "Test failed: empty response from model" {
		t.Error("Got empty response error despite having a valid API key")
	}
}

func TestExportModels(t *testing.T) {
	h := NewTestHarness(t)
	defer h.db.Close()

	// Create a test model
	modelID := "custom-test-export-" + uuid.New().String()[:8]
	_, err := h.db.CreateModel(context.Background(), generated.CreateModelParams{
		ModelID:      modelID,
		DisplayName:  "Test Export Model",
		ProviderType: "anthropic",
		Endpoint:     "https://api.anthropic.com/v1/messages",
		ApiKey:       "sk-test-key",
		ModelName:    "claude-sonnet-4-5",
		MaxTokens:    200000,
		Tags:         "",
	})
	if err != nil {
		t.Fatalf("Failed to create model: %v", err)
	}

	// Test export
	req, _ := http.NewRequest("GET", "/api/custom-models/export", nil)
	w := httptest.NewRecorder()
	h.server.handleExportModels(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify content type
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", w.Header().Get("Content-Type"))
	}

	// Verify Content-Disposition header
	disposition := w.Header().Get("Content-Disposition")
	if disposition == "" {
		t.Error("Missing Content-Disposition header")
	}
	if !strings.Contains(disposition, "attachment") {
		t.Errorf("Expected Content-Disposition to contain 'attachment', got %s", disposition)
	}

	// Parse the exported data
	var exportData ExportData
	if err := json.Unmarshal(w.Body.Bytes(), &exportData); err != nil {
		t.Fatalf("Failed to parse export data: %v", err)
	}

	// Verify export data
	if exportData.ExportedAt.IsZero() {
		t.Error("ExportedAt time is zero")
	}
	if len(exportData.Models) != 1 {
		t.Errorf("Expected 1 model, got %d", len(exportData.Models))
	}

	// Verify API key is NOT exported (ExportModel type doesn't have APIKey field)
	// This is a structural check - the ExportModel struct excludes APIKey
}

func TestImportModels(t *testing.T) {
	h := NewTestHarness(t)
	defer h.db.Close()

	// Create export data
	exportData := ExportData{
		ExportedAt: time.Now(),
		Models: []ExportModel{
			{
				DisplayName:  "Imported Model 1",
				ProviderType: "anthropic",
			Endpoint:     "https://api.anthropic.com/v1/messages",
			ModelName:    "claude-sonnet-4-5",
			MaxTokens:    200000,
			Tags:         "",
			},
			{
				DisplayName:  "Imported Model 2",
				ProviderType: "openai",
			Endpoint:     "https://api.openai.com/v1",
			ModelName:    "gpt-5",
			MaxTokens:    128000,
			Tags:         "",
			},
		},
	}

	jsonData, err := json.Marshal(exportData)
	if err != nil {
		t.Fatalf("Failed to marshal export data: %v", err)
	}

	// Create import request
	importReq := ImportRequest{
		Data:   jsonData,
		APIKey: "test-api-key-123",
	}

	reqBody, _ := json.Marshal(importReq)
	req, _ := http.NewRequest("POST", "/api/custom-models/import", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleImportModels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// Parse response
	var result ImportResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse import result: %v", err)
	}

	if result.Imported != 2 {
		t.Errorf("Expected 2 imported models, got %d", result.Imported)
	}
	if len(result.Errors) > 0 {
		t.Errorf("Expected no errors, got: %v", result.Errors)
	}

	// Verify models were actually imported with the provided API key
	models, err := h.db.GetModels(context.Background())
	if err != nil {
		t.Fatalf("Failed to get models: %v", err)
	}

	if len(models) != 2 {
		t.Errorf("Expected 2 models in database, got %d", len(models))
	}

	// Verify all imported models have the provided API key
	for _, m := range models {
		if m.ApiKey != "test-api-key-123" {
			t.Errorf("Expected API key 'test-api-key-123', got '%s'", m.ApiKey)
	}
	}
}

func TestImportModelsWithDuplicateNames(t *testing.T) {
	h := NewTestHarness(t)
	defer h.db.Close()

	// Create a model first to cause a name conflict
	_, err := h.db.CreateModel(context.Background(), generated.CreateModelParams{
		ModelID:      "custom-conflict-" + uuid.New().String()[:8],
		DisplayName:  "Conflicting Model",
		ProviderType: "anthropic",
		Endpoint:     "https://api.anthropic.com/v1/messages",
		ApiKey:       "existing-key",
		ModelName:    "claude",
		MaxTokens:    200000,
		Tags:         "",
	})
	if err != nil {
		t.Fatalf("Failed to create existing model: %v", err)
	}

	// Try to import a model with the same name
	exportData := ExportData{
		ExportedAt: time.Now(),
		Models: []ExportModel{
			{
				DisplayName:  "Conflicting Model",
				ProviderType: "anthropic",
				Endpoint:     "https://api.anthropic.com/v1/messages",
				ModelName:    "claude",
				MaxTokens:    200000,
				Tags:         "",
			},
		},
	}

	jsonData, _ := json.Marshal(exportData)
	importReq := ImportRequest{
		Data:   jsonData,
		APIKey: "new-key",
	}
	reqBody, _ := json.Marshal(importReq)
	req, _ := http.NewRequest("POST", "/api/custom-models/import", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleImportModels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// Should succeed with a renamed duplicate
	var result ImportResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Imported != 1 {
		t.Errorf("Expected 1 imported model, got %d", result.Imported)
	}

	// Verify the imported model was renamed
	models, _ := h.db.GetModels(context.Background())
	var importedCount int
	for _, m := range models {
		if m.DisplayName == "Conflicting Model (imported 1)" && m.ApiKey == "new-key" {
			importedCount++
		}
	}
	if importedCount != 1 {
		t.Error("Imported model was not properly renamed to handle conflict")
	}
}
