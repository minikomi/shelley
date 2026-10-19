package models

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models/modelsdev"
)

// scriptedTransport returns the next canned JSON body for each request and
// records every outgoing request body.
type scriptedTransport struct {
	responses []string
	bodies    [][]byte
}

func (s *scriptedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	s.bodies = append(s.bodies, body)
	next := s.responses[len(s.bodies)-1]
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(next)), Request: r,
	}, nil
}

func chatToolTurn(reasoning, call string) string {
	return `{"id":"c","object":"chat.completion","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","reasoning_content":"` + reasoning +
		`","tool_calls":[{"id":"` + call + `","type":"function","function":{"name":"fixture","arguments":"{}"}}]}}]}`
}

func responsesToolTurn(id, encrypted, call string) string {
	return `{"id":"r","status":"completed","output":[{"type":"reasoning","id":"` + id + `","encrypted_content":"` + encrypted +
		`","summary":[]},{"type":"function_call","call_id":"` + call + `","name":"fixture","arguments":"{}"}]}`
}

// toolLoop drives two tool continuations and a final answer through svc,
// feeding each response back as history, and returns the replay state seen in
// the second and third request bodies.
func toolLoop(t *testing.T, svc llm.Service, tr *scriptedTransport, replayed func(body []byte) []string) [][]string {
	t.Helper()
	history := []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "go"}}}}
	var seen [][]string
	for step, call := range []string{"call1", "call2", ""} {
		resp, err := svc.Do(context.Background(), &llm.Request{Messages: history, ThinkingLevel: llm.ThinkingLevelHigh})
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		if step > 0 {
			seen = append(seen, replayed(tr.bodies[step]))
		}
		if call == "" {
			break
		}
		history = append(history, llm.Message{Role: resp.Role, Origin: resp.Origin, Content: resp.Content},
			llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{
				Type: llm.ContentTypeToolResult, ToolUseID: call,
				ToolResult: []llm.Content{{Type: llm.ContentTypeText, Text: "ok"}},
			}}})
	}
	return seen
}

