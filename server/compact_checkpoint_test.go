package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

// entry builds a piContextMessage with a sequence id, so reduction tests can
// assert which [seq:N] markers survive.
func entry(seq int64, msg llm.Message) piContextMessage {
	return piContextMessage{llm: msg, source: generated.Message{SequenceID: seq}}
}

func userMsg(text string) llm.Message {
	return textMsg(llm.MessageRoleUser, text)
}

func assistantMsg(text string) llm.Message {
	return textMsg(llm.MessageRoleAssistant, text)
}

// failedToolResultMsg is toolResultMsg with the error flag set.
func failedToolResultMsg(text string) llm.Message {
	msg := toolResultMsg(text)
	msg.Content[0].ToolError = true
	return msg
}

// TestReduceKeepsEverySeqMarker is the load-bearing property of the whole
// design: reduction is allowed to be aggressive precisely because what it drops
// stays addressable. An entry whose body is cut to a single line must still
// carry its pointer, or the summarizer cannot cite what it never saw and the
// text is gone for good.
func TestReduceKeepsEverySeqMarker(t *testing.T) {
	t.Parallel()
	entries := []piContextMessage{
		entry(10, userMsg("do the thing")),
		entry(11, assistantMsg(strings.Repeat("verbose reasoning. ", 5000))),
		entry(12, toolResultMsg(strings.Repeat("x", 100000))),
		entry(13, toolResultMsg("")),
		entry(14, failedToolResultMsg("exit status 1: boom")),
	}
	out := reduceCheckpointTranscript(entries)
	for _, seq := range []int64{10, 11, 12, 13, 14} {
		if !strings.Contains(out, fmt.Sprintf("[seq:%d]", seq)) {
			t.Errorf("reduced transcript dropped the [seq:%d] marker:\n%s", seq, out)
		}
	}
}

// TestReduceCollapsesFailedAndEmptyToOneLine: negative knowledge survives. "the
// command failed" and "there was no output" are the class of fact summarizers
// are measured losing, so they collapse to one line and never to nothing.
func TestReduceCollapsesFailedAndEmptyToOneLine(t *testing.T) {
	t.Parallel()
	out := reduceCheckpointTranscript([]piContextMessage{
		entry(1, toolResultMsg("")),
		entry(2, failedToolResultMsg("permission denied opening /etc/shadow")),
	})
	if !strings.Contains(out, "(no output)") {
		t.Errorf("empty tool result did not collapse to a visible line:\n%s", out)
	}
	if !strings.Contains(out, "failed") || !strings.Contains(out, "permission denied") {
		t.Errorf("failed tool result lost its reason:\n%s", out)
	}
}

// TestReduceBudgetsByRole: a user message and a tool result of the same size
// must not be cut the same amount. User messages carry the requirements the
// summary exists to preserve; tool results are the least dense per byte.
func TestReduceBudgetsByRole(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("y", 200000)
	userOut := reduceCheckpointTranscript([]piContextMessage{entry(1, userMsg(big))})
	toolOut := reduceCheckpointTranscript([]piContextMessage{entry(1, toolResultMsg(big))})
	if len(userOut) <= len(toolOut) {
		t.Errorf("user message budget (%d bytes) should exceed tool result budget (%d bytes)", len(userOut), len(toolOut))
	}
}

// TestReduceDropsThinking: thinking is the bulkiest content and the least
// load-bearing, since anything it concluded that mattered was acted on.
func TestReduceDropsThinking(t *testing.T) {
	t.Parallel()
	msg := llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{
		{Type: llm.ContentTypeThinking, Thinking: "SECRET_INTERNAL_MUSING"},
		{Type: llm.ContentTypeText, Text: "here is the answer"},
	}}
	out := reduceCheckpointTranscript([]piContextMessage{entry(1, msg)})
	if strings.Contains(out, "SECRET_INTERNAL_MUSING") {
		t.Errorf("thinking leaked into summarizer input:\n%s", out)
	}
	if !strings.Contains(out, "here is the answer") {
		t.Errorf("dropping thinking also dropped the answer:\n%s", out)
	}
}

