package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"shelley.exe.dev/gitstate"
	"shelley.exe.dev/llm"
)

// Checkpoint-style compaction, selected by FlagAutomaticCompaction. It reuses
// every mechanic in distill_pi.go — the cut point, the verbatim recent tail,
// the single batched write, the rollback — and changes only what the
// summarizer is shown and asked for.
//
// The design is a three-stage pipeline, and the ordering is the point:
//
//	raw history -> deterministic reduction -> one LLM call -> + host facts
//
// Stage 1 costs nothing and no round-trip. Oversized observations (a 40KB file
// read, a screenful of grep hits) are replaced by a bounded digest that keeps
// its [seq:N] marker, so the exact bytes stay addressable in the database
// while the summarizer sees a transcript already free of shell noise. Stage 2
// is exactly one call — see the one-call rule below. Stage 3 appends facts
// Shelley already knows (branch, commit, files patched) rather than paying
// output tokens for the model to infer them from the transcript, sometimes
// wrongly.
//
// The one-call rule: a checkpoint is one LLM call, no retry loop, no
// classification pass, no second model deciding what matters. Compaction sits
// between turns, and the failure it guards against is a context window filling
// up; a fix that doubles the latency of the fix is not a fix. Improvements
// belong in the prompt (input tokens, roughly a quarter the price of output)
// or in stage 1 (free), not in extra round-trips.

// Per-message reduction budgets. A flat cap spends the transcript budget
// evenly across messages worth wildly different amounts. A checkpoint needs
// intent and outcome: what was asked, what was decided, what a command proved.
// Tool results are the bulk of any coding transcript and the least dense per
// byte — a 40KB file read contributes about as much as its first and last
// lines. User messages are the opposite: they carry the requirements and
// constraints the checkpoint exists to preserve, and are rarely long enough to
// need cutting.
const (
	checkpointUserMaxTokens       = 4000
	checkpointAssistantMaxTokens  = 1500
	checkpointToolResultMaxTokens = 600
	// The last checkpointRecentMessages entries get their budget multiplied,
	// because detail near the cut point is what the next turn is most likely
	// to need.
	checkpointRecentMessages     = 30
	checkpointRecentBudgetFactor = 3
	// checkpointTranscriptMaxTokens caps the whole reduced transcript. Past
	// it, the OLDEST entries are dropped first: the newest to-be-summarized
	// messages carry the most continuity value.
	checkpointTranscriptMaxTokens = 60000
)

// checkpointOmissionMarker is the single spelling for "you are looking at a
// partial view". Every truncation site emits it with a count; the prompt quotes
// it with a literal "N" (checkpointOmissionExample). Both come from this one
// format string, because a second spelling would silently defeat the prompt
// instruction that teaches the summarizer to recognize it.
const checkpointOmissionMarker = "[... %s characters omitted ...]"

// checkpointOmissionExample is the marker as quoted in the prompt.
var checkpointOmissionExample = fmt.Sprintf(checkpointOmissionMarker, "N")

// checkpointMessageBudget returns the reduction budget for one rendered entry.
func checkpointMessageBudget(msg llm.Message, recent bool) int {
	budget := checkpointAssistantMaxTokens
	switch {
	case isToolResultMessage(msg):
		budget = checkpointToolResultMaxTokens
	case msg.Role == llm.MessageRoleUser:
		budget = checkpointUserMaxTokens
	}
	if recent {
		budget *= checkpointRecentBudgetFactor
	}
	return budget
}

// withoutThinking returns msg with hidden assistant thinking removed. A
// checkpoint records observable state and the reasons given for it; thinking is
// the bulkiest content in a transcript and the least load-bearing, since
// anything it concluded that mattered was acted on and therefore visible.
func withoutThinking(msg llm.Message) llm.Message {
	kept := make([]llm.Content, 0, len(msg.Content))
	for _, c := range msg.Content {
		if c.Type == llm.ContentTypeThinking || c.Type == llm.ContentTypeRedactedThinking {
			continue
		}
		kept = append(kept, c)
	}
	msg.Content = kept
	return msg
}

