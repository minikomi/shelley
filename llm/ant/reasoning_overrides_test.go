package ant

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"shelley.exe.dev/llm"
)

func TestServiceReasoningOverrides(t *testing.T) {
	mediumHigh := []llm.ThinkingLevel{llm.ThinkingLevelMedium, llm.ThinkingLevelHigh}
	for _, tc := range []struct {
		name        string
		model       string
		levels      []llm.ThinkingLevel
		types       []string
		svcLevel    llm.ThinkingLevel
		reqLevel    llm.ThinkingLevel
		disabled    bool
		maxTokens   int
		wantType    string
		wantEffort  string
		wantBudget  int
		wantDefault string
	}{
		{name: "adaptive off clamps to provider medium", levels: mediumHigh, types: []string{"adaptive"}, reqLevel: llm.ThinkingLevelOff, wantType: "adaptive", wantEffort: "medium", wantDefault: "off"},
		{name: "adaptive low clamps to provider medium", levels: mediumHigh, types: []string{"adaptive"}, reqLevel: llm.ThinkingLevelLow, wantType: "adaptive", wantEffort: "medium", wantDefault: "off"},
		{name: "service off clamps to provider medium", levels: mediumHigh, types: []string{"adaptive"}, svcLevel: llm.ThinkingLevelOff, wantType: "adaptive", wantEffort: "medium", wantDefault: "medium"},
		{name: "service low clamps to provider medium", levels: mediumHigh, types: []string{"adaptive"}, svcLevel: llm.ThinkingLevelLow, wantType: "adaptive", wantEffort: "medium", wantDefault: "medium"},
		{name: "explicit minimal bypasses adaptive fallback", levels: []llm.ThinkingLevel{llm.ThinkingLevelMinimal}, types: []string{"adaptive"}, reqLevel: llm.ThinkingLevelLow, wantType: "adaptive", wantEffort: "minimal", wantDefault: "off"},
		{name: "explicit minimal bypasses catalog clamp", model: Claude48Opus, levels: []llm.ThinkingLevel{llm.ThinkingLevelMinimal}, reqLevel: llm.ThinkingLevelLow, wantType: "adaptive", wantEffort: "minimal", wantDefault: "off"},
		{name: "enabled only on known model", levels: mediumHigh, types: []string{"enabled"}, reqLevel: llm.ThinkingLevelLow, wantType: "enabled", wantBudget: 8192, wantDefault: "off"},
		{name: "enabled only overrides adaptive name", model: Claude48Opus, levels: mediumHigh, types: []string{"enabled"}, reqLevel: llm.ThinkingLevelHigh, wantType: "enabled", wantBudget: 16384, wantDefault: "off"},
		{name: "enabled budget stays within max tokens", levels: mediumHigh, types: []string{"enabled"}, reqLevel: llm.ThinkingLevelHigh, maxTokens: 3000, wantType: "enabled", wantBudget: 1976, wantDefault: "off"},
		{name: "enabled budget too small", levels: mediumHigh, types: []string{"enabled"}, reqLevel: llm.ThinkingLevelHigh, maxTokens: 1500, wantDefault: "off"},
		{name: "levels alone retain name mode", levels: mediumHigh, reqLevel: llm.ThinkingLevelLow, wantType: "enabled", wantBudget: 8192, wantDefault: "off"},
		{name: "types alone override name mode", model: Claude48Opus, types: []string{"enabled"}, reqLevel: llm.ThinkingLevelLow, wantType: "enabled", wantBudget: 2048, wantDefault: "off"},
		{name: "both types prefer adaptive", levels: mediumHigh, types: []string{"enabled", "adaptive"}, reqLevel: llm.ThinkingLevelHigh, wantType: "adaptive", wantEffort: "high", wantDefault: "off"},
		{name: "explicit empty levels disable", levels: []llm.ThinkingLevel{}, types: []string{"adaptive"}, reqLevel: llm.ThinkingLevelHigh},
		{name: "explicit empty types disable", levels: mediumHigh, types: []string{}, reqLevel: llm.ThinkingLevelHigh},
		{name: "DisableReasoning overrides provider policy", levels: mediumHigh, types: []string{"adaptive"}, reqLevel: llm.ThinkingLevelHigh, disabled: true},
		{name: "nil overrides retain existing mode", reqLevel: llm.ThinkingLevelLow, wantType: "enabled", wantBudget: 2048, wantDefault: "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := tc.model
			if model == "" {
				model = Claude46Opus
			}
			maxTokens := tc.maxTokens
			if maxTokens == 0 {
				maxTokens = 32000
			}
			requests := make(chan json.RawMessage, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := io.WriteString(w, mockSSEResponse("fixture", model, "ok", 1, 1)); err != nil {
					t.Errorf("write response: %v", err)
				}
			}))
			defer server.Close()

			svc := &Service{
				APIKey: "fixture", Model: model, URL: server.URL + "/v1/messages",
				MaxTokens: maxTokens, ThinkingLevel: tc.svcLevel,
				ReasoningLevels: tc.levels, ThinkingTypes: tc.types, DisableReasoning: tc.disabled,
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
			body := <-requests
			var wire request
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Model != model || wire.MaxTokens != maxTokens {
				t.Fatalf("model/max_tokens = %s/%d, want %s/%d", wire.Model, wire.MaxTokens, model, maxTokens)
			}
			if tc.wantType == "" {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(body, &fields); err != nil {
					t.Fatal(err)
				}
				for _, field := range []string{"thinking", "output_config"} {
					if value, present := fields[field]; present {
						t.Errorf("unwanted %s: %s", field, value)
					}
				}
			} else {
				if wire.Thinking == nil || wire.Thinking.Type != tc.wantType {
					t.Fatalf("thinking = %+v, want type %s", wire.Thinking, tc.wantType)
				}
				if wire.Thinking.BudgetTokens != tc.wantBudget {
					t.Errorf("budget_tokens = %d, want %d", wire.Thinking.BudgetTokens, tc.wantBudget)
				}
				if tc.wantEffort == "" {
					if wire.OutputConfig != nil {
						t.Errorf("enabled mode emitted adaptive output_config: %+v", wire.OutputConfig)
					}
				} else if wire.OutputConfig == nil || wire.OutputConfig.Effort != tc.wantEffort {
					t.Errorf("output_config = %+v, want effort %s", wire.OutputConfig, tc.wantEffort)
				}
			}
			if got := svc.DefaultReasoningLevel(); got != tc.wantDefault {
				t.Errorf("default = %q, want %q", got, tc.wantDefault)
			}
			wantSupport := tc.wantDefault != ""
			if got := svc.SupportsReasoning(); got != wantSupport {
				t.Errorf("supports reasoning = %t, want %t", got, wantSupport)
			}
			levels := svc.SupportedReasoningLevels()
			if !wantSupport && len(levels) != 0 {
				t.Errorf("disabled service advertises levels: %v", levels)
			}
			if wantSupport && tc.levels != nil {
				if !slices.Equal(levels, tc.levels) {
					t.Fatalf("levels = %v, want %v", levels, tc.levels)
				}
				first := tc.levels[0]
				levels[0] = llm.ThinkingLevelDefault
				if svc.ReasoningLevels[0] != first {
					t.Error("advertised levels alias service configuration")
				}
			}
		})
	}
}
