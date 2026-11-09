package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestCompactionShards(t *testing.T) {
	tests := []struct {
		name    string
		weights []int
		want    [][2]int
	}{
		{"empty", nil, nil},
		{"one", []int{8}, [][2]int{{0, 1}}},
		{"fewer than workers", []int{1, 2, 3}, [][2]int{{0, 1}, {1, 2}, {2, 3}}},
		{"at most four", []int{1, 1, 1, 1, 1, 1}, [][2]int{{0, 1}, {1, 2}, {2, 4}, {4, 6}}},
		{"minimum maximum", []int{6, 5, 4, 3}, [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 4}}},
		{"known weighted balance", []int{8, 7, 6, 5, 4}, [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 5}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compactionShards(tt.weights)
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("shards = %v, want %v", got, tt.want)
			}
			next, count := 0, 0
			for _, shard := range got {
				if shard[0] != next || shard[1] <= shard[0] {
					t.Fatalf("not contiguous: %v", got)
				}
				for i := shard[0]; i < shard[1]; i++ {
					count++
				}
				next = shard[1]
			}
			if count != len(tt.weights) || next != len(tt.weights) {
				t.Fatalf("assigned %d of %d weights: %v", count, len(tt.weights), got)
			}
		})
	}
}

func TestCompactionWorkerNotes(t *testing.T) {
	valid := func(s string) *llm.Response {
		return &llm.Response{Content: []llm.Content{{Type: llm.ContentTypeText, Text: s}}}
	}
	tests := []struct {
		name string
		resp *llm.Response
		ok   bool
	}{
		{"valid", valid(`{"summaries":[{"id":2,"note":" two "},{"id":3,"note":"three"}]}`), true},
		{"invalid JSON", valid(`{`), false},
		{"unknown field", valid(`{"summaries":[],"surprise":1}`), false},
		{"missing ID", valid(`{"summaries":[{"id":2,"note":"two"}]}`), false},
		{"duplicate ID", valid(`{"summaries":[{"id":2,"note":"two"},{"id":2,"note":"again"},{"id":3,"note":"three"}]}`), false},
		{"extra ID", valid(`{"summaries":[{"id":2,"note":"two"},{"id":4,"note":"four"}]}`), false},
		{"empty note", valid(`{"summaries":[{"id":2,"note":"  "},{"id":3,"note":"three"}]}`), false},
		{"over 180 words", valid(`{"summaries":[{"id":2,"note":"` + strings.Repeat("w ", 181) + `"},{"id":3,"note":"three"}]}`), false},
		{"trailing JSON", valid(`{"summaries":[{"id":2,"note":"two"},{"id":3,"note":"three"}]} {}`), false},
		{"tool call", &llm.Response{Content: []llm.Content{{Type: llm.ContentTypeToolUse}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := compactionWorkerNotes(tt.resp, 2, 3)
			if (err == nil) != tt.ok {
				t.Fatalf("notes=%v err=%v", got, err)
			}
			if tt.ok && (got[2] != "two" || got[3] != "three") {
				t.Fatalf("notes = %v", got)
			}
		})
	}
}

type compactionTestService struct {
	do func(context.Context, *llm.Request) (*llm.Response, error)
}

func (s *compactionTestService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return s.do(ctx, req)
}
func (*compactionTestService) Provider() string       { return "test" }
func (*compactionTestService) MaxImageDimension() int { return 0 }
func (*compactionTestService) MaxImageBytes() int     { return 0 }
func (*compactionTestService) SupportsImages() bool   { return true }

type compactionWorkerPayload struct {
	Brief  string `json:"brief"`
	Chunks []struct {
		ID       int `json:"id"`
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	} `json:"chunks"`
}

func compactionTestItems(n int) []contextItem {
	items := make([]contextItem, n)
	for i := range items {
		seq := int64(i + 1)
		items[i] = contextItem{
			from: seq, to: seq,
			source:  &generated.Message{SequenceID: seq},
			message: llm.Message{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: fmt.Sprintf("source-%d", seq)}}},
		}
	}
	return items
}

