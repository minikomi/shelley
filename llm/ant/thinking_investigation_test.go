package ant

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"shelley.exe.dev/llm"
)

// Test-only old policy: compare age-based removal with preserved production.
// Keep request settings and corruption filters identical between arms.
func oldThinkingPolicyRequest(s *Service, r *llm.Request) *request {
	out := s.fromLLMRequest(r)
	out.Messages = nil
	src := sanitizeServerToolBlocks(r.Messages)
	last := -1
	for i, m := range src {
		if m.Role == llm.MessageRoleAssistant {
			last = i
		}
	}
	for i, m := range src {
		if m.Role == llm.MessageRoleAssistant && i != last {
			m = stripThinkingBlocks(m)
		}
		if msg := fromLLMMessage(m); len(msg.Content) > 0 {
			out.Messages = append(out.Messages, msg)
		}
	}
	return out
}

func thinkingJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func thinkingRound(id string) []llm.Message {
	return []llm.Message{
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{
			{Type: llm.ContentTypeThinking, Thinking: "synthetic reasoning", Signature: "synthetic-" + id},
			{Type: llm.ContentTypeRedactedThinking, Data: "synthetic-redacted"},
			{Type: llm.ContentTypeToolUse, ID: id, ToolName: "lookup", ToolInput: json.RawMessage(`{"key":"A"}`)},
		}},
		{Role: llm.MessageRoleUser, Content: []llm.Content{
			{Type: llm.ContentTypeToolResult, ToolUseID: id, ToolResult: llm.TextContent("23"), Cache: true},
		}},
	}
}

func TestThinkingInvestigationPrefixChurn(t *testing.T) {
	s := &Service{Model: Claude46Sonnet, ThinkingLevel: llm.ThinkingLevelLow}
	r := &llm.Request{Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("Look up A, then B.")}}}
	r.Messages = append(r.Messages, thinkingRound("a")...)
	before := oldThinkingPolicyRequest(s, r)
	preservedBefore := s.fromLLMRequest(r)
	if !reflect.DeepEqual(before, preservedBefore) {
		t.Fatal("one-assistant requests should be identical")
	}
	r.Messages = append(r.Messages, thinkingRound("b")...)
	after := oldThinkingPolicyRequest(s, r)
	preservedAfter := s.fromLLMRequest(r)
	if !reflect.DeepEqual(before.Messages[0], after.Messages[0]) ||
		!reflect.DeepEqual(before.Messages[2], after.Messages[2]) {
		t.Fatal("user and cached tool-result blocks should not change")
	}
	if bytes.Equal(thinkingJSON(t, before.Messages[1]), thinkingJSON(t, after.Messages[1])) {
		t.Fatal("aging the assistant should change the earlier serialized message")
	}
	if !bytes.Equal(thinkingJSON(t, preservedBefore.Messages), thinkingJSON(t, preservedAfter.Messages[:3])) {
		t.Fatal("preservation should leave the earlier serialized messages identical")
	}
	// Everything before messages (including tools, system and thinking settings)
	// remains identical. Byte-prefix churn is not itself a cache-miss measurement.
	after.Messages, preservedAfter.Messages = nil, nil
	if !bytes.Equal(thinkingJSON(t, after), thinkingJSON(t, preservedAfter)) {
		t.Fatal("test-only converter changed non-message settings")
	}
}

func TestThinkingInvestigationMultipleToolRounds(t *testing.T) {
	r := &llm.Request{Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent("Do three lookups.")}}}
	for _, id := range []string{"a", "b", "c"} {
		r.Messages = append(r.Messages, thinkingRound(id)...)
	}
	s := &Service{Model: Claude46Sonnet}
	stock, preserved := oldThinkingPolicyRequest(s, r), s.fromLLMRequest(r)
	for i := 1; i < len(r.Messages); i += 2 {
		want := preserved.Messages[i].Content
		if i < 5 {
			want = want[2:]
		}
		if !reflect.DeepEqual(stock.Messages[i].Content, want) {
			t.Fatalf("assistant message %d: unexpected retained blocks", i)
		}
		if !reflect.DeepEqual(stock.Messages[i+1], preserved.Messages[i+1]) {
			t.Fatalf("tool result after message %d changed", i)
		}
	}
	// No new ordinary user turn occurred: the fence is per assistant message,
	// not the entire multi-tool assistant turn.
}

func TestThinkingInvestigationImmutabilityAndSanitizers(t *testing.T) {
	r := &llm.Request{
		System:   []llm.SystemContent{{Text: "Synthetic system", Cache: true}},
		Tools:    []*llm.Tool{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages: append(thinkingRound("a"), thinkingRound("b")...),
	}
	r.Messages[0].Content = append(r.Messages[0].Content,
		llm.Content{Type: llm.ContentTypeThinking, Thinking: "unsigned"},
		llm.Content{Type: llm.ContentTypeText},
		llm.Content{Type: llm.ContentTypeServerToolUse, ID: "orphan", ToolName: "web_search"},
	)
	s := &Service{Model: Claude46Sonnet}
	before := thinkingJSON(t, r)
	stock := thinkingJSON(t, s.fromLLMRequest(r))
	preserved := s.fromLLMRequest(r)
	s.fromLLMRequestStrippingAllThinking(r)
	if !bytes.Equal(before, thinkingJSON(t, r)) {
		t.Fatal("conversion mutated the source history")
	}
	if !bytes.Equal(stock, thinkingJSON(t, s.fromLLMRequest(r))) {
		t.Fatal("repeated conversion changed serialization")
	}
	if len(preserved.Messages[0].Content) != 3 ||
		preserved.Messages[0].Content[0].Signature != "synthetic-a" ||
		preserved.Messages[0].Content[1].Type != "redacted_thinking" {
		t.Fatal("preserve-all must retain signed/redacted blocks but still sanitize invalid blocks")
	}
}