// failedOrEmptyToolResult reports whether a tool result errored or produced no
// output, and returns a one-line stand-in for it.
//
// These collapse to one line and never to zero. "grep found nothing" and "the
// command exited 1" are negative knowledge — the same class as an approach
// tried and abandoned — and negative knowledge is precisely what summarizers
// have been measured losing. Every "this output did not matter" heuristic
// scores an empty result as noise, which is backwards.
func failedOrEmptyToolResult(msg llm.Message) (string, bool) {
	if !isToolResultMessage(msg) {
		return "", false
	}
	failed := false
	body := ""
	for _, c := range msg.Content {
		if c.Type != llm.ContentTypeToolResult {
			continue
		}
		if c.ToolError {
			failed = true
		}
		for _, r := range c.ToolResult {
			body += r.Text
		}
	}
	trimmed := strings.TrimSpace(body)
	switch {
	case failed:
		return "[Tool result]: failed: " + firstLine(trimmed, 200), true
	case trimmed == "":
		return "[Tool result]: (no output)", true
	}
	return "", false
}

// firstLine returns the first line of s, capped at maxChars.
func firstLine(s string, maxChars int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > maxChars {
		s = truncateUTF8(s, maxChars)
	}
	if s == "" {
		return "(no detail)"
	}
	return s
}

// reduceCheckpointTranscript is stage 1: it renders the messages to be
// summarized as a plain-text transcript with each entry prefixed by its
// [seq:N] marker, bounded per message and overall, with no LLM involved.
//
// Nothing here deletes data. It controls only what the summarizer sees; the
// original rows are untouched and remain queryable, which is what makes
// aggressive reduction safe — a dropped detail is one SQL query away, provided
// its [seq:N] survives, so every entry keeps its marker even when its body is
// cut to a single line.
func reduceCheckpointTranscript(entries []piContextMessage) string {
	type rendered struct {
		seq    int64
		text   string
		tokens int
	}
	var lines []rendered
	for i, entry := range entries {
		msg := withoutThinking(entry.llm)
		body, collapsed := failedOrEmptyToolResult(msg)
		if !collapsed {
			body = serializePiConversation([]llm.Message{msg})
			if strings.TrimSpace(body) == "" {
				continue
			}
			recent := i >= len(entries)-checkpointRecentMessages
			body = truncateMiddleForSummary(body, checkpointMessageBudget(msg, recent))
		}
		lines = append(lines, rendered{seq: entry.source.SequenceID, text: body, tokens: estimateCharTokens(body)})
	}

	total := 0
	for _, l := range lines {
		total += l.tokens
	}
	start := 0
	for start < len(lines) && total > checkpointTranscriptMaxTokens {
		total -= lines[start].tokens
		start++
	}

	var parts []string
	if start > 0 {
		parts = append(parts, fmt.Sprintf(
			"[... %d earlier messages (seq %d-%d) omitted from this transcript to control cost; they are still in the database and can be read by sequence number ...]",
			start, lines[0].seq, lines[start-1].seq))
	}
	for _, l := range lines[start:] {
		parts = append(parts, fmt.Sprintf("[seq:%d] %s", l.seq, l.text))
	}
	return strings.Join(parts, "\n\n")
}

// estimateCharTokens applies the char/4 heuristic used by
// estimatePiMessageTokens to an already-rendered string.
func estimateCharTokens(s string) int {
	return (len(s) + 3) / 4
}

// truncateMiddleForSummary caps s to roughly maxTokens, keeping the head and
// the tail and dropping the middle. For a tool call or a command the start
// carries the intent and the end carries the result, so the middle is the
// least valuable part to cut.
func truncateMiddleForSummary(s string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(s) <= maxChars {
		return s
	}
	keepEachSide := maxChars / 2
	headEnd := keepEachSide
	for headEnd > 0 && !utf8.RuneStart(s[headEnd]) {
		headEnd--
	}
	tailStart := len(s) - keepEachSide
	for tailStart < len(s) && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	head, tail := s[:headEnd], s[tailStart:]
	omitted := strconv.Itoa(len(s) - len(head) - len(tail))
	return head + "\n" + fmt.Sprintf(checkpointOmissionMarker, omitted) + "\n" + tail
}

