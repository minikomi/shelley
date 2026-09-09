package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func TestInputTransformationsDoNotCreateWarnings(t *testing.T) {
	var warnings []string
	l := NewLoop(Config{RecordWarning: func(_ context.Context, text string) error {
		warnings = append(warnings, text)
		return nil
	}})
	drop := func(path, reason string) llm.InputTransformation {
		return llm.InputTransformation{Type: "thinking_dropped", Path: path, Reason: reason}
	}
	prefix := drop("messages.1.content.0", "prefix_binding_mismatch")
	resp := &llm.Response{InputTransformations: []llm.InputTransformation{
		prefix, prefix, drop("messages.3.content.0", "prefix_binding_mismatch"),
		drop("messages.5.content.0", "model_binding_mismatch"),
		drop("messages.9.content.0", "future_reason"),
		{Type: "future_type", Path: "irrelevant", Reason: "prefix_binding_mismatch"},
	}}
	seen := make(map[llm.InputTransformation]bool)
	l.logInputTransformations(context.Background(), resp, seen)
	l.logInputTransformations(context.Background(), resp, seen)
	if len(warnings) != 0 {
		t.Fatalf("unexpected drop banners: %v", warnings)
	}
	if len(l.history) != 0 {
		t.Fatal("warning was added to LLM history")
	}
	// No metadata, or no persistence callback, needs no special handling.
	l.logInputTransformations(context.Background(), &llm.Response{}, seen)
	NewLoop(Config{}).logInputTransformations(context.Background(), resp, seen)
	// Unrelated warnings still use the existing callback/UI.
	event := llm.RetryEvent{}
	l.recordRetryWarning(context.Background())(event)
	if len(warnings) != 1 || warnings[0] != llm.FormatRetryEvent(event) {
		t.Fatalf("retry warning was affected: %v", warnings)
	}
}

type transformationService struct {
	pauseLLMService
	responses []*llm.Response
}

func (s *transformationService) Do(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.lastSent = append(s.lastSent, req.Messages)
	s.calls++
	if s.calls > len(s.responses) {
		return nil, errors.New("continuation failed")
	}
	return s.responses[s.calls-1], nil
}

func transformationPauseChain() []*llm.Response {
	a := llm.InputTransformation{Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "prefix_binding_mismatch"}
	b := llm.InputTransformation{Type: "thinking_dropped", Path: "messages.3.content.0", Reason: "model_binding_mismatch"}
	return []*llm.Response{
		{Role: llm.MessageRoleAssistant, Content: llm.TextContent("first"), StopReason: llm.StopReasonPause, InputTransformations: []llm.InputTransformation{a}},
		{Role: llm.MessageRoleAssistant, Content: llm.TextContent("second"), StopReason: llm.StopReasonPause, InputTransformations: []llm.InputTransformation{a, b}},
		{Role: llm.MessageRoleAssistant, Content: llm.TextContent("answer"), StopReason: llm.StopReasonEndTurn},
	}
}

func TestInputTransformationLogsAcrossPauses(t *testing.T) {
	for _, writeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "callback-succeeds", true: "callback-would-fail"}[writeFails], func(t *testing.T) {
			s := &transformationService{responses: transformationPauseChain()}
			var logs bytes.Buffer
			var recorded []llm.Message
			var warnings []string
			l := NewLoop(Config{
				LLM:    s,
				Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
				RecordWarning: func(_ context.Context, text string) error {
					warnings = append(warnings, text)
					if writeFails {
						return errors.New("warning persistence failed")
					}
					return nil
				},
				RecordMessage: func(_ context.Context, m llm.Message, _ llm.Usage, _ []llm.PurposedUsage) error {
					recorded = append(recorded, m)
					return nil
				},
			})
			l.QueueUserMessage(llm.UserStringMessage("question"))
			if err := l.ProcessOneTurn(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 0 || s.calls != 3 {
				t.Fatalf("warnings=%v calls=%d", warnings, s.calls)
			}
			if got := strings.Count(logs.String(), `"msg":"LLM input transformation"`); got != 2 {
				t.Fatalf("diagnostic logs=%d, want two distinct pause diagnostics", got)
			}
			last := recorded[len(recorded)-1]
			if len(last.Content) != 3 || last.Content[2].Text != "answer" || !last.EndOfTurn {
				t.Fatal("lost model output when logging diagnostics")
			}
			for _, history := range []any{s.lastSent, l.history, recorded} {
				data, err := json.Marshal(history)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(data, []byte("historical thinking")) || bytes.Contains(data, []byte("binding_mismatch")) {
					t.Fatal("warning or metadata was sent back to LLM/history")
				}
			}
		})
	}
}

