package gem

import (
	"encoding/json"
	"fmt"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/gem/gemini"
)

type groundingCitation struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	CitedText string `json:"cited_text"`
}

func convertSearchResponse(res *gemini.Response) ([]llm.Content, error) {
	if len(res.Candidates) == 0 {
		return convertGeminiResponseToContent(res), nil
	}
	candidate := res.Candidates[0]
	var content []llm.Content
	for _, part := range candidate.Content.Parts {
		if part.ToolCall == nil && part.ToolResponse == nil {
			one := &gemini.Response{Candidates: []gemini.Candidate{{Content: gemini.Content{Parts: []gemini.Part{part}}}}}
			content = append(content, convertGeminiResponseToContent(one)...)
			continue
		}
		opaque, err := json.Marshal(part)
		if err != nil {
			return nil, fmt.Errorf("marshal Gemini server tool: %w", err)
		}
		c := llm.Content{ToolName: "web_search", GeminiServerTool: opaque}
		if part.ToolCall != nil {
			if part.ToolCall.ToolType != "GOOGLE_SEARCH_WEB" {
				return nil, fmt.Errorf("unsupported Gemini server tool %q", part.ToolCall.ToolType)
			}
			c.Type = llm.ContentTypeServerToolUse
			c.ID = part.ToolCall.ID
			c.ToolInput = part.ToolCall.Args
		} else {
			if part.ToolResponse.ToolType != "GOOGLE_SEARCH_WEB" {
				return nil, fmt.Errorf("unsupported Gemini server tool result %q", part.ToolResponse.ToolType)
			}
			c.Type = llm.ContentTypeWebSearchToolResult
			c.ToolUseID = part.ToolResponse.ID
		}
		content = append(content, c)
	}
	metadata := candidate.GroundingMetadata
	if metadata == nil {
		return content, nil
	}
	var citations []groundingCitation
	for _, support := range metadata.GroundingSupports {
		if support.Segment == nil {
			return nil, fmt.Errorf("Gemini grounding support has no segment")
		}
		for _, index := range support.GroundingChunkIndices {
			if index < 0 || index >= len(metadata.GroundingChunks) {
				return nil, fmt.Errorf("Gemini grounding chunk index %d out of range", index)
			}
			web := metadata.GroundingChunks[index].Web
			if web == nil || web.URI == "" {
				return nil, fmt.Errorf("Gemini grounding chunk %d has no web source URI", index)
			}
			citations = append(citations, groundingCitation{
				Type: "google_grounding", URL: web.URI, Title: web.Title, CitedText: support.Segment.Text,
			})
		}
	}
	target := -1
	for i := range content {
		if content[i].Type == llm.ContentTypeText && content[i].Text != "" {
			target = i
		}
	}
	if target == -1 {
		if len(citations) > 0 {
			return nil, fmt.Errorf("Gemini grounding citations have no answer text")
		}
		return content, nil
	}
	if len(citations) > 0 {
		raw, err := json.Marshal(citations)
		if err != nil {
			return nil, fmt.Errorf("marshal Gemini grounding citations: %w", err)
		}
		content[target].Citations = raw
	}
	if metadata.SearchEntryPoint != nil {
		content[target].GoogleSearchSuggestions = metadata.SearchEntryPoint.RenderedContent
	}
	return content, nil
}
