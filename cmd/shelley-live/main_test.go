package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionMintsRealtimeClientSecret(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(w, http.StatusOK, map[string]any{"value": "ek_test"})
	}))
	defer upstream.Close()

	a, err := newApp(config{
		openAIBaseURL: upstream.URL,
		openAIAPIKey:  "secret",
		shelleyURL:    upstream.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/session", nil)
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	session := gotBody["session"].(map[string]any)
	if session["model"] != "gpt-realtime" {
		t.Fatalf("model = %v", session["model"])
	}
	if len(session["tools"].([]any)) != 3 {
		t.Fatalf("tools = %v", session["tools"])
	}
}

func TestBuildJobStartsShelleyConversation(t *testing.T) {
	var gotPath string
	var gotHeader string
	var gotBody map[string]any
	shelley := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Shelley-Request")
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(w, http.StatusCreated, map[string]any{"conversation_id": "job123"})
	}))
	defer shelley.Close()

	a, err := newApp(config{
		openAIBaseURL:    shelley.URL,
		shelleyURL:       shelley.URL,
		shelleyCWD:       "/work",
		shelleyPublicURL: "https://example.test",
		userEmail:        "adam@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"kind":"build","goal":"Fix the button","acceptance_criteria":"Tests pass"}`
	req := httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/conversations/new" || gotHeader != "1" {
		t.Fatalf("request = %s, header = %q", gotPath, gotHeader)
	}
	if gotBody["cwd"] != "/work" {
		t.Fatalf("cwd = %v", gotBody["cwd"])
	}
	message := gotBody["message"].(string)
	if !strings.Contains(message, "Fix the button") || !strings.Contains(message, "Tests pass") {
		t.Fatalf("message = %q", message)
	}
	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)
	if result["shelley_url"] != "https://example.test/c/job123" {
		t.Fatalf("shelley_url = %v", result["shelley_url"])
	}
}

func TestFollowupQueuesExistingConversation(t *testing.T) {
	var gotBody map[string]any
	shelley := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/conversation/job123/chat" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(w, http.StatusAccepted, map[string]any{})
	}))
	defer shelley.Close()

	a, _ := newApp(config{shelleyURL: shelley.URL})
	req := httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(
		`{"kind":"followup","goal":"Use the smaller icon","conversation_id":"job123"}`,
	))
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotBody["queue"] != true || gotBody["message"] != "Use the smaller icon" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestJobEventsProxiesSSE(t *testing.T) {
	shelley := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"conversation_state\":{\"working\":false}}\n\n")
	}))
	defer shelley.Close()

	a, _ := newApp(config{shelleyURL: shelley.URL})
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/job123/events", nil)
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"working":false`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
