package loop

import (
	"strings"
	"testing"

	"shelley.exe.dev/llm"
)

func TestShapeLiveContextElidesStableOldToolResult(t *testing.T) {
	t.Parallel()
	req := elisionRequest("cat src/main.go", strings.Repeat("source\n", 600), 184, false)
	before := estimateRequestTokens(req)
	outbound, stats := ShapeLiveContext(req, before*2, testElisionConfig())

	if stats.Decision != "large_results" {
		t.Fatalf("decision = %q, want large_results", stats.Decision)
	}
	if stats.ElidedResults != 1 {
		t.Fatalf("elided results = %d, want 1", stats.ElidedResults)
	}
	if got := outbound.Messages[1].Content[0].ToolResult[0].Text; !strings.Contains(got, "seq: 184") {
		t.Fatalf("marker does not recover canonical sequence:\n%s", got)
	}
	if got := outbound.Messages[1].Content[0].ToolResult[0].Text; !strings.Contains(got, "command: cat src/main.go") {
		t.Fatalf("marker lost command:\n%s", got)
	}
	if got := req.Messages[1].Content[0].ToolResult[0].Text; !strings.HasPrefix(got, "source\n") {
		t.Fatalf("input request was mutated:\n%s", got)
	}
	if stats.AfterTokens >= stats.BeforeTokens {
		t.Fatalf("after = %d, before = %d", stats.AfterTokens, stats.BeforeTokens)
	}
}

func TestShapeLiveContextLeavesLiveAndRecentResultsVerbatim(t *testing.T) {
	t.Parallel()
	req := elisionRequest("cat src/main.go", strings.Repeat("source\n", 600), 0, false)
	before := estimateRequestTokens(req)
	outbound, stats := ShapeLiveContext(req, before*2, testElisionConfig())
	if stats.ElidedResults != 0 || stats.Decision != "no_eligible_results" {
		t.Fatalf("stats = %+v, want no eligible results", stats)
	}
	if got, want := outbound.Messages[1].Content[0].ToolResult[0].Text, req.Messages[1].Content[0].ToolResult[0].Text; got != want {
		t.Fatal("live result changed without a stable sequence")
	}

	recent := elisionRequest("cat src/main.go", strings.Repeat("source\n", 600), 184, false)
	recent.Messages = recent.Messages[:2]
	recent.Messages[1].SequenceID = 184
	before = estimateRequestTokens(recent)
	outbound, stats = ShapeLiveContext(recent, before*2, LiveContextElisionConfig{
		StartPressure:       0.4,
		ProgressivePressure: 0.55,
		CompactionPressure:  0.7,
		ProtectedTailTokens: before,
		LargeResultTokens:   20,
		MinimumResultTokens: 10,
	})
	if stats.ElidedResults != 0 {
		t.Fatalf("protected tail was elided: %+v", stats)
	}
	if got, want := outbound.Messages[1].Content[0].ToolResult[0].Text, recent.Messages[1].Content[0].ToolResult[0].Text; got != want {
		t.Fatal("protected tool result changed")
	}
}

func TestShapeLiveContextPrioritizesHistoryAndPreservesFailures(t *testing.T) {
	t.Parallel()
	req := &llm.Request{Messages: []llm.Message{
		{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{
				{Type: llm.ContentTypeToolUse, ID: "search", ToolName: "bash", ToolInput: []byte(`{"command":"shelley-history search \"redis\""}`)},
				{Type: llm.ContentTypeToolUse, ID: "read", ToolName: "bash", ToolInput: []byte(`{"command":"cat server/cache.go"}`)},
			},
		},
		{
			SequenceID: 381,
			Role:       llm.MessageRoleUser,
			Content: []llm.Content{
				{Type: llm.ContentTypeToolResult, ToolUseID: "search", ToolError: true, ToolResult: llm.TextContent(strings.Repeat("history\n", 600))},
				{Type: llm.ContentTypeToolResult, ToolUseID: "read", ToolResult: llm.TextContent(strings.Repeat("code\n", 600))},
			},
		},
		{Role: llm.MessageRoleUser, Content: llm.TextContent(strings.Repeat("recent ", 10))},
	}}
	before := estimateRequestTokens(req)
	outbound, stats := ShapeLiveContext(req, before*2, testElisionConfig())
	if stats.ElidedResults != 1 || stats.HistoryResultsElided != 1 {
		t.Fatalf("history result was not selected first: %+v", stats)
	}
	search := outbound.Messages[1].Content[0].ToolResult[0].Text
	if !strings.Contains(search, "status: failed") || !strings.Contains(search, "recover: shelley-history 381 381") {
		t.Fatalf("failed history marker is incomplete:\n%s", search)
	}
	if got := outbound.Messages[1].Content[1].ToolResult[0].Text; !strings.HasPrefix(got, "code\n") {
		t.Fatalf("exploration result should remain after history priority:\n%s", got)
	}
}

