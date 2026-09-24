package db

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
)

func TestSearchConversationsPreferSlugMatches(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ctx := t.Context()

	fixtures := []struct {
		slug     *string
		archived bool
		message  bool
		updated  string
	}{
		{stringPtr("pelican-older"), false, false, "2024-01-01 00:00:00"},
		{stringPtr("project-pelican-notes"), false, true, "2024-01-02 00:00:00"},
		{stringPtr("archived-pelican"), true, false, "2024-01-03 00:00:00"},
		{stringPtr("active-notes"), false, true, "2024-01-04 00:00:00"},
		{nil, false, true, "2024-01-05 00:00:00"},
		{stringPtr("active-latest"), false, true, "2024-01-06 00:00:00"},
		{stringPtr("archived-notes"), true, true, "2024-01-07 00:00:00"},
		{stringPtr("unrelated"), false, false, "2024-01-08 00:00:00"},
	}
	ids := make([]string, len(fixtures))
	for i, fixture := range fixtures {
		conv, err := db.CreateConversation(ctx, fixture.slug, true, nil, nil, ConversationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = conv.ConversationID
		if fixture.message {
			for range 2 {
				if _, err := db.CreateMessage(ctx, CreateMessageParams{
					ConversationID: conv.ConversationID,
					Type:           MessageTypeAgent,
					LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "A pelican by the bay"}}},
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if fixture.archived {
			if _, err := db.ArchiveConversation(ctx, conv.ConversationID); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Pool().Exec(ctx, "UPDATE conversations SET updated_at = ? WHERE conversation_id = ?", fixture.updated, conv.ConversationID); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name   string
		search func(context.Context, string, int64, int64) ([]ConversationListItem, error)
		want   []string
	}{
		{
			name: "FTS",
			search: func(ctx context.Context, query string, limit, offset int64) ([]ConversationListItem, error) {
				hits, err := db.SearchConversationsFTS(ctx, query, limit, offset)
				items := make([]ConversationListItem, len(hits))
				for i, hit := range hits {
					items[i] = hit.ConversationListItem
				}
				return items, err
			},
			want: []string{ids[1], ids[0], ids[2], ids[5], ids[4], ids[3], ids[6]},
		},
		{
			name:   "WithMessages",
			search: db.SearchConversationsWithMessages,
			want:   []string{ids[1], ids[0], ids[5], ids[4], ids[3]},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := func(limit, offset int64, want []string) {
				t.Helper()
				results, err := tc.search(ctx, "PELICAN", limit, offset)
				if err != nil {
					t.Fatal(err)
				}
				got := make([]string, len(results))
				for i, result := range results {
					got[i] = result.ConversationID
				}
				if !slices.Equal(got, want) {
					t.Fatalf("limit=%d offset=%d: got %v, want %v", limit, offset, got, want)
				}
			}
			check(50, 0, tc.want)
			for i, id := range tc.want {
				check(1, int64(i), []string{id})
			}
			check(1, int64(len(tc.want)), nil)
		})
	}
}

func TestSearchConversationsFTS(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Active conversation with a message mentioning "pelican"
	active, err := db.CreateConversation(ctx, stringPtr("active-bird"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: active.ConversationID,
		Type:           MessageTypeUser,
		UserData:       map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "I saw a pelican by the bay"}}},
	}); err != nil {
		t.Fatalf("create user msg: %v", err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: active.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "That pelican had a silver beak"}}},
	}); err != nil {
		t.Fatalf("create second matching msg: %v", err)
	}

	// Archived conversation with a message mentioning "pelican" too
	archived, err := db.CreateConversation(ctx, stringPtr("old-notes"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create archived: %v", err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: archived.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": "A pelican is a large waterbird"}}},
	}); err != nil {
		t.Fatalf("create agent msg: %v", err)
	}
	if _, err := db.ArchiveConversation(ctx, archived.ConversationID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Decoy conversation with no matching content.
	if _, err := db.CreateConversation(ctx, stringPtr("decoy"), true, nil, nil, ConversationOptions{}); err != nil {
		t.Fatalf("create decoy: %v", err)
	}

	results, err := db.SearchConversationsFTS(ctx, "pelican", 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %#v", len(results), results)
	}
	gotActive := false
	gotArchived := false
	for _, r := range results {
		if r.Conversation.ConversationID == active.ConversationID {
			gotActive = true
		}
		if r.Conversation.ConversationID == archived.ConversationID {
			gotArchived = true
		}
		// Every FTS hit should come with a snippet that contains both
		// the matched term and our mark sentinels.
		if r.Snippet == "" {
			t.Errorf("missing snippet for %s", r.Conversation.ConversationID)
		}
		if !strings.Contains(r.Snippet, SnippetMarkStart) || !strings.Contains(r.Snippet, SnippetMarkEnd) {
			t.Errorf("snippet missing mark sentinels: %q", r.Snippet)
		}
		if !strings.Contains(strings.ToLower(r.Snippet), "pelican") {
			t.Errorf("snippet does not contain match term: %q", r.Snippet)
		}
	}
	if !gotActive || !gotArchived {
		t.Errorf("missing expected conversations: active=%v archived=%v", gotActive, gotArchived)
	}
	var snippets []generated.SearchConversationsFTSSnippetsRow
	if err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		match := `"pelican"*`
		var err error
		snippets, err = generated.New(rx.Conn()).SearchConversationsFTSSnippets(ctx, generated.SearchConversationsFTSSnippetsParams{
			MarkStart: SnippetMarkStart,
			MarkEnd:   SnippetMarkEnd,
			FtsMatch:  &match,
			ConvIds:   []string{active.ConversationID, archived.ConversationID},
		})
		return err
	}); err != nil {
		t.Fatalf("SearchConversationsFTSSnippets: %v", err)
	}
	if len(snippets) != 2 {
		t.Fatalf("SearchConversationsFTSSnippets returned %d rows, want one per conversation", len(snippets))
	}

	// Slug match should still work even with no message hits.
	results, err = db.SearchConversationsFTS(ctx, "decoy", 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS slug: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 slug result, got %d", len(results))
	}
	// Slug-only hits have no snippet.
	if results[0].Snippet != "" {
		t.Errorf("expected empty snippet for slug-only match, got %q", results[0].Snippet)
	}

	// Prefix matching: typing "peli" should still find the pelican messages.
	results, err = db.SearchConversationsFTS(ctx, "peli", 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS prefix: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 prefix results, got %d", len(results))
	}

	// Empty query returns no results without erroring.
	emptyResults, err := db.SearchConversationsFTS(ctx, "   ", 50, 0)
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if len(emptyResults) != 0 {
		t.Errorf("expected 0 results for empty query, got %d", len(emptyResults))
	}

	// FTS5 syntax characters in user input must not break the query.
	if _, err := db.SearchConversationsFTS(ctx, `pel"ican AND foo*`, 50, 0); err != nil {
		t.Fatalf("escaped query: %v", err)
	}

	// A literal % in the search query must not match every slug via LIKE.
	noise, err := db.SearchConversationsFTS(ctx, "%", 50, 0)
	if err != nil {
		t.Fatalf("percent query: %v", err)
	}
	if len(noise) != 0 {
		t.Errorf("expected 0 results for bare %% query, got %d", len(noise))
	}
}