// checkpointSummarizationPrompt asks for small, source-mapped working state: a
// tiny task graph, then short sourced notes. Both halves are deliberate.
//
// The graph answers "what is true now" in fixed keys. Prose answers "why", which
// is what someone picking the work up cold needs and what only a model can
// write — but prose is a poor place to look up whether an approach was already
// ruled out, because the answer is a clause inside a paragraph: cheap to read,
// expensive to scan, easy to contradict by accident at the next compaction. The
// graph exists mainly for `rejected`. Work already done cannot be undone by
// forgetting it; an approach abandoned for a reason and not recorded gets
// attempted a second time.
//
// The node cap is not decoration. Given no limit, summarizers emit every
// passing finding as a node — measured at 9-15 per conversation, +33% output
// tokens for no improvement in what was preserved. Hence both the cap and the
// explicit definition of what earns a node.
var checkpointSummarizationPrompt = `The messages above are a conversation to summarize. Produce a compact working state that another LLM will use to continue this work. It replaces the transcript in context, so it must be enough to carry on from.

Start with a task graph in a fenced ` + "`state`" + ` block:

` + "```" + `state
goal: <the overall objective, one line>
<task name>: done|active|blocked|rejected [seq:N]
  <subtask name>: done|active|blocked|rejected [seq:N]
` + "```" + `

A task earns a line only if its status is settled or it is currently in play. A passing observation is not a task. Use at most 8 lines total; indent a line to show it is part of the one above it. ` + "`rejected`" + ` means tried and abandoned — always record these, with the reason in the notes below, or the next reader will try it again. ` + "`blocked`" + ` needs a note saying what would unblock it.

Then write short sourced notes, grouped by topic:

## <Topic>
- <A durable fact, decision, constraint, or user requirement.> [seq:N]

Requirements:
- Record user requirements, preferences, decisions, and the reasons for them. Rank these ABOVE identifiers: a reader who loses a file path can search for it, a reader who loses a constraint breaks it.
- Keep exact file paths, symbols, commands, and error messages when they are what a claim is about.
- Describe the current state. Omit routine verification history unless it changes what happens next.
- Write notes, not narrative. No chronology, no restating what the transcript said in order.

Pointers:
- Every transcript entry above is prefixed with its sequence number, like "[seq:42]".
- Cite that exact marker inline, attached to the claim it supports. Use [seq:42] for one message or [seq:42-58] for a span.
- Cite ONLY numbers that actually appear above. Never invent, guess, or round one. A pointer is the reader's route back to the original text; an invented pointer sends them somewhere unrelated with nothing to signal it was made up.
- Prefer a claim plus a pointer over a long quotation. The pointer is how a claim stays one line.
- Where a previous summary appears above with its own pointers, carry those pointers forward unchanged. This is not a second round of compression: facts and identifiers a previous summary preserved must survive intact.
- A "` + checkpointOmissionExample + `" note means you are seeing part of an entry. Summarize what is visible; do not guess at the rest. Its pointer still resolves to the whole thing.`

// checkpointSummaryPrefix/Suffix wrap the summary as presented to the reading
// model. The suffix is the retrieval half of checkpointing: it says outright
// that history is intact and shows how to read it.
//
// Retrieval is what makes an aggressively reduced summary correct rather than
// merely small. The summary is lossy on purpose; the message stream is
// lossless; the [seq:N] pointers are the join between them, and they are only
// worth anything if the reading model knows they are resolvable. Shelley
// already exports SHELLEY_DB and SHELLEY_CONVERSATION_ID to every bash command
// (see claudetool.ShelleyEnv), and compaction does not delete rows — it starts
// a new generation and leaves the old one in place — so the exact original text
// is one query away with the tools the agent already has. No dedicated
// retrieval tool is needed for this.
const checkpointSummaryPrefix = `The conversation history before this point was compacted into the working state below. The original messages were NOT deleted; see the retrieval note after it.

<working-state>
`

const checkpointSummaryRetrievalSuffix = `
</working-state>

Each [seq:N] or [seq:N-M] above cites an original message by sequence number. Those messages are still in shelley's database, verbatim. To read one, query it from bash:

sqlite3 "$SHELLEY_DB" "SELECT sequence_id, type, json_extract(llm_data,'$.Content[0].Text') FROM messages WHERE conversation_id='$SHELLEY_CONVERSATION_ID' AND sequence_id BETWEEN <first> AND <last> ORDER BY sequence_id;"

Tool calls and results keep their payload deeper in the llm_data JSON, so select the whole llm_data column for those rows. To search history instead of following a pointer, replace the sequence range with AND llm_data LIKE '%term%'.

Do this when exact wording or surrounding evidence matters — a requirement you are about to act on, an error you are about to re-fix, a decision you are about to reverse. The working state above is a summary and may have dropped the detail you need; the pointers are how you get it back.`