func chatReplay(body []byte) []string {
	var req struct {
		Messages []struct {
			Role             string  `json:"role"`
			ReasoningContent *string `json:"reasoning_content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		panic(err)
	}
	var out []string
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			if m.ReasoningContent == nil {
				out = append(out, "<none>")
			} else {
				out = append(out, *m.ReasoningContent)
			}
		}
	}
	return out
}

func responsesReplay(body []byte) []string {
	var req struct {
		Input []struct {
			Type             string `json:"type"`
			EncryptedContent string `json:"encrypted_content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		panic(err)
	}
	var out []string
	for _, item := range req.Input {
		if item.Type == "reasoning" {
			out = append(out, item.EncryptedContent)
		}
	}
	return out
}

// TestSavedProviderToolLoopReplaysLikeOrdinaryAdapters compares captured wire
// requests of saved routes with the ordinary adapters for identical
// endpoint/model/history: the saved path must not lose reasoning state.
func TestSavedProviderToolLoopReplaysLikeOrdinaryAdapters(t *testing.T) {
	chatTurns := []string{chatToolTurn("think-1", "call1"), chatToolTurn("think-2", "call2"), `{"id":"c","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`}
	responsesTurns := []string{responsesToolTurn("rs_1", "enc-1", "call1"), responsesToolTurn("rs_2", "enc-2", "call2"), `{"id":"r","status":"completed","output":[]}`}
	for _, tc := range []struct {
		name, kind, endpoint, wire string
		turns                      []string
		replayed                   func([]byte) []string
		want                       [][]string
	}{
		{"deepseek chat", "openai", "https://api.deepseek.com/v1", "deepseek-v4-pro", chatTurns, chatReplay,
			[][]string{{"think-1"}, {"think-1", "think-2"}}},
		{"z.ai chat", "openai", "https://api.z.ai/api/paas/v4", "glm-5.2", chatTurns, chatReplay,
			[][]string{{"think-1"}, {"think-1", "think-2"}}},
		{"openai responses", "openai-responses", "https://api.openai.com/v1", "gpt-6-luna", responsesTurns, responsesReplay,
			[][]string{{"enc-1"}, {"enc-1", "enc-2"}}},
		{"unrecognised gateway chat", "openai", "https://gateway.example/v1", "mystery-model", chatTurns, chatReplay,
			[][]string{{"<none>"}, {"<none>", "<none>"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var metadata *modelsdev.Metadata
			if md, found := modelsdev.LookupMetadata(tc.endpoint, tc.wire); found {
				metadata = &md
			}
			run := func(build func(*http.Client) llm.Service) [][]string {
				tr := &scriptedTransport{responses: tc.turns}
				return toolLoop(t, build(&http.Client{Transport: tr}), tr, tc.replayed)
			}
			model := oai.Model{ModelName: tc.wire, UserName: tc.wire, URL: tc.endpoint}
			ordinary := run(func(hc *http.Client) llm.Service {
				if tc.kind == "openai-responses" {
					return &oai.ResponsesService{Model: model, ModelURL: tc.endpoint, APIKey: "k", HTTPC: hc, ProviderName: "openai"}
				}
				return &oai.Service{Model: model, ModelURL: tc.endpoint, APIKey: "k", HTTPC: hc, ProviderName: "openai"}
			})
			saved := run(func(hc *http.Client) llm.Service {
				built, err := BuildProviderModel("acct", tc.wire, "route", tc.wire, tc.kind, tc.endpoint, "k", hc, metadata)
				if err != nil {
					t.Fatal(err)
				}
				return built.Service
			})
			if !reflect.DeepEqual(ordinary, tc.want) {
				t.Fatalf("ordinary replay = %v, want %v", ordinary, tc.want)
			}
			if !reflect.DeepEqual(saved, ordinary) {
				t.Fatalf("saved replay = %v, ordinary = %v", saved, ordinary)
			}
		})
	}
}

func TestSavedResponsesRouteKeepsCatalogServiceFeatures(t *testing.T) {
	const endpoint = "https://api.openai.com/v1"
	build := func(endpoint, wire string) (Built, *scriptedTransport) {
		t.Helper()
		var metadata *modelsdev.Metadata
		if md, found := modelsdev.LookupMetadata(endpoint, wire); found {
			metadata = &md
		}
		tr := &scriptedTransport{responses: []string{`{"id":"r","status":"completed","output":[]}`}}
		built, err := BuildProviderModel("acct", wire, "route", wire, "openai-responses", endpoint, "k", &http.Client{Transport: tr}, metadata)
		if err != nil {
			t.Fatal(err)
		}
		return built, tr
	}
	verbosity := func(tr *scriptedTransport) string {
		var req struct {
			Text struct {
				Verbosity string `json:"verbosity"`
			} `json:"text"`
		}
		if err := json.Unmarshal(tr.bodies[0], &req); err != nil {
			t.Fatal(err)
		}
		return req.Text.Verbosity
	}
	send := func(b Built) {
		t.Helper()
		if _, err := b.Service.Do(context.Background(), simpleRequest()); err != nil {
			t.Fatal(err)
		}
	}

	official, tr := build(endpoint, "gpt-6-luna")
	send(official)
	if got := llm.PatchProfile(official.Service); got != "codex_apply_patch" {
		t.Fatalf("patch profile = %q", got)
	}
	if got := verbosity(tr); got != oai.GPT6Luna.TextVerbosity {
		t.Fatalf("verbosity = %q, want %q", got, oai.GPT6Luna.TextVerbosity)
	}

	// The same wire name behind an unrelated gateway has only advisory
	// evidence: no catalog features.
	proxy, tr := build("https://gateway.example/v1", "gpt-6-luna")
	send(proxy)
	if got := llm.PatchProfile(proxy.Service); got != "nested" {
		t.Fatalf("gateway patch profile = %q", got)
	}
	if got := verbosity(tr); got != "" {
		t.Fatalf("gateway verbosity = %q", got)
	}
}

func TestSavedProviderServiceForwardsCacheWarming(t *testing.T) {
	metadata, found := modelsdev.LookupMetadata("https://api.openai.com/v1", "gpt-6-luna")
	if !found || !providerReasoningEnabled(&metadata) {
		t.Fatal("fixture must have exact reasoning controls")
	}
	for _, tc := range []struct {
		name     string
		metadata *modelsdev.Metadata
		level    llm.ThinkingLevel
		effort   string
	}{
		{"exact", &metadata, llm.ThinkingLevelHigh, "high"},
		{"unknown", nil, llm.ThinkingLevelOff, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := predictable.NewService()
			svc := providerCapabilityService{Service: inner, metadata: tc.metadata}
			if got := llm.CacheTTL(svc); got != inner.CacheTTL() {
				t.Fatalf("CacheTTL = %v, want %v", got, inner.CacheTTL())
			}
			request := &llm.Request{ThinkingLevel: llm.ThinkingLevelHigh, ReasoningEffort: "high"}
			usage, err := llm.WarmCache(t.Context(), svc, request)
			if err != nil || usage.CacheReadInputTokens != 1024 {
				t.Fatalf("WarmCache = (%+v, %v)", usage, err)
			}
			warms := inner.WarmRequests()
			if len(warms) != 1 || warms[0].ThinkingLevel != tc.level || warms[0].ReasoningEffort != tc.effort {
				t.Fatalf("warm request = %+v, want %v/%q", warms, tc.level, tc.effort)
			}
			if request.ThinkingLevel != llm.ThinkingLevelHigh || request.ReasoningEffort != "high" {
				t.Fatal("warm changed caller's request")
			}
		})
	}
}
