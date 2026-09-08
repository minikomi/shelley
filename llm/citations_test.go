package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

const openAICitation = `{"type":"url_citation","start_index":0,"end_index":6,"url":"https://example.com/source","title":"Source title"}`
const anthropicWebCitation = `{"type":"web_search_result_location","url":"https://example.com/web","title":"Web title","cited_text":"Original cited passage","encrypted_index":"opaque-signed-index","extra":{"keep":true}}`

func citationRequest(raw string) *Request {
	return &Request{Messages: []Message{{Role: MessageRoleAssistant, Content: []Content{{Type: ContentTypeText, Text: "Answer", Citations: json.RawMessage(raw)}}}}}
}

func TestPrepareRequestCitations(t *testing.T) {
	native := " [ \n" + anthropicWebCitation + `, {"type":"char_location","opaque":true}, {"type":"page_location"}, {"type":"content_block_location"}, {"type":"search_result_location"} ] `
	for _, tt := range []struct {
		name     string
		target   CitationTarget
		raw      string
		wantRaw  string
		wantText string
	}{
		{"original OpenAI bug", CitationsAnthropic, "[" + openAICitation + "]", "", "Answer\n\nSource: Source title — https://example.com/source"},
		{"native byte preservation and provider validation boundary", CitationsAnthropic, native, native, "Answer"},
		{"mixed retains native", CitationsAnthropic, "[" + anthropicWebCitation + "," + openAICitation + "]", "[" + anthropicWebCitation + "]", "Answer\n\nSource: Source title — https://example.com/source"},
		{"reverse chat", CitationsOpenAIChat, "[" + anthropicWebCitation + "]", "", "Answer\n\nSource: Web title — https://example.com/web\nCited text: Original cited passage"},
		{"reverse responses", CitationsOpenAIResponses, "[" + anthropicWebCitation + "]", "", "Answer\n\nSource: Web title — https://example.com/web\nCited text: Original cited passage"},
		{"same provider responses", CitationsOpenAIResponses, "[" + openAICitation + "]", "", "Answer\n\nSource: Source title — https://example.com/source"},
		{"same provider chat", CitationsOpenAIChat, "[" + openAICitation + "]", "", "Answer\n\nSource: Source title — https://example.com/source"},
		{"omitted local zero ranges and title", CitationsAnthropic, `[{"type":"url_citation","url":"https://example.com"}]`, "", "Answer\n\nSource: https://example.com"},
		{"nullable web title", CitationsOpenAIChat, `[{"type":"web_search_result_location","url":"https://example.com","title":null,"cited_text":"quote","encrypted_index":"sig"}]`, "", "Answer\n\nSource: https://example.com\nCited text: quote"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := citationRequest(tt.raw)
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := PrepareRequestCitations(context.Background(), req, tt.target)
			if err != nil {
				t.Fatal(err)
			}
			c := got.Messages[0].Content[0]
			if c.Text != tt.wantText || string(c.Citations) != tt.wantRaw {
				t.Fatalf("got text %q citations %s; want text %q citations %s", c.Text, c.Citations, tt.wantText, tt.wantRaw)
			}
			after, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || string(req.Messages[0].Content[0].Citations) != tt.raw {
				t.Fatal("preparation mutated original history")
			}
			for _, input := range []*Request{req, got} {
				again, err := PrepareRequestCitations(context.Background(), input, tt.target)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, again) {
					t.Fatal("preparation is not repeatable/idempotent")
				}
			}
			var reloaded Request
			if err := json.Unmarshal(before, &reloaded); err != nil {
				t.Fatal(err)
			}
			roundTrip, err := PrepareRequestCitations(context.Background(), &reloaded, tt.target)
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := json.Marshal(got)
			roundTripJSON, _ := json.Marshal(roundTrip)
			if !bytes.Equal(gotJSON, roundTripJSON) {
				t.Fatal("JSON persistence changed preparation")
			}
			// Even the retained raw bytes must not alias the caller's history.
			got.Messages[0].Role = MessageRoleUser
			got.Messages[0].Content[0].Text = "changed"
			if len(c.Citations) > 0 {
				c.Citations[0] = '!'
			}
			if req.Messages[0].Role != MessageRoleAssistant || req.Messages[0].Content[0].Text != "Answer" || string(req.Messages[0].Content[0].Citations) != tt.raw {
				t.Fatal("prepared history aliases original")
			}
		})
	}
}

