package ant

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
	for _, tc := range []struct {
		name       string
		model      string
		svcLevel   llm.ThinkingLevel
		reqLevel   llm.ThinkingLevel
		wantType   string
		wantEffort string
	}{
		{name: "service off catalog fallback", model: Claude48Opus, svcLevel: llm.ThinkingLevelOff, wantType: "adaptive", wantEffort: "low"},
		{name: "request off catalog fallback", model: Claude48Opus, svcLevel: llm.ThinkingLevelMedium, reqLevel: llm.ThinkingLevelOff, wantType: "adaptive", wantEffort: "low"},
		{name: "incoming thinking", model: Claude48Opus, svcLevel: llm.ThinkingLevelOff, reqLevel: llm.ThinkingLevelHigh, wantType: "adaptive", wantEffort: "high"},
		{name: "service thinking", model: Claude48Opus, svcLevel: llm.ThinkingLevelMedium, wantType: "adaptive", wantEffort: "medium"},
		{name: "model name fallback", model: "claude-opus-99-0", reqLevel: llm.ThinkingLevelHigh, wantType: "adaptive", wantEffort: "high"},
		{name: "budget thinking", model: Claude45Sonnet, reqLevel: llm.ThinkingLevelHigh, wantType: "enabled"},
	} {
		for _, disabled := range []bool{false, true} {
			t.Run(tc.name+"/disabled="+strconv.FormatBool(disabled), func(t *testing.T) {
				type capturedRequest struct {
					wire map[string]json.RawMessage
					beta string
				}
				requests := make(chan capturedRequest, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var wire map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
						t.Errorf("decode request: %v", err)
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					requests <- capturedRequest{wire: wire, beta: r.Header.Get("Anthropic-Beta")}
					w.Header().Set("Content-Type", "text/event-stream")
					if _, err := io.WriteString(w, mockSSEResponse("fixture", tc.model, "ok", 1, 1)); err != nil {
						t.Errorf("write response: %v", err)
					}
				}))
				defer server.Close()

				svc := &Service{
					APIKey: "fixture", Model: tc.model, URL: server.URL + "/v1/messages",
					ThinkingLevel: tc.svcLevel, DisableReasoning: disabled,
					EnableThinkingBinding: true,
				}
				resp, err := svc.Do(t.Context(), &llm.Request{
					Messages:      []llm.Message{llm.UserStringMessage("hello")},
					ThinkingLevel: tc.reqLevel,
				})
				if err != nil {
					t.Fatalf("Do: %v", err)
				}
				if got := llm.FirstText(resp); got != "ok" {
					t.Fatalf("response text = %q, want ok", got)
				}
				got := <-requests
				if model := string(got.wire["model"]); model != strconv.Quote(tc.model) {
					t.Fatalf("model = %s, want %q", model, tc.model)
				}
				if disabled {
					for _, field := range []string{"thinking", "output_config"} {
						if value, ok := got.wire[field]; ok {
							t.Errorf("disabled reasoning emitted %s: %s", field, value)
						}
					}
					if got.beta != "" {
						t.Errorf("disabled reasoning emitted beta header: %q", got.beta)
					}
					if svc.SupportsReasoning() || len(svc.SupportedReasoningLevels()) != 0 || svc.DefaultReasoningLevel() != "" {
						t.Error("disabled service advertises reasoning controls")
					}
					return
				}
				var wire request
				data, err := json.Marshal(got.wire)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &wire); err != nil {
					t.Fatal(err)
				}
				if wire.Thinking == nil || wire.Thinking.Type != tc.wantType || wire.Thinking.BlockBinding == nil {
					t.Fatalf("thinking = %+v, want %s with binding controls", wire.Thinking, tc.wantType)
				}
				if got.beta != thinkingBindingBeta {
					t.Errorf("beta = %q, want %q", got.beta, thinkingBindingBeta)
				}
				if tc.wantEffort != "" && (wire.OutputConfig == nil || wire.OutputConfig.Effort != tc.wantEffort) {
					t.Errorf("output_config = %+v, want effort %q", wire.OutputConfig, tc.wantEffort)
				}
			})
		}
	}
}