func TestPausedTurnMergesInputTransformations(t *testing.T) {
	responses := transformationPauseChain()
	s := &transformationService{responses: responses[1:]}
	l := NewLoop(Config{})
	resolved, err := l.resolvePausedTurn(context.Background(), func(r *llm.Request) (*llm.Response, error) {
		return s.Do(context.Background(), r)
	}, &llm.Request{}, responses[0])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolved.InputTransformations, responses[1].InputTransformations) {
		t.Fatalf("lost/duplicated pause metadata: %+v", resolved.InputTransformations)
	}
	if len(responses[0].Content) != 1 || len(responses[0].InputTransformations) != 1 {
		t.Fatal("merge changed initial response")
	}
}

func TestInputTransformationLogSurvivesContinuationError(t *testing.T) {
	s := &transformationService{responses: transformationPauseChain()[:1]}
	var logs bytes.Buffer
	warnings := 0
	l := NewLoop(Config{
		LLM:           s,
		Logger:        slog.New(slog.NewJSONHandler(&logs, nil)),
		RecordWarning: func(context.Context, string) error { warnings++; return nil },
		RecordMessage: func(context.Context, llm.Message, llm.Usage, []llm.PurposedUsage) error { return nil },
	})
	l.QueueUserMessage(llm.UserStringMessage("question"))
	if err := l.ProcessOneTurn(context.Background()); err == nil || warnings != 0 {
		t.Fatalf("err=%v warnings=%d", err, warnings)
	}
	if !strings.Contains(logs.String(), `"msg":"LLM input transformation"`) {
		t.Fatal("lost successful pause leg diagnostic after continuation failed")
	}
}

func TestInputTransformationLogsWithoutWarningCallback(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil)).With("conversationID", "conversation-safe")
	l := NewLoop(Config{Logger: logger})
	entry := llm.InputTransformation{Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "prefix_binding_mismatch"}
	unknown := llm.InputTransformation{Type: "future_type", Path: "messages.3.content.0", Reason: "future_reason"}
	resp := &llm.Response{
		ID: "response-safe", Model: "claude-mythos-preview",
		Content:              []llm.Content{{Type: llm.ContentTypeThinking, Thinking: "secret-thinking", Signature: "secret-signature"}},
		InputTransformations: []llm.InputTransformation{entry, entry, unknown},
	}
	seen := make(map[llm.InputTransformation]bool)
	l.logInputTransformations(context.Background(), resp, seen)
	l.logInputTransformations(context.Background(), resp, seen)
	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d logs, want one per distinct transformation", len(lines))
	}
	for i, want := range []llm.InputTransformation{entry, unknown} {
		var record map[string]any
		if err := json.Unmarshal(lines[i], &record); err != nil {
			t.Fatal(err)
		}
		for key, value := range map[string]string{
			"conversationID": "conversation-safe", "response_id": resp.ID, "model": resp.Model,
			"type": want.Type, "path": want.Path, "reason": want.Reason,
		} {
			if record[key] != value {
				t.Errorf("log %s=%v, want %s", key, record[key], value)
			}
		}
	}
	if strings.Contains(logs.String(), "secret-") || len(l.history) != 0 {
		t.Fatal("diagnostics leaked model content or entered history")
	}
}