func TestShapeLiveContextCollapsesExplorationRun(t *testing.T) {
	t.Parallel()
	req := &llm.Request{Messages: []llm.Message{
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: "cat", ToolName: "bash", ToolInput: []byte(`{"command":"cat server/convo.go"}`),
		}}},
		{SequenceID: 184, Role: llm.MessageRoleUser, Content: []llm.Content{{
			Type: llm.ContentTypeToolResult, ToolUseID: "cat", ToolResult: llm.TextContent(strings.Repeat("source\n", 600)),
		}}},
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: "sed", ToolName: "bash", ToolInput: []byte(`{"command":"sed -n '1,80p' server/convo.go"}`),
		}}},
		{SequenceID: 185, Role: llm.MessageRoleUser, Content: []llm.Content{{
			Type: llm.ContentTypeToolResult, ToolUseID: "sed", ToolResult: llm.TextContent(strings.Repeat("source\n", 600)),
		}}},
		{Role: llm.MessageRoleUser, Content: llm.TextContent(strings.Repeat("recent ", 10))},
	}}
	before := estimateRequestTokens(req)
	outbound, stats := ShapeLiveContext(req, before*2, testElisionConfig())
	if stats.ExplorationResultsElided != 2 {
		t.Fatalf("exploration results elided = %d, want 2: %+v", stats.ExplorationResultsElided, stats)
	}
	leader := outbound.Messages[1].Content[0].ToolResult[0].Text
	if !strings.Contains(leader, "Exploration run elided: 2") ||
		!strings.Contains(leader, "- cat server/convo.go") ||
		!strings.Contains(leader, "- sed -n '1,80p' server/convo.go") ||
		!strings.Contains(leader, "shelley-history 184 185") {
		t.Fatalf("unexpected run marker:\n%s", leader)
	}
	if got := outbound.Messages[3].Content[0].ToolResult[0].Text; got != " " {
		t.Fatalf("run follower = %q, want protocol-preserving blank", got)
	}
}

func TestShapeLiveContextDefersAtCompactionPressure(t *testing.T) {
	t.Parallel()
	req := elisionRequest("grep -R TODO .", strings.Repeat("result\n", 600), 184, false)
	before := estimateRequestTokens(req)
	outbound, stats := ShapeLiveContext(req, int(float64(before)/0.7), testElisionConfig())
	if stats.Decision != "defer_to_compaction" || stats.ElidedResults != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if got, want := outbound.Messages[1].Content[0].ToolResult[0].Text, req.Messages[1].Content[0].ToolResult[0].Text; got != want {
		t.Fatal("request changed at compaction pressure")
	}
}

func TestIsExplorationCommand(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"cat x", "sed -n '1,5p' x", "rg needle", "git diff --stat"} {
		if !isExplorationCommand(command) {
			t.Errorf("%q should be observational", command)
		}
	}
	for _, command := range []string{"go test ./...", "git commit -m x", "python script.py"} {
		if isExplorationCommand(command) {
			t.Errorf("%q should not be observational", command)
		}
	}
}

func elisionRequest(command, result string, seq int64, failed bool) *llm.Request {
	return &llm.Request{Messages: []llm.Message{
		{Role: llm.MessageRoleAssistant, Content: []llm.Content{{
			Type: llm.ContentTypeToolUse, ID: "call", ToolName: "bash", ToolInput: []byte(`{"command":` + quoteJSON(command) + `}`),
		}}},
		{SequenceID: seq, Role: llm.MessageRoleUser, Content: []llm.Content{{
			Type: llm.ContentTypeToolResult, ToolUseID: "call", ToolError: failed, ToolResult: llm.TextContent(result),
		}}},
		{Role: llm.MessageRoleUser, Content: llm.TextContent(strings.Repeat("recent ", 10))},
	}}
}

func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func testElisionConfig() LiveContextElisionConfig {
	return LiveContextElisionConfig{
		StartPressure:       0.4,
		ProgressivePressure: 0.55,
		CompactionPressure:  0.7,
		ProtectedTailTokens: 4,
		LargeResultTokens:   20,
		MinimumResultTokens: 10,
	}
}
