package ant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
)

// All signatures/data are synthetic: these tests prove structure, not acceptance.
func historyReliabilityRequest() *llm.Request {
	r := &llm.Request{
		System:   []llm.SystemContent{{Text: "synthetic system", Cache: true}},
		Tools:    []*llm.Tool{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []llm.Message{llm.UserStringMessage("Look up A then B.")},
	}
	r.Messages = append(r.Messages, thinkingRound("a")...)
	return r
}

// Exercise actual Do, including citation preparation, JSON encoding and headers;
// the transport cannot reach a provider. Do not print request bodies on failure.
func historyReliabilityWire(t *testing.T, s Service, r *llm.Request) (*request, []byte, string) {
	t.Helper()
	var wire request
	var payload []byte
	var beta string
	calls := 0
	s.HTTPC = &http.Client{Transport: &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
		calls++
		var err error
		payload, err = io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &wire); err != nil {
			t.Fatal(err)
		}
		beta = req.Header.Get("Anthropic-Beta")
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(mockSSEResponse("synthetic", s.Model, "ok", 1, 1)))}, nil
	}}}
	if _, err := s.Do(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d", calls)
	}
	return &wire, payload, beta
}

func historyReliabilityReload(t *testing.T, r *llm.Request) *llm.Request {
	t.Helper()
	out := *r
	out.Messages = make([]llm.Message, len(r.Messages))
	// Storage marshals each llm.Message, not the provider request/response.
	for i, msg := range r.Messages {
		if err := json.Unmarshal(thinkingJSON(t, msg), &out.Messages[i]); err != nil {
			t.Fatal(err)
		}
	}
	return &out
}

func historyReliabilityThinkingCount(w *request) int {
	n := 0
	for _, m := range w.Messages {
		for _, c := range m.Content {
			if c.Type == "thinking" || c.Type == "redacted_thinking" {
				n++
			}
		}
	}
	return n
}

func TestThinkingHistoryAppendOnlyStorageRoundTrip(t *testing.T) {
	s := Service{Model: ClaudeFable5, ThinkingLevel: llm.ThinkingLevelLow}
	r := historyReliabilityRequest()
	// Omitted-display thinking is signed even when its text is empty.
	r.Messages[1].Content[0].Thinking = ""
	r.Messages[1].Content[2].ToolInput = json.RawMessage("{ \"key\": \"A<&>\", \"n\": 1.0 }")
	before := thinkingJSON(t, r)
	first, payload, _ := historyReliabilityWire(t, s, r)
	_, reloadedPayload, _ := historyReliabilityWire(t, s, historyReliabilityReload(t, r))
	if !bytes.Equal(payload, reloadedPayload) {
		t.Fatal("message storage round trip changed wire bytes")
	}
	if first.Messages[1].Content[0].Thinking == nil || *first.Messages[1].Content[0].Thinking != "" {
		t.Fatal("signed empty thinking lost its required thinking field")
	}
	if !bytes.Equal(before, thinkingJSON(t, r)) {
		t.Fatal("serialization mutated source")
	}
	for _, id := range []string{"b", "c"} {
		r.Messages = append(r.Messages, thinkingRound(id)...)
		current, _, _ := historyReliabilityWire(t, s, r)
		if !bytes.Equal(thinkingJSON(t, first.Messages), thinkingJSON(t, current.Messages[:len(first.Messages)])) {
			t.Fatal("append-only tool round changed earlier wire messages")
		}
		if historyReliabilityThinkingCount(current) != len(r.Messages)-1 {
			t.Fatal("signed/redacted blocks lost")
		}
		first = current
	}
}