func TestThinkingDropLogsAcrossRequestsAndLoops(t *testing.T) {
	var logs bytes.Buffer
	var warnings []string
	history := []llm.Message{{Role: llm.MessageRoleAssistant, Content: []llm.Content{
		{Type: llm.ContentTypeThinking, Thinking: "secret-thinking", Signature: "secret-signature"},
	}}}
	before, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{
		History: history,
		Logger:  slog.New(slog.NewJSONHandler(&logs, nil)),
		RecordWarning: func(_ context.Context, text string) error {
			warnings = append(warnings, text)
			return nil
		},
	}
	l := NewLoop(config)
	for i, tc := range []struct {
		model, reason string
		paths         []string
		recreate      bool
	}{
		{"claude-opus-5", "model_binding_mismatch", []string{"messages.1.content.0"}, false},
		{"claude-opus-5", "model_binding_mismatch", []string{"messages.1.content.0", "messages.3.content.0"}, false},
		{"claude-opus-5", "model_binding_mismatch", []string{"messages.5.content.0"}, true},
		{"claude-opus-5", "prefix_binding_mismatch", []string{"messages.5.content.0"}, false},
		{"other-model", "model_binding_mismatch", []string{"messages.5.content.0"}, true},
		{"claude-opus-5", "model_binding_mismatch", []string{"messages.9.content.0"}, true},
	} {
		if tc.recreate {
			l = NewLoop(config)
		}
		resp := &llm.Response{Model: tc.model}
		for _, path := range tc.paths {
			resp.InputTransformations = append(resp.InputTransformations, llm.InputTransformation{
				Type: "thinking_dropped", Path: path, Reason: tc.reason,
			})
		}
		// Each new tool round/user turn gets a fresh diagnostic log map.
		l.logInputTransformations(context.Background(), resp, make(map[llm.InputTransformation]bool))
		if len(warnings) != 0 {
			t.Fatalf("request %d: unexpected drop banners=%v", i, warnings)
		}
		after, err := json.Marshal(l.GetHistory())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("request %d mutated thinking/history: %s", i, after)
		}
	}
	if got := strings.Count(logs.String(), `"msg":"LLM input transformation"`); got != 7 {
		t.Fatalf("got %d diagnostic logs, want all 7 paths across requests", got)
	}
	if strings.Contains(logs.String(), "secret-") {
		t.Fatal("diagnostics leaked thinking/signatures")
	}
}

func TestThinkingDropLogsAcrossToolRoundsAndUserTurns(t *testing.T) {
	entry := llm.InputTransformation{Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "model_binding_mismatch"}
	s := &transformationService{responses: []*llm.Response{
		{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonToolUse, Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: "echo-1", ToolName: "echo", ToolInput: json.RawMessage(`{}`),
		}}},
		{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("first answer")},
		{Role: llm.MessageRoleAssistant, StopReason: llm.StopReasonEndTurn, Content: llm.TextContent("second answer")},
	}}
	for _, resp := range s.responses {
		resp.Model = "claude-opus-5"
		resp.InputTransformations = []llm.InputTransformation{entry}
	}
	var logs bytes.Buffer
	warnings := 0
	l := NewLoop(Config{
		LLM:           s,
		Logger:        slog.New(slog.NewJSONHandler(&logs, nil)),
		RecordWarning: func(context.Context, string) error { warnings++; return nil },
		RecordMessage: func(context.Context, llm.Message, llm.Usage, []llm.PurposedUsage) error { return nil },
		Tools: []*llm.Tool{{
			Name: "echo", InputSchema: llm.MustSchema(`{"type":"object","properties":{}}`),
			Run: func(context.Context, json.RawMessage) llm.ToolOut {
				return llm.ToolOut{LLMContent: llm.TextContent("echoed")}
			},
		}},
	})
	for range 2 {
		l.QueueUserMessage(llm.UserStringMessage("question"))
		if err := l.ProcessOneTurn(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if warnings != 0 || s.calls != 3 {
		t.Fatalf("warnings=%d calls=%d, want no banners over three requests", warnings, s.calls)
	}
	if got := strings.Count(logs.String(), `"msg":"LLM input transformation"`); got != 3 {
		t.Fatalf("diagnostic logs=%d, want all three requests", got)
	}
}
