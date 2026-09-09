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

func TestInputTransformationWarnings(t *testing.T) {
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
	l.recordInputTransformationWarnings(context.Background(), resp, seen)
	l.recordInputTransformationWarnings(context.Background(), resp, seen)
	if len(warnings) != 3 {
		t.Fatalf("warnings = %v; want three grouped reasons without duplicates", warnings)
	}
	for i, want := range []string{"2 historical thinking block(s): the preceding conversation", "serving model cannot read", "did not supply a recognized reason"} {
		if !strings.Contains(warnings[i], want) {
			t.Errorf("warning %d = %q, want %q", i, warnings[i], want)
		}
	}
	if len(l.history) != 0 {
		t.Fatal("warning was added to LLM history")
	}
	// No metadata, or no persistence callback, needs no special handling.
	l.recordInputTransformationWarnings(context.Background(), &llm.Response{}, seen)
	NewLoop(Config{}).recordInputTransformationWarnings(context.Background(), resp, seen)
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

func TestInputTransformationWarningsAcrossPauses(t *testing.T) {
	for _, writeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "persisted", true: "warning-write-fails"}[writeFails], func(t *testing.T) {
			s := &transformationService{responses: transformationPauseChain()}
			var recorded []llm.Message
			var warnings []string
			l := NewLoop(Config{
				LLM: s,
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
			if len(warnings) != 2 || s.calls != 3 {
				t.Fatalf("warnings=%v calls=%d", warnings, s.calls)
			}
			last := recorded[len(recorded)-1]
			if len(last.Content) != 3 || last.Content[2].Text != "answer" || !last.EndOfTurn {
				t.Fatal("lost model output when recording warnings")
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

func TestInputTransformationWarningSurvivesContinuationError(t *testing.T) {
	s := &transformationService{responses: transformationPauseChain()[:1]}
	warnings := 0
	l := NewLoop(Config{
		LLM:           s,
		RecordWarning: func(context.Context, string) error { warnings++; return nil },
		RecordMessage: func(context.Context, llm.Message, llm.Usage, []llm.PurposedUsage) error { return nil },
	})
	l.QueueUserMessage(llm.UserStringMessage("question"))
	if err := l.ProcessOneTurn(context.Background()); err == nil || warnings != 1 {
		t.Fatalf("err=%v warnings=%d", err, warnings)
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
	l.recordInputTransformationWarnings(context.Background(), resp, seen)
	l.recordInputTransformationWarnings(context.Background(), resp, seen)
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