func TestThinkingHistoryModelSwitchAndSettings(t *testing.T) {
	r := historyReliabilityRequest()
	s := Service{Model: ClaudeFable5, ThinkingLevel: llm.ThinkingLevelLow}
	original := thinkingJSON(t, r)
	a, aPayload, _ := historyReliabilityWire(t, s, r)
	s.Model = Claude5Opus
	b, _, _ := historyReliabilityWire(t, s, historyReliabilityReload(t, r))
	if a.Model == b.Model || !bytes.Equal(thinkingJSON(t, a.Messages), thinkingJSON(t, b.Messages)) {
		t.Fatal("model switch must change model without editing signed history")
	}
	s.Model = ClaudeFable5
	_, backPayload, _ := historyReliabilityWire(t, s, historyReliabilityReload(t, r))
	if !bytes.Equal(aPayload, backPayload) {
		t.Fatal("A-B-A without new messages changed A request")
	}
	// In a real B turn its blocks remain when switching back to A; no model
	// provenance is stored in llm.Content or consulted by this adapter.
	r.Messages = append(r.Messages, thinkingRound("b-model")...)
	back, _, _ := historyReliabilityWire(t, s, r)
	if historyReliabilityThinkingCount(back) != 4 {
		t.Fatal("switch-back deleted either model's history")
	}
	r.Messages = r.Messages[:3]
	for _, tc := range []struct {
		name    string
		model   string
		level   llm.ThinkingLevel
		binding bool
	}{
		{"low", ClaudeFable5, llm.ThinkingLevelLow, true},
		{"high", ClaudeFable5, llm.ThinkingLevelHigh, true},
		{"off", ClaudeFable5, llm.ThinkingLevelOff, false},
		{"compatible-provider", "minimax-m2.5", llm.ThinkingLevelLow, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *r
			copy.ThinkingLevel = tc.level
			s.Model = tc.model
			w, _, beta := historyReliabilityWire(t, s, &copy)
			binding := w.Thinking != nil && w.Thinking.BlockBinding != nil
			if binding != tc.binding || (beta == thinkingBindingBeta) != tc.binding {
				t.Fatal("binding/header gating mismatch")
			}
			if tc.level == llm.ThinkingLevelOff && w.Thinking != nil {
				t.Fatal("off enabled request thinking")
			}
			if !bytes.Equal(thinkingJSON(t, a.Messages), thinkingJSON(t, w.Messages)) {
				t.Fatal("settings change altered history")
			}
		})
	}
	if !bytes.Equal(original, thinkingJSON(t, r)) {
		t.Fatal("model/settings conversion mutated source")
	}
}

func TestThinkingHistoryChangedPrefixPreservesSource(t *testing.T) {
	s := Service{Model: ClaudeFable5, ThinkingLevel: llm.ThinkingLevelLow}
	for _, scenario := range []string{"system", "tool-schema", "earlier-summary-edit", "compact-tail"} {
		t.Run(scenario, func(t *testing.T) {
			r := historyReliabilityRequest()
			r.Messages = append(r.Messages, thinkingRound("b")...)
			baseline, payload, _ := historyReliabilityWire(t, s, r)
			switch scenario {
			case "system":
				r.System[0].Text += " changed"
			case "tool-schema":
				r.Tools[0].InputSchema = json.RawMessage(`{"type":"object","description":"changed"}`)
			case "earlier-summary-edit":
				r.Messages[0].Content[0].Text = "edited older distilled summary"
			case "compact-tail":
				// performPiDistillation prepends a user summary and copies a
				// verbatim tail; findPiCutPoint may start at an assistant.
				r.Messages = append([]llm.Message{llm.UserStringMessage("compaction summary")}, r.Messages[3:]...)
			}
			source := thinkingJSON(t, r)
			w, changed, _ := historyReliabilityWire(t, s, r)
			if bytes.Equal(payload, changed) {
				t.Fatal("prefix edit had no wire effect")
			}
			if !bytes.Equal(source, thinkingJSON(t, r)) {
				t.Fatal("conversion mutated edited source")
			}
			want := baseline.Messages[3:]
			got := w.Messages[len(w.Messages)-2:]
			if !bytes.Equal(thinkingJSON(t, want), thinkingJSON(t, got)) {
				t.Fatal("changed prefix rewrote retained tail")
			}
			if w.Thinking.BlockBinding.PrefixMismatchBehavior != "drop_block" {
				t.Fatal("changed prefix lacks binding policy")
			}
		})
	}
}