// TestSearchConversationsFTSStripsCitationMarkers checks the search snippet,
// which is built from raw message JSON and is client-facing.
func TestSearchConversationsFTSStripsCitationMarkers(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	conv, err := db.CreateConversation(ctx, stringPtr("cited"), true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	text := "A pelican is a large waterbird\ue200cite\ue202turn1search0\ue201 that fishes in flocks."
	if _, err := db.CreateMessage(ctx, CreateMessageParams{
		ConversationID: conv.ConversationID,
		Type:           MessageTypeAgent,
		LLMData:        map[string]any{"Content": []any{map[string]any{"Type": 2, "Text": text}}},
	}); err != nil {
		t.Fatalf("create agent msg: %v", err)
	}

	results, err := db.SearchConversationsFTS(ctx, "pelican", 50, 0)
	if err != nil {
		t.Fatalf("SearchConversationsFTS: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	snippet := results[0].Snippet
	if strings.ContainsAny(snippet, "\ue200\ue201\ue202\ue203") {
		t.Errorf("snippet contains citation markers: %q", snippet)
	}
	if strings.Contains(snippet, "citeturn1search0") {
		t.Errorf("snippet contains citation payload: %q", snippet)
	}
	if !strings.Contains(snippet, "that fishes in flocks.") {
		t.Errorf("snippet lost text following the citation: %q", snippet)
	}
}
