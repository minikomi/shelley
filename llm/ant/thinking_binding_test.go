package ant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

func TestThinkingBindingRequestGating(t *testing.T) {
	for _, tt := range []struct {
		name  string
		model string
		level llm.ThinkingLevel
		want  bool
	}{
		{"adaptive", Claude46Opus, llm.ThinkingLevelMedium, true},
		{"mythos", "claude-mythos-preview", llm.ThinkingLevelLow, true},
		{"qualified-mythos", "anthropic/claude-mythos-preview", llm.ThinkingLevelLow, true},
		{"not-claude", "not-claude-sonnet-4-6", llm.ThinkingLevelLow, false},
		{"budget", Claude45Sonnet, llm.ThinkingLevelLow, true},
		{"legacy", "claude-3-7-sonnet-20250219", llm.ThinkingLevelLow, true},
		{"legacy-qualified", "us.anthropic.claude-3-7-sonnet-20250219-v1:0", llm.ThinkingLevelLow, true},
		{"qualified", "us.anthropic.claude-sonnet-4-5-v1:0", llm.ThinkingLevelLow, true},
		{"gateway", "anthropic/claude-sonnet-4-6", llm.ThinkingLevelLow, true},
		{"compatible", "minimax-m2.5", llm.ThinkingLevelLow, false},
		{"adaptive-off", Claude46Opus, llm.ThinkingLevelOff, false},
		{"budget-off", Claude45Sonnet, llm.ThinkingLevelOff, false},
		{"default-off", Claude46Opus, llm.ThinkingLevelDefault, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{Model: tt.model, ThinkingLevel: tt.level, HTTPC: &http.Client{Transport: &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
				var wire request
				if err := json.NewDecoder(req.Body).Decode(&wire); err != nil {
					t.Fatal(err)
				}
				got := wire.Thinking != nil && wire.Thinking.BlockBinding != nil
				if got != tt.want || (req.Header.Get("Anthropic-Beta") == thinkingBindingBeta) != tt.want {
					t.Fatalf("binding config=%t header=%q want=%t", got, req.Header.Get("Anthropic-Beta"), tt.want)
				}
				if got && wire.Thinking.BlockBinding.PrefixMismatchBehavior != "drop_block" {
					t.Fatal("wrong prefix mismatch behavior")
				}
				if (tt.level == llm.ThinkingLevelOff || tt.level == llm.ThinkingLevelDefault) && wire.Thinking != nil {
					t.Fatal("binding controls enabled thinking that was off")
				}
				if req.Header.Get("Anthropic-Version") != "2023-06-01" {
					t.Fatal("lost API version")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(mockSSEResponse("msg", tt.model, "ok", 1, 1)))}, nil
			}}}}
			if _, err := s.Do(context.Background(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage("hello")}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestThinkingBindingStreamMetadata(t *testing.T) {
	a := llm.InputTransformation{Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "prefix_binding_mismatch"}
	b := llm.InputTransformation{Type: "thinking_dropped", Path: "messages.3.content.0", Reason: "model_binding_mismatch"}
	unknown := llm.InputTransformation{Type: "future_type", Path: "messages.5.content.0", Reason: "future_reason"}
	for _, tt := range []struct {
		name  string
		start []llm.InputTransformation
		delta []llm.InputTransformation
		want  []llm.InputTransformation
	}{
		{name: "absent"},
		{name: "start", start: []llm.InputTransformation{a, a}, want: []llm.InputTransformation{a}},
		{name: "delta", delta: []llm.InputTransformation{b}, want: []llm.InputTransformation{b}},
		{name: "both-repeated-unknown", start: []llm.InputTransformation{a}, delta: []llm.InputTransformation{a, b, unknown, b}, want: []llm.InputTransformation{a, b, unknown}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stream strings.Builder
			emit := func(v any) { stream.WriteString("data: " + string(thinkingJSON(t, v)) + "\n\n") }
			emit(streamEvent{Type: "message_start", Message: &response{ID: "msg", Model: Claude46Opus, Role: "assistant", InputTransformations: tt.start}})
			emit(streamEvent{Type: "content_block_start", ContentBlock: &content{Type: "text", Text: strp("answer")}})
			delta := thinkingJSON(t, streamDelta{StopReason: "end_turn"})
			emit(streamEvent{Type: "message_delta", Delta: delta, InputTransformations: tt.delta})
			emit(streamEvent{Type: "message_stop"})
			parsed, err := parseSSEStream(strings.NewReader(stream.String()), nil)
			if err != nil {
				t.Fatal(err)
			}
			out := toLLMResponse(parsed)
			if !reflect.DeepEqual(out.InputTransformations, tt.want) {
				t.Fatalf("transformations = %+v, want %+v", out.InputTransformations, tt.want)
			}
			if out.Content[0].Text != "answer" || out.StopReason != llm.StopReasonEndTurn {
				t.Fatal("metadata handling changed model output")
			}
			if bytes.Contains(thinkingJSON(t, out.ToMessage()), []byte("binding_mismatch")) {
				t.Fatal("input diagnostics leaked into model history")
			}
		})
	}
}