func TestThinkingHistoryCitationPreparationStable(t *testing.T) {
	s := Service{Model: ClaudeFable5, ThinkingLevel: llm.ThinkingLevelLow}
	for _, kind := range []string{"web_search_result_location", "url_citation"} {
		t.Run(kind, func(t *testing.T) {
			r := historyReliabilityRequest()
			c := llm.Content{Type: llm.ContentTypeText, Text: "cited answer", Citations: json.RawMessage(`[{"type":"` + kind + `","url":"https://example.invalid/source","encrypted_index":"synthetic-opaque"}]`)}
			r.Messages[1].Content = append(r.Messages[1].Content, c)
			r.Messages = append(r.Messages, thinkingRound("b")...)
			source := thinkingJSON(t, r)
			w, first, _ := historyReliabilityWire(t, s, r)
			_, second, _ := historyReliabilityWire(t, s, historyReliabilityReload(t, r))
			if !bytes.Equal(first, second) || !bytes.Equal(source, thinkingJSON(t, r)) {
				t.Fatal("citation preparation unstable or mutated storage")
			}
			text := w.Messages[1].Content[3]
			if kind == "url_citation" {
				if len(text.Citations) != 0 || !strings.Contains(*text.Text, "Source: https://example.invalid/source") {
					t.Fatal("foreign citation not converted")
				}
			} else if *text.Text != c.Text || !bytes.Equal(text.Citations, c.Citations) {
				t.Fatal("native opaque citation changed")
			}
			if historyReliabilityThinkingCount(w) != 4 {
				t.Fatal("citation conversion stripped thinking")
			}
		})
	}
}

func TestThinkingHistoryServerToolSanitizerPrefixChange(t *testing.T) {
	s := Service{Model: ClaudeFable5, ThinkingLevel: llm.ThinkingLevelLow}
	r := historyReliabilityRequest()
	r.Messages = r.Messages[:2]
	r.Messages[1].Content = append(r.Messages[1].Content, llm.Content{Type: llm.ContentTypeServerToolUse, ID: "server", ToolName: "web_search", ToolInput: json.RawMessage(`{}`)})
	before, _, _ := historyReliabilityWire(t, s, r)
	if len(before.Messages[1].Content) != 4 {
		t.Fatal("final orphan continuation point removed")
	}
	// Split server results are explicitly handled by sanitizeServerToolBlocks.
	// Normal resolvePausedTurn merges blocks; this represents persisted broken
	// history, not a claim that every ordinary tool round takes this path.
	r.Messages = append(r.Messages, thinkingRound("a")[1], llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{
		{Type: llm.ContentTypeWebSearchToolResult, ToolUseID: "server"},
		{Type: llm.ContentTypeThinking, Thinking: "later", Signature: "synthetic-later"},
	}})
	source := thinkingJSON(t, r)
	after, _, _ := historyReliabilityWire(t, s, r)
	if len(after.Messages[1].Content) != 3 || len(after.Messages[3].Content) != 1 {
		t.Fatal("split server pair not sanitized")
	}
	if !bytes.Equal(thinkingJSON(t, before.Messages[1].Content[:3]), thinkingJSON(t, after.Messages[1].Content)) {
		t.Fatal("sanitizer changed unrelated blocks")
	}
	if historyReliabilityThinkingCount(after) != 3 || !bytes.Equal(source, thinkingJSON(t, r)) {
		t.Fatal("sanitizer removed thinking or mutated source")
	}
	// Repair within the same assistant message is retained, matching merged pauses.
	r.Messages[1].Content = append(r.Messages[1].Content, llm.Content{Type: llm.ContentTypeWebSearchToolResult, ToolUseID: "server"})
	paired, _, _ := historyReliabilityWire(t, s, r)
	if len(paired.Messages[1].Content) != 5 {
		t.Fatal("same-message server pair removed")
	}
}