func TestPrepareRequestCitationsAbsent(t *testing.T) {
	for _, target := range []CitationTarget{CitationsAnthropic, CitationsOpenAIChat, CitationsOpenAIResponses} {
		for _, raw := range []string{"", "null", " \n null ", "[]", "[ ]"} {
			for _, mediaType := range []string{"", "image/png"} {
				req := citationRequest(raw)
				req.Messages[0].Content[0].MediaType = mediaType
				got, err := PrepareRequestCitations(context.Background(), req, target)
				if err != nil {
					t.Fatal(err)
				}
				if got.Messages[0].Content[0].Citations != nil {
					t.Fatalf("%q not normalized to nil", raw)
				}
				if string(req.Messages[0].Content[0].Citations) != raw {
					t.Fatal("mutated absent metadata")
				}
			}
		}
	}
	// Explicitly exercise the old persistence representation.
	var req Request
	if err := json.Unmarshal([]byte(`{"Messages":[{"Role":1,"Content":[{"Type":2,"Text":"old history","Citations":null}]}]}`), &req); err != nil {
		t.Fatal(err)
	}
	got, err := PrepareRequestCitations(context.Background(), &req, CitationsAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	if got.Messages[0].Content[0].Citations != nil || string(req.Messages[0].Content[0].Citations) != "null" {
		t.Fatal("old null history mishandled")
	}
}

func TestPrepareRequestCitationsErrors(t *testing.T) {
	for _, target := range []CitationTarget{CitationsAnthropic, CitationsOpenAIChat, CitationsOpenAIResponses} {
		for _, tt := range []struct{ name, raw, path, reason string }{
			{"unknown", `[{"type":"future_citation"}]`, "[0]", "unsupported citation type"},
			{"malformed", `[{`, "", "expected citation array"},
			{"object", `{}`, "", "expected citation array"},
			{"whitespace", ` `, "", "expected citation array"},
			{"entry null", `[null]`, "[0]", "expected citation object"},
			{"entry array", `[[]]`, "[0]", "expected citation object"},
			{"entry number", `[1]`, "[0]", "expected citation object"},
			{"missing tag", `[{}]`, "[0]", "type must"},
			{"null tag", `[{"type":null}]`, "[0]", "type must"},
			{"wrong tag shape", `[{"type":{}}]`, "[0]", "type must"},
			{"missing URL", `[{"type":"url_citation"}]`, "[0]", "url must"},
			{"empty URL", `[{"type":"url_citation","url":" "}]`, "[0]", "url must"},
			{"URL shape", `[{"type":"url_citation","url":5}]`, "[0]", "url must"},
			{"title shape", `[{"type":"url_citation","url":"x","title":[]}]`, "[0]", "title must"},
			{"quote shape", `[{"type":"url_citation","url":"x","cited_text":false}]`, "[0]", "cited_text must"},
			{"range shape", `[{"type":"url_citation","url":"x","start_index":"0"}]`, "[0]", "start_index must"},
			{"null range", `[{"type":"url_citation","url":"x","end_index":null}]`, "[0]", "end_index must"},
			{"mixed invalid", "[" + openAICitation + `,{"type":"future"}]`, "[1]", "unsupported citation type"},
		} {
			t.Run(string(target)+"/"+tt.name, func(t *testing.T) {
				req := citationRequest(tt.raw)
				got, err := PrepareRequestCitations(context.Background(), req, target)
				if got != nil || err == nil || !strings.Contains(err.Error(), string(target)) || !strings.Contains(err.Error(), "messages[0].content[0].citations"+tt.path) || !strings.Contains(err.Error(), tt.reason) {
					t.Fatalf("got %v, error %v", got, err)
				}
				if req.Messages[0].Content[0].Text != "Answer" || string(req.Messages[0].Content[0].Citations) != tt.raw {
					t.Fatal("error mutated history")
				}
			})
		}
		for _, content := range []Content{{Type: ContentTypeText, MediaType: "image/png"}, {Type: ContentTypeThinking}, {Type: ContentTypeToolResult}} {
			content.Citations = json.RawMessage("[" + openAICitation + "]")
			req := &Request{Messages: []Message{{Content: []Content{content}}}}
			if _, err := PrepareRequestCitations(context.Background(), req, target); err == nil || !strings.Contains(err.Error(), "messages[0].content[0].citations[0]: citations require text") {
				t.Fatalf("non-text error = %v", err)
			}
		}
		if target == CitationsAnthropic {
			continue
		}
		for _, kind := range []string{"char_location", "page_location", "content_block_location", "search_result_location"} {
			if _, err := PrepareRequestCitations(context.Background(), citationRequest(`[{"type":"`+kind+`"}]`), target); err == nil || !strings.Contains(err.Error(), "locators are unsupported") {
				t.Fatalf("locator error = %v", err)
			}
		}
		for _, field := range []string{"url", "title", "cited_text", "encrypted_index"} {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(anthropicWebCitation), &fields); err != nil {
				t.Fatal(err)
			}
			fields[field] = json.RawMessage(`42`)
			raw, err := json.Marshal([]map[string]json.RawMessage{fields})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareRequestCitations(context.Background(), citationRequest(string(raw)), target); err == nil || !strings.Contains(err.Error(), field+" must") {
				t.Fatalf("web field error = %v", err)
			}
		}
	}
}

func TestPrepareRequestCitationsNestedAndLogs(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	req := citationRequest("[" + openAICitation + "]")
	req.Messages = append(req.Messages, Message{Role: MessageRoleUser, Content: []Content{{Type: ContentTypeToolResult, ToolUseID: "call", ToolResult: []Content{{Type: ContentTypeText, Text: "Nested", Citations: json.RawMessage(`[{"type":"future"}]`)}}}}})
	if _, err := PrepareRequestCitations(context.Background(), req, CitationsAnthropic); err == nil || !strings.Contains(err.Error(), "messages[1].content[0].tool_result[0].citations[0]") {
		t.Fatalf("nested error = %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("logged success before complete validation: %s", logs.String())
	}
	req.Messages[1].Content[0].ToolResult[0].Citations = json.RawMessage("[" + openAICitation + "]")
	got, err := PrepareRequestCitations(context.Background(), req, CitationsAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Messages[1].Content[0].ToolResult[0].Text, "Source title") {
		t.Fatal("nested citation not converted")
	}
	got.Messages[1].Content[0].ToolResult[0].Text = "changed"
	if req.Messages[1].Content[0].ToolResult[0].Text != "Nested" {
		t.Fatal("nested history aliases input")
	}
	for _, want := range []string{`"destination":"anthropic"`, `"count":1`, `"types":["url_citation"]`, `messages[1].content[0].tool_result[0].citations`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log missing %s: %s", want, logs.String())
		}
	}
	for _, secret := range []string{"example.com", "Source title", "Nested"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log leaked %s", secret)
		}
	}
}
