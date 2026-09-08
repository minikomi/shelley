package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// CitationTarget identifies the request serializer, not the originating provider.
type CitationTarget string

const (
	CitationsAnthropic       CitationTarget = "anthropic"
	CitationsOpenAIChat      CitationTarget = "openai-chat"
	CitationsOpenAIResponses CitationTarget = "openai-responses"
)

type citationConversion struct {
	path  string
	types []string
}

// PrepareRequestCitations copies request history and adapts citations before any
// provider filtering or retries. Native Anthropic objects are opaque: only their
// object shape and recognized type tag are checked; provider schema validation
// (including signed/encrypted fields) remains Anthropic's responsibility.
// URL conversions validate the fields they interpret and render source references
// as text, never fabricating Anthropic encrypted_index values. Document/search
// locators cannot be represented faithfully by the OpenAI serializers and error.
func PrepareRequestCitations(ctx context.Context, req *Request, target CitationTarget) (*Request, error) {
	switch target {
	case CitationsAnthropic, CitationsOpenAIChat, CitationsOpenAIResponses:
	default:
		return nil, fmt.Errorf("prepare citations: unsupported destination %q", target)
	}
	if req == nil {
		return nil, fmt.Errorf("prepare citations for %s: nil request", target)
	}
	out := *req
	out.Messages = slices.Clone(req.Messages)
	var conversions []citationConversion
	for i := range out.Messages {
		content, err := prepareContentCitations(req.Messages[i].Content, target, fmt.Sprintf("messages[%d].content", i), &conversions)
		if err != nil {
			return nil, fmt.Errorf("prepare citations for %s: %w", target, err)
		}
		out.Messages[i].Content = content
	}
	// Report only after the entire request validates; never log source payloads.
	for _, conversion := range conversions {
		slog.InfoContext(ctx, "converted request citations to text references", "destination", target,
			"path", conversion.path, "count", len(conversion.types), "types", conversion.types)
	}
	return &out, nil
}

func prepareContentCitations(content []Content, target CitationTarget, path string, conversions *[]citationConversion) ([]Content, error) {
	out := slices.Clone(content)
	for i := range out {
		c := &out[i]
		citationPath := fmt.Sprintf("%s[%d].citations", path, i)
		raw := bytes.TrimSpace(c.Citations)
		c.Citations = slices.Clone(c.Citations)
		if len(c.Citations) == 0 || bytes.Equal(raw, []byte("null")) {
			// Older persisted history contains literal null rather than nil.
			c.Citations = nil
		} else {
			var entries []json.RawMessage
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, fmt.Errorf("%s: expected citation array: %w", citationPath, err)
			}
			if len(entries) > 0 && (c.Type != ContentTypeText || c.MediaType != "") {
				return nil, fmt.Errorf("%s[0]: citations require text content", citationPath)
			}
			var retained [][]byte
			var converted []string
			for k, entry := range entries {
				entryPath := fmt.Sprintf("%s[%d]", citationPath, k)
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(entry, &fields); err != nil || fields == nil {
					return nil, fmt.Errorf("%s: expected citation object", entryPath)
				}
				kind, err := citationString(fields, "type", true, false)
				if err != nil || kind == "" {
					return nil, fmt.Errorf("%s: type must be a nonempty string", entryPath)
				}
				switch kind {
				case "char_location", "page_location", "content_block_location", "search_result_location", "web_search_result_location":
					if target == CitationsAnthropic {
						retained = append(retained, entry)
						continue
					}
					if kind != "web_search_result_location" {
						return nil, fmt.Errorf("%s: cannot convert %q to text URL references; document/search locators are unsupported", entryPath, kind)
					}
				case "url_citation":
				default:
					return nil, fmt.Errorf("%s: unsupported citation type %q", entryPath, kind)
				}
				reference, err := citationURLReference(fields, kind)
				if err != nil {
					return nil, fmt.Errorf("%s (%s): %w", entryPath, kind, err)
				}
				c.Text += reference
				converted = append(converted, kind)
			}
			if len(entries) == 0 || (len(converted) > 0 && len(retained) == 0) {
				c.Citations = nil
			} else if len(converted) > 0 {
				// Keep retained objects byte-for-byte, including opaque fields.
				c.Citations = append([]byte("["), bytes.Join(retained, []byte(","))...)
				c.Citations = append(c.Citations, ']')
			}
			if len(converted) > 0 {
				*conversions = append(*conversions, citationConversion{path: citationPath, types: converted})
			}
		}
		var err error
		c.ToolResult, err = prepareContentCitations(c.ToolResult, target, fmt.Sprintf("%s[%d].tool_result", path, i), conversions)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func citationString(fields map[string]json.RawMessage, name string, required, nullable bool) (string, error) {
	raw, ok := fields[name]
	if !ok && !required {
		return "", nil
	}
	if nullable && bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var value string
	if !ok || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return value, nil
}

func citationURLReference(fields map[string]json.RawMessage, kind string) (string, error) {
	url, err := citationString(fields, "url", true, false)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(url) == "" {
		return "", fmt.Errorf("url must be a nonempty string")
	}
	web := kind == "web_search_result_location"
	title, err := citationString(fields, "title", web, web)
	if err != nil {
		return "", err
	}
	quote, err := citationString(fields, "cited_text", web, false)
	if err != nil {
		return "", err
	}
	if _, err := citationString(fields, "encrypted_index", false, false); err != nil {
		return "", err
	}
	for _, name := range []string{"start_index", "end_index"} {
		if raw, ok := fields[name]; ok {
			var index int
			if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &index) != nil || index < 0 {
				return "", fmt.Errorf("%s must be a nonnegative integer", name)
			}
		}
	}
	reference := "\n\nSource: "
	if title != "" {
		reference += title + " — "
	}
	reference += url
	if quote != "" {
		reference += "\nCited text: " + quote
	}
	return reference, nil
}