func TestThinkingHistoryDiagnosticsExcludedAfterStorage(t *testing.T) {
	response := &llm.Response{
		Role: llm.MessageRoleAssistant, Content: thinkingRound("a")[0].Content,
		StopReason:           llm.StopReasonToolUse,
		InputTransformations: []llm.InputTransformation{{Type: "thinking_dropped", Path: "messages.1.content.0", Reason: "model_binding_mismatch"}},
	}
	r := historyReliabilityRequest()
	r.Messages[1] = response.ToMessage()
	saved := thinkingJSON(t, r.Messages[1])
	r = historyReliabilityReload(t, r)
	_, wire, _ := historyReliabilityWire(t, Service{Model: ClaudeFable5, ThinkingLevel: llm.ThinkingLevelLow}, r)
	for _, marker := range []string{"thinking_dropped", "model_binding_mismatch", "InputTransformations", "input_transformations"} {
		if bytes.Contains(saved, []byte(marker)) || bytes.Contains(wire, []byte(marker)) {
			t.Fatal("diagnostic leaked into saved/model history")
		}
	}
	if len(response.InputTransformations) != 1 {
		t.Fatal("history conversion removed response diagnostics")
	}
}

// Unlike thinkingRound's fixed cache flags, production moves the cache marker
// between requests. That changes message JSON, but not signed content itself.
func TestThinkingHistoryActualLoopCacheMarkerMoves(t *testing.T) {
	r := historyReliabilityRequest()
	r.Messages[2].Content[0].Cache = false
	source := thinkingJSON(t, r.Messages)
	var wires []request
	s := &Service{Model: ClaudeFable5, ThinkingLevel: llm.ThinkingLevelLow}
	s.HTTPC = &http.Client{Transport: &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
		var w request
		if err := json.NewDecoder(req.Body).Decode(&w); err != nil {
			t.Fatal(err)
		}
		wires = append(wires, w)
		body := mockSSEResponse("end", s.Model, "done", 1, 1)
		if len(wires) == 1 {
			var stream strings.Builder
			emit := func(v any) { stream.WriteString("data: " + string(thinkingJSON(t, v)) + "\n\n") }
			emit(streamEvent{Type: "message_start", Message: &response{ID: "b", Model: s.Model, Role: "assistant"}})
			emit(streamEvent{Type: "content_block_start", ContentBlock: &content{Type: "tool_use", ID: "b", ToolName: "lookup", ToolInput: json.RawMessage(`{}`)}})
			emit(streamEvent{Type: "content_block_stop"})
			emit(streamEvent{Type: "message_delta", Delta: thinkingJSON(t, streamDelta{StopReason: "tool_use"})})
			emit(streamEvent{Type: "message_stop"})
			body = stream.String()
		}
		if len(wires) > 2 {
			t.Fatal("unexpected extra tool round")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}}
	tool := *r.Tools[0]
	tool.Run = func(context.Context, json.RawMessage) llm.ToolOut {
		return llm.ToolOut{LLMContent: llm.TextContent("23")}
	}
	var saved []llm.Message
	l := loop.NewLoop(loop.Config{
		LLM: s, History: r.Messages, Tools: []*llm.Tool{&tool}, System: r.System,
		RecordMessage: func(_ context.Context, m llm.Message, _ llm.Usage, _ []llm.PurposedUsage) error {
			saved = append(saved, m)
			return nil
		},
	})
	if err := l.ProcessOneTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(wires) != 2 {
		t.Fatalf("wire requests = %d", len(wires))
	}
	first, second := wires[0], wires[1]
	if first.Messages[2].Content[0].CacheControl == nil || second.Messages[2].Content[0].CacheControl != nil || second.Messages[4].Content[0].CacheControl == nil {
		t.Fatal("expected cache marker to move from old to new tool result")
	}
	if bytes.Equal(thinkingJSON(t, first.Messages), thinkingJSON(t, second.Messages[:3])) {
		t.Fatal("expected metadata-only prefix change")
	}
	first.Messages[2].Content[0].CacheControl = nil
	if !bytes.Equal(thinkingJSON(t, first.Messages), thinkingJSON(t, second.Messages[:3])) {
		t.Fatal("loop changed historical content beyond cache metadata")
	}
	if !bytes.Equal(source, thinkingJSON(t, r.Messages)) {
		t.Fatal("loop cache preparation mutated source")
	}
	for _, m := range saved {
		for _, c := range m.Content {
			if c.Cache {
				t.Fatal("request cache flag leaked into saved output")
			}
		}
	}
}