func TestSummarizeCompactionUsesActiveServiceAndAssignedSources(t *testing.T) {
	items := compactionTestItems(8)
	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	var mu sync.Mutex
	var requests []*llm.Request
	svc := &compactionTestService{do: func(ctx context.Context, req *llm.Request) (*llm.Response, error) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		arrived <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		var payload compactionWorkerPayload
		if err := json.Unmarshal([]byte(req.Messages[0].Content[0].Text), &payload); err != nil {
			return nil, err
		}
		summaries := make([]map[string]any, 0, len(payload.Chunks))
		for _, chunk := range payload.Chunks {
			summaries = append(summaries, map[string]any{"id": chunk.ID, "note": "n"})
		}
		data, _ := json.Marshal(map[string]any{"summaries": summaries})
		return &llm.Response{Content: []llm.Content{{Type: llm.ContentTypeText, Text: string(data)}}}, nil
	}}
	collapse := make([]claudetool.CompactCollapse, len(items))
	for i := range collapse {
		id := fmt.Sprint(i + 1)
		collapse[i] = claudetool.CompactCollapse{From: claudetool.IndexID(id), To: claudetool.IndexID(id)}
	}
	in := claudetool.CompactInPlaceInput{Brief: "keep this brief", Collapse: collapse}
	ctx := llm.WithThinkingLevel(llm.WithLLMService(t.Context(), svc), llm.ThinkingLevelHigh)
	done := make(chan error, 1)
	go func() {
		_, err := summarizeCompaction(ctx, items, in)
		done <- err
	}()
	for range 4 {
		<-arrived
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("worker calls = %d, want 4", len(requests))
	}
	seen := map[string]int{}
	for _, req := range requests {
		if req.ThinkingLevel != llm.ThinkingLevelHigh || len(req.Tools) != 0 {
			t.Fatalf("worker request thinking/tools = %v/%v", req.ThinkingLevel, req.Tools)
		}
		var payload compactionWorkerPayload
		if err := json.Unmarshal([]byte(req.Messages[0].Content[0].Text), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Brief != in.Brief {
			t.Fatalf("brief = %q", payload.Brief)
		}
		for _, chunk := range payload.Chunks {
			if len(chunk.Messages) != 1 {
				t.Fatalf("chunk %d includes %d messages", chunk.ID, len(chunk.Messages))
			}
			seen[chunk.Messages[0].ID]++
		}
	}
	for i := 1; i <= len(items); i++ {
		if seen[fmt.Sprint(i)] != 1 {
			t.Fatalf("source %d assigned %d times", i, seen[fmt.Sprint(i)])
		}
	}
}

func TestSummarizeCompactionCollectsUsageOnParseErrorAndWorkerFailure(t *testing.T) {
	for _, tt := range []struct {
		name string
		do   func(context.Context, *llm.Request) (*llm.Response, error)
	}{
		{"parse error", func(context.Context, *llm.Request) (*llm.Response, error) {
			return &llm.Response{Usage: llm.Usage{InputTokens: 7}, Content: []llm.Content{{Type: llm.ContentTypeText, Text: `{`}}}, nil
		}},
		{"worker failure", func(context.Context, *llm.Request) (*llm.Response, error) {
			return &llm.Response{Usage: llm.Usage{InputTokens: 9}}, errors.New("failed")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var collected []llm.Usage
			ctx := llm.WithUsageCollector(llm.WithLLMService(t.Context(), &compactionTestService{do: tt.do}), func(purpose string, usage llm.Usage) {
				if purpose != "compaction" {
					t.Errorf("purpose = %q", purpose)
				}
				mu.Lock()
				collected = append(collected, usage)
				mu.Unlock()
			})
			_, err := summarizeCompaction(ctx, compactionTestItems(1), claudetool.CompactInPlaceInput{Collapse: []claudetool.CompactCollapse{{From: "1", To: "1"}}})
			if err == nil {
				t.Fatal("expected worker error")
			}
			if len(collected) != 1 || collected[0].InputTokens == 0 {
				t.Fatalf("usage collected = %v", collected)
			}
		})
	}
}

func TestSummarizeCompactionSendsImagesAsBlocksAndOmitsOpaquePayloads(t *testing.T) {
	item := compactionTestItems(1)[0]
	const imageData = "actual-image-data"
	item.message.Content = append(item.message.Content,
		llm.Content{Type: llm.ContentTypeText, Text: "image context", MediaType: "image/png", Data: imageData},
		llm.Content{Type: llm.ContentTypeText, Text: "opaque", GeminiServerTool: json.RawMessage(`{"private":"opaque-sentinel"}`)},
	)
	svc := &compactionTestService{do: func(_ context.Context, req *llm.Request) (*llm.Response, error) {
		if len(req.Messages[0].Content) != 3 {
			return nil, fmt.Errorf("content blocks = %d, want JSON, label, image", len(req.Messages[0].Content))
		}
		image := req.Messages[0].Content[2]
		if image.MediaType != "image/png" || image.Data != imageData {
			return nil, fmt.Errorf("image block = %+v", image)
		}
		payload := req.Messages[0].Content[0].Text
		if strings.Contains(payload, imageData) || strings.Contains(payload, "opaque-sentinel") {
			return nil, fmt.Errorf("JSON contains image or opaque payload: %s", payload)
		}
		return &llm.Response{Content: []llm.Content{{Type: llm.ContentTypeText, Text: `{"summaries":[{"id":1,"note":"n"}]}`}}}, nil
	}}
	ctx := llm.WithLLMService(t.Context(), svc)
	_, err := summarizeCompaction(ctx, []contextItem{item}, claudetool.CompactInPlaceInput{
		Collapse: []claudetool.CompactCollapse{{From: "1", To: "1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
