package oai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"shelley.exe.dev/llm"
)

func TestServiceDisableReasoning(t *testing.T) {
	testDisableReasoning(t, false)
}

func TestResponsesServiceDisableReasoning(t *testing.T) {
	testDisableReasoning(t, true)
}

func testDisableReasoning(t *testing.T, responses bool) {
	t.Helper()
	for _, tc := range []struct {
		name       string
		svcLevel   llm.ThinkingLevel
		svcEffort  string
		reqLevel   llm.ThinkingLevel
		reqEffort  string
		wantEffort string
	}{
		{name: "incoming thinking", svcLevel: llm.ThinkingLevelOff, reqLevel: llm.ThinkingLevelHigh, wantEffort: "high"},
		{name: "off catalog fallback", svcLevel: llm.ThinkingLevelOff, reqLevel: llm.ThinkingLevelOff, wantEffort: "low"},
		{name: "service thinking", svcLevel: llm.ThinkingLevelMedium, wantEffort: "medium"},
		{name: "service effort", svcEffort: "custom-service", wantEffort: "custom-service"},
		{name: "request effort", svcLevel: llm.ThinkingLevelHigh, svcEffort: "custom-service", reqLevel: llm.ThinkingLevelMedium, reqEffort: "custom-request", wantEffort: "custom-request"},
	} {
		for _, disabled := range []bool{false, true} {
			t.Run(tc.name+"/disabled="+strconv.FormatBool(disabled), func(t *testing.T) {
				requests := make(chan map[string]json.RawMessage, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var wire map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
						t.Errorf("decode request: %v", err)
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					requests <- wire
					body := `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
					if responses {
						body = `{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
					}
					w.Header().Set("Content-Type", "application/json")
					if _, err := io.WriteString(w, body); err != nil {
						t.Errorf("write response: %v", err)
					}
				}))
				defer server.Close()

				// A known reasoning ID on a proxy endpoint must not override an
				// explicit opt-out, even when models.dev advertises effort levels.
				var svc llm.Service
				if responses {
					svc = &ResponsesService{
						APIKey: "fixture", Model: GPT6Astra, ModelURL: server.URL,
						ThinkingLevel: tc.svcLevel, ReasoningEffort: tc.svcEffort,
						DisableReasoning: disabled,
					}
				} else {
					svc = &Service{
						APIKey: "fixture", Model: GPT6Astra, ModelURL: server.URL,
						ThinkingLevel: tc.svcLevel, ReasoningEffort: tc.svcEffort,
						DisableReasoning: disabled,
					}
				}
				resp, err := svc.Do(t.Context(), &llm.Request{
					Messages:        []llm.Message{llm.UserStringMessage("hello")},
					ThinkingLevel:   tc.reqLevel,
					ReasoningEffort: tc.reqEffort,
				})
				if err != nil {
					t.Fatalf("Do: %v", err)
				}
				if got := llm.FirstText(resp); got != "ok" {
					t.Fatalf("response text = %q, want ok", got)
				}
				wire := <-requests
				if got := string(wire["model"]); got != strconv.Quote(GPT6Astra.ModelName) {
					t.Fatalf("model = %s, want %q", got, GPT6Astra.ModelName)
				}
				if disabled {
					for _, field := range []string{"reasoning_effort", "reasoning", "include"} {
						if value, ok := wire[field]; ok {
							t.Errorf("disabled reasoning emitted %s: %s", field, value)
						}
					}
					if llm.SupportsReasoning(svc) || len(llm.SupportedReasoningLevels(svc)) != 0 || llm.ServiceDefaultReasoningLevel(svc) != "" {
						t.Error("disabled service advertises reasoning controls")
					}
					return
				}
				if !llm.SupportsReasoning(svc) || len(llm.SupportedReasoningLevels(svc)) == 0 {
					t.Fatal("fixture must exercise known reasoning metadata")
				}
				var effort string
				if responses {
					var reasoning responsesReasoning
					if err := json.Unmarshal(wire["reasoning"], &reasoning); err != nil {
						t.Fatalf("decode reasoning: %v", err)
					}
					effort = reasoning.Effort
					if reasoning.Summary != "auto" || string(wire["include"]) != `["reasoning.encrypted_content"]` {
						t.Errorf("enabled Responses reasoning controls changed: %s", wire)
					}
				} else if err := json.Unmarshal(wire["reasoning_effort"], &effort); err != nil {
					t.Fatalf("decode reasoning_effort: %v", err)
				}
				if effort != tc.wantEffort {
					t.Errorf("effort = %q, want %q", effort, tc.wantEffort)
				}
			})
		}
	}
}