func TestThinkingSignatureRecoveryBoundedAndSafe(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal", true: "recovered"}[success], func(t *testing.T) {
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(old)
			calls := 0
			var retries []llm.RetryEvent
			s := &Service{Model: Claude46Opus, ThinkingLevel: llm.ThinkingLevelMedium, Backoff: []time.Duration{0}}
			s.HTTPC = &http.Client{Transport: &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
				calls++
				var wire request
				if err := json.NewDecoder(req.Body).Decode(&wire); err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, m := range wire.Messages {
					for _, c := range m.Content {
						if c.Type == "thinking" || c.Type == "redacted_thinking" {
							count++
						}
					}
				}
				if (calls == 1 && count != 4) || (calls > 1 && count != 0) {
					t.Fatalf("call %d thinking count %d", calls, count)
				}
				if success && calls == 2 {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(mockSSEResponse("msg", s.Model, "answer", 1, 1)))}, nil
				}
				return &http.Response{StatusCode: 400, Header: http.Header{"Request-Id": {"safe-request"}}, Body: io.NopCloser(strings.NewReader("Invalid `signature` secret-error-body secret-thinking secret-signature"))}, nil
			}}}
			r := &llm.Request{Messages: append(thinkingRound("a"), thinkingRound("b")...), OnRetry: func(e llm.RetryEvent) { retries = append(retries, e) }}
			before := thinkingJSON(t, r.Messages)
			out, err := s.Do(context.Background(), r)
			if (err == nil) != success || calls != 2 || len(retries) != 1 {
				t.Fatalf("success=%t err=%v calls=%d retries=%d", success, err, calls, len(retries))
			}
			if success && out.Content[0].Text != "answer" {
				t.Fatal("lost recovered output")
			}
			if !strings.Contains(retries[0].Err, "invalid thinking signature") || !strings.Contains(retries[0].Err, "without historical thinking") {
				t.Fatalf("missing recovery reason: %+v", retries[0])
			}
			if !bytes.Equal(before, thinkingJSON(t, r.Messages)) {
				t.Fatal("recovery mutated persisted history")
			}
			if strings.Contains(logs.String(), "secret-") || (err != nil && strings.Contains(err.Error(), "secret-")) || strings.Contains(retries[0].Err, "secret-") {
				t.Fatal("signature recovery leaked sensitive diagnostics")
			}
			for _, key := range []string{"safe-request", "invalid_thinking_signature", "anthropic", s.Model} {
				if !strings.Contains(logs.String(), key) {
					t.Errorf("missing safe log field %q", key)
				}
			}
		})
	}
}

func TestThinkingBindingProviderDiagnostics(t *testing.T) {
	entry := llm.InputTransformation{Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "prefix_binding_mismatch"}
	start := streamEvent{Type: "message_start", Message: &response{ID: "msg", Model: Claude46Opus, Role: "assistant", InputTransformations: []llm.InputTransformation{entry, entry}}}
	stream := "data: " + string(thinkingJSON(t, start)) + "\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"secret-thinking\",\"signature\":\"secret-signature\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	s := &Service{Model: Claude46Opus, ThinkingLevel: llm.ThinkingLevelLow, HTTPC: &http.Client{Transport: &roundTripFunc{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Request-Id": {"req-safe"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
	}}}}
	out, err := s.Do(context.Background(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage("hello")}})
	if err != nil || len(out.InputTransformations) != 1 {
		t.Fatalf("response metadata not propagated: err=%v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
		t.Fatalf("expected exactly one structured log: %v", err)
	}
	for key, want := range map[string]string{"provider": "anthropic", "model": Claude46Opus, "request_id": "req-safe", "response_id": "msg", "type": entry.Type, "path": entry.Path, "reason": entry.Reason} {
		if record[key] != want {
			t.Errorf("log %s=%v want=%s", key, record[key], want)
		}
	}
	if strings.Contains(logs.String(), "secret-") {
		t.Fatal("provider log leaked thinking/signature")
	}
}
