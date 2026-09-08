package oai

import (
	"encoding/json"
	"fmt"
	"strings"

	"shelley.exe.dev/llm"
)

func adaptCitation(kind string, fields map[string]json.RawMessage) (reference string, retain bool, err error) {
	// Chat and Responses serialize both web citation shapes as text references.
	switch kind {
	case "url_citation", "web_search_result_location":
	case "char_location", "page_location", "content_block_location", "search_result_location":
		return "", false, fmt.Errorf("cannot convert %q to text URL references; document/search locators are unsupported", kind)
	default:
		return "", false, fmt.Errorf("unsupported citation type %q", kind)
	}
	url, err := llm.CitationString(fields, "url", true, false)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(url) == "" {
		return "", false, fmt.Errorf("url must be a nonempty string")
	}
	web := kind == "web_search_result_location"
	title, err := llm.CitationString(fields, "title", web, web)
	if err != nil {
		return "", false, err
	}
	quote, err := llm.CitationString(fields, "cited_text", web, false)
	if err != nil {
		return "", false, err
	}
	if _, err := llm.CitationString(fields, "encrypted_index", false, false); err != nil {
		return "", false, err
	}
	for _, name := range []string{"start_index", "end_index"} {
		if err := llm.CitationNonnegativeInteger(fields, name); err != nil {
			return "", false, err
		}
	}
	return llm.FormatCitationURLReference(url, title, quote), false, nil
}
