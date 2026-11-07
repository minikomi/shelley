package gem

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/llm/gem/gemini"
)

func TestGroundedSearchToolTurn(t *testing.T) {
	var res gemini.Response
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[
		{"toolResponse":{"id":"search","toolType":"GOOGLE_SEARCH_WEB","response":{"search_suggestions":"<p>Paris</p>","opaque":"keep"}},"thoughtSignature":"signed"},
		{"functionCall":{"name":"weather","args":{"city":"Paris"}}}
	]}}]}`), &res)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := convertSearchResponse(&res)
	if err != nil {
		t.Fatal(err)
	}
	if got := blocks[0]; len(got.ToolResult) != 1 || got.ToolUseID != "search" || got.ToolResult[0].GoogleSearchSuggestions != "<p>Paris</p>" {
		t.Fatalf("search result lost suggestions: %+v", got)
	}
	var replay struct {
		ThoughtSignature string
		ToolResponse     struct{ Response map[string]string }
	}
	if err := json.Unmarshal(blocks[0].GeminiServerTool, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.ThoughtSignature != "signed" || replay.ToolResponse.Response["opaque"] != "keep" {
		t.Fatal("search replay lost provider fields")
	}

	candidate := &res.Candidates[0]
	candidate.Content.Parts = append(candidate.Content.Parts, gemini.Part{Text: "First."}, gemini.Part{Text: "🏟 Paris hosted. Next.", ThoughtSignature: "answer-signature"})
	candidate.GroundingMetadata = &gemini.GroundingMetadata{
		SearchEntryPoint:  &gemini.SearchEntryPoint{RenderedContent: "<p>Paris</p>"},
		GroundingChunks:   []gemini.GroundingChunk{{Web: &gemini.GroundingWeb{URI: "https://example.org/paris"}}},
		GroundingSupports: []gemini.GroundingSupport{{Segment: &gemini.GroundingSegment{PartIndex: 1, EndIndex: len("🏟 Paris hosted."), Text: "🏟 Paris hosted."}, GroundingChunkIndices: []int{0}}},
	}
	blocks, err = convertSearchResponse(&res)
	if err != nil {
		t.Fatal(err)
	}
	answer := blocks[3]
	var cites []groundingCitation
	if err := json.Unmarshal(answer.Citations, &cites); err != nil {
		t.Fatal(err)
	}
	if len(cites) != 1 || cites[0].EndUTF16 != 16 || answer.Signature != "answer-signature" || answer.Text != candidate.Content.Parts[3].Text || answer.GoogleSearchSuggestions != "" {
		t.Fatalf("incorrect grounded answer: %+v", answer)
	}
}