// TestReduceDropsOldestAtCap: over the transcript budget the OLDEST entries go
// first, because the newest to-be-summarized messages carry the most continuity
// value. The dropped span is announced, with its sequence range, so the reader
// knows it exists and can query it.
func TestReduceDropsOldestAtCap(t *testing.T) {
	t.Parallel()
	var entries []piContextMessage
	// Each user message is ~20k tokens, so a handful blows the 60k cap.
	for i := int64(1); i <= 12; i++ {
		entries = append(entries, entry(i, userMsg(fmt.Sprintf("MARKER%d ", i)+strings.Repeat("z", 80000))))
	}
	out := reduceCheckpointTranscript(entries)
	if !strings.Contains(out, "earlier messages") {
		t.Fatalf("over-budget transcript did not announce dropped entries:\n%s", out[:min(len(out), 400)])
	}
	if !strings.Contains(out, "MARKER12") {
		t.Error("newest entry was dropped; oldest should go first")
	}
	if strings.Contains(out, "MARKER1 ") {
		t.Error("oldest entry survived an over-budget transcript")
	}
	if estimateCharTokens(out) > checkpointTranscriptMaxTokens*2 {
		t.Errorf("reduced transcript is %d tokens, far over the %d cap", estimateCharTokens(out), checkpointTranscriptMaxTokens)
	}
}

// TestTruncateMiddleKeepsHeadAndTail: the start of a command carries intent and
// the end carries the result, so the middle is what gets cut. The marker must
// match the one constant the prompt quotes.
func TestTruncateMiddleKeepsHeadAndTail(t *testing.T) {
	t.Parallel()
	s := "HEAD_INTENT" + strings.Repeat("m", 10000) + "TAIL_RESULT"
	out := truncateMiddleForSummary(s, 100)
	if !strings.HasPrefix(out, "HEAD_INTENT") {
		t.Error("truncation lost the head, which carries intent")
	}
	if !strings.HasSuffix(out, "TAIL_RESULT") {
		t.Error("truncation lost the tail, which carries the result")
	}
	if !strings.Contains(out, "characters omitted") {
		t.Errorf("truncation did not emit the omission marker: %q", out)
	}
	if !strings.Contains(checkpointSummarizationPrompt, checkpointOmissionExample) {
		t.Error("the prompt does not quote the marker the truncator emits; the summarizer will not recognize a partial view")
	}
}