// historyPointerPattern matches [seq:N] and [seq:N-M].
var historyPointerPattern = regexp.MustCompile(`\[seq:(\d+)(?:-(\d+))?\]`)

// sanitizeCheckpointPointers removes any pointer that does not name a real
// message in this conversation, replacing it with a visible marker.
//
// A fabricated pointer is worse than no citation. With no citation the reader
// knows the summary is all they have; with a bad one they run a query, get
// something unrelated or nothing, and have no signal that the citation was
// invented. Marking the removal keeps that honest.
//
// Validity is existence, not membership in the summarized span. The prompt
// tells the model to carry pointers forward from a previous summary, and those
// cite messages from before this compaction's input — rejecting them would
// delete correct citations for following instructions, and would erase exactly
// the oldest evidence that recursive summarizing is least able to reconstruct.
func sanitizeCheckpointPointers(summary string, valid map[int64]bool) (string, int) {
	removed := 0
	out := historyPointerPattern.ReplaceAllStringFunc(summary, func(match string) string {
		sub := historyPointerPattern.FindStringSubmatch(match)
		first, err := strconv.ParseInt(sub[1], 10, 64)
		if err != nil || !valid[first] {
			removed++
			return "[unverifiable pointer removed]"
		}
		if sub[2] != "" {
			last, err := strconv.ParseInt(sub[2], 10, 64)
			if err != nil || !valid[last] || last < first {
				removed++
				return "[unverifiable pointer removed]"
			}
		}
		return match
	})
	return out, removed
}

// validateCheckpointSummary rejects output that is not a working state at all —
// a refusal, an apology, unstructured prose — so a bad summary never becomes
// the next generation's opening context. The caller rolls back on error, which
// leaves the conversation uncompacted rather than gutted.
func validateCheckpointSummary(summary string) error {
	if !strings.Contains(summary, "goal:") {
		return fmt.Errorf("checkpoint summary has no state block")
	}
	if !strings.Contains(summary, "## ") {
		return fmt.Errorf("checkpoint summary has no topic notes")
	}
	return nil
}

// checkpointHostFacts is stage 3: repository state Shelley knows for certain,
// appended after the model's output.
//
// Shelley already has the branch, the commit, and every path passed to patch.
// A model asked for them spends output tokens inferring them from the
// transcript and is occasionally wrong, and a wrong SHA is more expensive than
// a missing one. Generating them directly is cheaper and correct by
// construction. No fidelity claim attaches to this beyond that — it is a token
// and correctness change, and the prompt does not ask for these facts.
func checkpointHostFacts(cwd string, summarized []piContextMessage) string {
	var lines []string
	if st := gitstate.GetGitState(cwd); st != nil && st.IsRepo {
		branch := st.Branch
		if branch == "" {
			branch = "(detached)"
		}
		lines = append(lines, fmt.Sprintf("branch: %s at %s", branch, st.Commit))
		if st.Subject != "" {
			lines = append(lines, "head: "+st.Subject)
		}
	}
	edits := map[string]int{}
	for _, entry := range summarized {
		if entry.llm.Role != llm.MessageRoleAssistant {
			continue
		}
		for _, c := range entry.llm.Content {
			if c.Type != llm.ContentTypeToolUse || c.ToolName != "patch" || len(c.ToolInput) == 0 {
				continue
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal(c.ToolInput, &args); err != nil {
				continue
			}
			if path := jsonStringField(args, "path"); path != "" {
				edits[path]++
			}
		}
	}
	if len(edits) > 0 {
		paths := make([]string, 0, len(edits))
		for p := range edits {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		lines = append(lines, "edited in the summarized span:")
		for _, p := range paths {
			lines = append(lines, fmt.Sprintf("  %s (%d edits)", p, edits[p]))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n```repo-state\n" + strings.Join(lines, "\n") + "\n```"
}