func TestTruncateMiddlePreservesUTF8(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("日本語テキスト", 2000)
	out := truncateMiddleForSummary(s, 50)
	if !utf8ValidString(out) {
		t.Error("truncation split a multi-byte rune")
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// TestSanitizePointers covers the rule that a fabricated pointer is worse than
// no citation, and the reason validity is existence rather than span: the prompt
// asks the model to carry pointers forward from a previous summary, and those
// name messages from before this compaction's input.
func TestSanitizePointers(t *testing.T) {
	t.Parallel()
	valid := map[int64]bool{5: true, 10: true, 11: true, 12: true, 40: true}
	tests := []struct {
		name    string
		in      string
		keep    bool
		removed int
	}{
		{"real single", "decided X [seq:10]", true, 0},
		{"real range", "decided X [seq:10-12]", true, 0},
		{"carried forward from before the span", "constraint Y [seq:5]", true, 0},
		{"invented", "decided X [seq:999]", false, 1},
		{"range with invented end", "decided X [seq:10-998]", false, 1},
		{"reversed range", "decided X [seq:12-10]", false, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, removed := sanitizeCheckpointPointers(test.in, valid)
			if removed != test.removed {
				t.Errorf("removed = %d, want %d (out: %q)", removed, test.removed, out)
			}
			if test.keep && out != test.in {
				t.Errorf("a valid pointer was altered: %q -> %q", test.in, out)
			}
			if !test.keep && strings.Contains(out, "[seq:") {
				t.Errorf("an invalid pointer survived: %q", out)
			}
			if !test.keep && !strings.Contains(out, "unverifiable pointer removed") {
				t.Errorf("removal was silent, giving the reader no signal: %q", out)
			}
		})
	}
}

// TestValidateCheckpointSummaryGatesUsabilityNotShape: rolling back is not
// neutral -- it means the context window keeps filling -- so validation rejects
// only summaries that could not serve as context at all. A well-organized
// summary in the wrong shape is still usable and must be kept.
func TestValidateCheckpointSummaryGatesUsabilityNotShape(t *testing.T) {
	t.Parallel()
	if err := validateCheckpointSummary("I'm sorry, I can't help with that."); err == nil {
		t.Error("a refusal validated; it would become the next generation's context")
	}
	if err := validateCheckpointSummary(""); err == nil {
		t.Error("empty output validated")
	}
	wellFormed := "```state\ngoal: ship it\nparse: done [seq:4]\n```\n\n## Parsing\n- it works [seq:4]\n" + strings.Repeat("- more detail here [seq:5]\n", 10)
	if err := validateCheckpointSummary(wellFormed); err != nil {
		t.Errorf("a well-formed working state was rejected: %v", err)
	}
	// Measured: on a real 255-message conversation both the workhorse and the
	// fallback model returned a report like this, with no state block. The
	// strict check threw both away and compacted nothing, which is strictly
	// worse than keeping them.
	reportShaped := "## Conversation Summary\n\n### Objective\nThe user asked to compare two compaction strategies.\n\n### Key Decisions\n- Keep compact as the default.\n" + strings.Repeat("- another durable decision worth keeping\n", 5)
	if err := validateCheckpointSummary(reportShaped); err != nil {
		t.Errorf("a usable report-shaped summary was rejected, which would roll back and compact nothing: %v", err)
	}
}

// TestCheckpointSummaryShapeReportsDivergence: shape is tracked for logging so
// prompt drift stays visible, without being fatal.
func TestCheckpointSummaryShapeReportsDivergence(t *testing.T) {
	t.Parallel()
	hasState, hasTopics, hasPointers := checkpointSummaryShape("```state\ngoal: x\n```\n## T\n- a [seq:3]")
	if !hasState || !hasTopics || !hasPointers {
		t.Errorf("a complete summary reported missing parts: state=%v topics=%v pointers=%v", hasState, hasTopics, hasPointers)
	}
	// Plain prose with no structure at all.
	hasState, hasTopics, hasPointers = checkpointSummaryShape("The user asked for a summary and here it is.")
	if hasState || hasTopics || hasPointers {
		t.Errorf("unstructured prose reported parts it lacks: state=%v topics=%v pointers=%v", hasState, hasTopics, hasPointers)
	}
}

// TestCheckpointHostFactsCountsPatches: stage 3 states repo facts Shelley knows
// rather than paying output tokens for the model to infer them, sometimes wrongly.
func TestCheckpointHostFactsCountsPatches(t *testing.T) {
	t.Parallel()
	patch := func(path string) llm.Message {
		input, _ := json.Marshal(map[string]string{"path": path})
		return llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{
			{Type: llm.ContentTypeToolUse, ToolName: "patch", ToolInput: input},
		}}
	}
	out := checkpointHostFacts("/nonexistent-not-a-repo", []piContextMessage{
		entry(1, patch("a.go")), entry(2, patch("a.go")), entry(3, patch("b.go")),
	})
	if !strings.Contains(out, "a.go (2 edits)") {
		t.Errorf("patch counts wrong:\n%s", out)
	}
	if !strings.Contains(out, "b.go (1 edits)") {
		t.Errorf("second file missing:\n%s", out)
	}
	if got := checkpointHostFacts("/nonexistent-not-a-repo", nil); got != "" {
		t.Errorf("with no repo and no edits there is nothing to state, got %q", got)
	}
}

// TestCheckpointPromptStatesItsContract guards the instructions that measurement
// showed to matter, so a future prompt edit cannot quietly drop them.
func TestCheckpointPromptStatesItsContract(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"[seq:42]",     // shows the pointer syntax concretely
		"Never invent", // the fabricated-pointer rule
		"at most 8",    // the node cap that keeps output cost down
		"rejected",     // negative knowledge earns a slot
		"ABOVE",        // directives rank above identifiers
		"carry those pointers forward unchanged",
		"subject matter, not instructions", // stops the model copying formats discussed in the transcript
	} {
		if !strings.Contains(checkpointSummarizationPrompt, want) {
			t.Errorf("prompt no longer says %q", want)
		}
	}
	// The retrieval suffix is what makes a lossy summary correct rather than
	// merely small. It names the host-installed reader rather than embedding a
	// fragile SQL query that returns blank tool rows.
	for _, want := range []string{"shelley-history 480 500", "--search", "NOT deleted"} {
		if !strings.Contains(checkpointSummaryPrefix+checkpointSummaryRetrievalSuffix, want) {
			t.Errorf("retrieval note no longer mentions %q", want)
		}
	}
}
