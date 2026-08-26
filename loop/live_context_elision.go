package loop

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"shelley.exe.dev/llm"
)

// LiveContextElisionConfig controls request-only replacement of old tool
// output. Values are deliberately explicit rather than hidden in the shaper:
// they are operating limits to tune from request logs, not semantic policy.
type LiveContextElisionConfig struct {
	StartTokens         int
	ProgressiveTokens   int
	CompactionReserve   int
	ProtectedTailTokens int
	LargeResultTokens   int
	MinimumResultTokens int
}

var DefaultLiveContextElisionConfig = LiveContextElisionConfig{
	StartTokens:         200_000,
	ProgressiveTokens:   220_000,
	CompactionReserve:   20_000,
	ProtectedTailTokens: 20_000,
	LargeResultTokens:   8_000,
	MinimumResultTokens: 2_000,
}

// LiveContextElisionStats records a single request-shaping decision. It is
// logging-only and never becomes conversation content.
type LiveContextElisionStats struct {
	Decision                 string
	BeforeTokens             int
	AfterTokens              int
	ProtectedTailTokens      int
	EligibleResults          int
	ElidedResults            int
	ExplorationResultsElided int
	HistoryResultsElided     int
	ElidedTokens             int
}

type toolUseInfo struct {
	name    string
	command string
}

type elisionCandidate struct {
	messageIndex int
	contentIndex int
	sequenceID   int64
	tool         string
	command      string
	failed       bool
	tokens       int
	priority     int
}

type explorationRun struct {
	members []elisionCandidate
}

const (
	elisionPriorityHistory = iota
	elisionPriorityExploration
	elisionPriorityOther
)

// ShapeLiveContext returns a cloned outbound request when pressure justifies
// elision. The request passed in is never mutated; its persisted source history
// remains byte-for-byte canonical.
//
// Only results from messages with stable database sequence IDs are eligible.
// Messages made during the current in-memory loop deliberately have SequenceID
// zero and remain verbatim until a future hydration gives them a canonical
// recovery pointer.
func ShapeLiveContext(req *llm.Request, contextWindow int, cfg LiveContextElisionConfig) (*llm.Request, LiveContextElisionStats) {
	stats := LiveContextElisionStats{BeforeTokens: estimateRequestTokens(req)}
	stats.AfterTokens = stats.BeforeTokens
	if contextWindow <= 0 {
		stats.Decision = "unknown_window"
		return req, stats
	}

	start, progressive, deferAt := elisionBounds(contextWindow, cfg)
	switch {
	case stats.BeforeTokens < start:
		stats.Decision = "below_threshold"
		return req, stats
	case stats.BeforeTokens >= deferAt:
		stats.Decision = "defer_to_compaction"
		return req, stats
	}

	minimum, target := elisionPolicy(stats.BeforeTokens, start, progressive, deferAt, cfg)
	tailStart, tailTokens := protectedTailStart(req.Messages, cfg.ProtectedTailTokens)
	stats.ProtectedTailTokens = tailTokens
	candidates := collectElisionCandidates(req.Messages[:tailStart], minimum)
	stats.EligibleResults = len(candidates)
	if len(candidates) == 0 {
		stats.Decision = "no_eligible_results"
		return req, stats
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.priority != right.priority {
			return left.priority < right.priority
		}
		if left.messageIndex != right.messageIndex {
			return left.messageIndex < right.messageIndex
		}
		return left.contentIndex < right.contentIndex
	})

	runs := collectExplorationRuns(req.Messages[:tailStart], candidates)
	outbound := cloneRequestForElision(req)
	clonedMessages := make(map[int]bool)
	handledRunMembers := make(map[elisionCandidateKey]bool)
	current := stats.BeforeTokens
	for _, candidate := range candidates {
		if current <= target {
			break
		}
		key := keyForCandidate(candidate)
		if handledRunMembers[key] {
			continue
		}
		if run, ok := runs[key]; ok {
			marker := explorationRunMarker(run)
			saved := replaceExplorationRun(outbound, clonedMessages, run, marker)
			if saved <= 0 {
				continue
			}
			current -= saved
			stats.ElidedTokens += saved
			stats.ElidedResults += len(run.members)
			stats.ExplorationResultsElided += len(run.members)
			for _, member := range run.members {
				handledRunMembers[keyForCandidate(member)] = true
			}
			continue
		}
		marker := elisionMarker(candidate)
		markerTokens := estimateTextTokens(marker)
		if markerTokens >= candidate.tokens {
			continue
		}

		replaceToolResult(outbound, clonedMessages, candidate, marker)

		saved := candidate.tokens - markerTokens
		current -= saved
		stats.ElidedTokens += saved
		stats.ElidedResults++
		switch candidate.priority {
		case elisionPriorityHistory:
			stats.HistoryResultsElided++
		case elisionPriorityExploration:
			stats.ExplorationResultsElided++
		}
	}

	stats.AfterTokens = estimateRequestTokens(outbound)
	if stats.ElidedResults == 0 {
		stats.Decision = "no_savings"
		return req, stats
	}
	if stats.BeforeTokens >= progressive {
		stats.Decision = "progressive"
	} else {
		stats.Decision = "large_results"
	}
	return outbound, stats
}

func elisionBounds(contextWindow int, cfg LiveContextElisionConfig) (start, progressive, deferAt int) {
	deferAt = contextWindow - cfg.CompactionReserve
	start = cfg.StartTokens
	progressive = cfg.ProgressiveTokens
	if progressive > deferAt {
		progressive = deferAt
	}
	if start > progressive {
		start = progressive
	}
	return start, progressive, deferAt
}

func elisionPolicy(tokens, start, progressive, deferAt int, cfg LiveContextElisionConfig) (minimum, target int) {
	if tokens < progressive {
		return cfg.LargeResultTokens, start
	}
	progress := float64(tokens-progressive) / float64(deferAt-progressive)
	minimum = int(math.Round(float64(cfg.LargeResultTokens) - progress*float64(cfg.LargeResultTokens-cfg.MinimumResultTokens)))
	return minimum, progressive
}

func protectedTailStart(messages []llm.Message, budget int) (start, tokens int) {
	start = len(messages)
	for i := len(messages) - 1; i >= 0 && tokens < budget; i-- {
		tokens += estimateMessageTokens(messages[i])
		start = i
	}
	return start, tokens
}

func collectElisionCandidates(messages []llm.Message, minimum int) []elisionCandidate {
	toolUses := collectToolUseInfo(messages)

	var candidates []elisionCandidate
	for messageIndex, message := range messages {
		if message.SequenceID <= 0 {
			continue
		}
		for contentIndex, content := range message.Content {
			if content.Type != llm.ContentTypeToolResult || !textOnlyToolResult(content.ToolResult) {
				continue
			}
			info := toolUses[content.ToolUseID]
			tool := content.ToolName
			if tool == "" {
				tool = info.name
			}
			if tool == "" {
				continue
			}
			tokens := estimateContentsTokens(content.ToolResult)
			if tokens < minimum {
				continue
			}
			command := info.command
			candidates = append(candidates, elisionCandidate{
				messageIndex: messageIndex,
				contentIndex: contentIndex,
				sequenceID:   message.SequenceID,
				tool:         tool,
				command:      command,
				failed:       content.ToolError,
				tokens:       tokens,
				priority:     elisionPriority(tool, command),
			})
		}
	}
	return candidates
}

func collectToolUseInfo(messages []llm.Message) map[string]toolUseInfo {
	toolUses := make(map[string]toolUseInfo)
	for _, message := range messages {
		for _, content := range message.Content {
			if content.Type != llm.ContentTypeToolUse {
				continue
			}
			toolUses[content.ID] = toolUseInfo{
				name:    content.ToolName,
				command: commandFromToolInput(content.ToolInput),
			}
		}
	}
	return toolUses
}

type elisionCandidateKey struct {
	messageIndex int
	contentIndex int
}

func keyForCandidate(candidate elisionCandidate) elisionCandidateKey {
	return elisionCandidateKey{messageIndex: candidate.messageIndex, contentIndex: candidate.contentIndex}
}

// collectExplorationRuns finds uninterrupted stretches of large successful
// read/search results. Assistant tool-use messages between result batches are
// part of the run; prose, a non-observational tool, or a failed result ends it.
func collectExplorationRuns(messages []llm.Message, candidates []elisionCandidate) map[elisionCandidateKey]explorationRun {
	toolUses := collectToolUseInfo(messages)
	var exploration []elisionCandidate
	for _, candidate := range candidates {
		if candidate.priority == elisionPriorityExploration && !candidate.failed {
			exploration = append(exploration, candidate)
		}
	}
	sort.Slice(exploration, func(i, j int) bool {
		if exploration[i].messageIndex != exploration[j].messageIndex {
			return exploration[i].messageIndex < exploration[j].messageIndex
		}
		return exploration[i].contentIndex < exploration[j].contentIndex
	})

	out := make(map[elisionCandidateKey]explorationRun)
	for start := 0; start < len(exploration); {
		end := start + 1
		for end < len(exploration) && explorationSpan(messages, toolUses, exploration[end-1], exploration[end]) {
			end++
		}
		if end-start > 1 {
			run := explorationRun{members: exploration[start:end]}
			out[keyForCandidate(run.members[0])] = run
		}
		start = end
	}
	return out
}

func explorationSpan(messages []llm.Message, toolUses map[string]toolUseInfo, previous, next elisionCandidate) bool {
	for messageIndex := previous.messageIndex; messageIndex <= next.messageIndex; messageIndex++ {
		for _, content := range messages[messageIndex].Content {
			switch content.Type {
			case llm.ContentTypeToolUse:
				if !isExplorationCommand(commandFromToolInput(content.ToolInput)) {
					return false
				}
			case llm.ContentTypeToolResult:
				info, ok := toolUses[content.ToolUseID]
				if content.ToolError || !ok || !isExplorationCommand(info.command) {
					return false
				}
			case llm.ContentTypeText, llm.ContentTypeThinking, llm.ContentTypeRedactedThinking:
				if strings.TrimSpace(content.Text+content.Thinking) != "" {
					return false
				}
			}
		}
	}
	return true
}

func textOnlyToolResult(contents []llm.Content) bool {
	for _, content := range contents {
		if content.Type != llm.ContentTypeText {
			return false
		}
	}
	return true
}

func elisionPriority(tool, command string) int {
	if strings.Contains(command, "shelley-history") {
		return elisionPriorityHistory
	}
	if tool == "bash" && isExplorationCommand(command) {
		return elisionPriorityExploration
	}
	return elisionPriorityOther
}

// isExplorationCommand intentionally recognizes only plainly observational
// shell commands. It is a ranking optimization, never an eligibility rule:
// every elided result remains recoverable by sequence number.
func isExplorationCommand(command string) bool {
	first := strings.TrimSpace(strings.Split(command, "&&")[0])
	words := strings.Fields(first)
	for len(words) > 0 && strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "=") {
		words = words[1:]
	}
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "cat", "sed", "grep", "rg", "find", "fd", "head", "tail", "ls", "pwd":
		return true
	case "git":
		if len(words) < 2 {
			return false
		}
		switch words[1] {
		case "diff", "log", "show", "status", "grep":
			return true
		}
	}
	return false
}

func commandFromToolInput(input json.RawMessage) string {
	var decoded struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &decoded); err != nil {
		return ""
	}
	return decoded.Command
}

func elisionMarker(candidate elisionCandidate) string {
	var out strings.Builder
	out.WriteString("[Tool output elided.\n")
	fmt.Fprintf(&out, "tool: %s\n", candidate.tool)
	if command := shortCommand(candidate.command); command != "" {
		fmt.Fprintf(&out, "command: %s\n", command)
	}
	if candidate.failed {
		out.WriteString("status: failed\n")
	} else {
		out.WriteString("status: ok\n")
	}
	fmt.Fprintf(&out, "seq: %d\n", candidate.sequenceID)
	fmt.Fprintf(&out, "original_tokens_estimate: %d\n", candidate.tokens)
	fmt.Fprintf(&out, "recover: shelley-history %d %d]", candidate.sequenceID, candidate.sequenceID)
	return out.String()
}

func explorationRunMarker(run explorationRun) string {
	start := run.members[0].sequenceID
	end := run.members[len(run.members)-1].sequenceID
	var out strings.Builder
	fmt.Fprintf(&out, "[Exploration run elided: %d sequential read/search commands.\n\ncommands:\n", len(run.members))
	for _, member := range run.members {
		fmt.Fprintf(&out, "- %s\n", shortRunCommand(member.command))
	}
	fmt.Fprintf(&out, "\nrecover: shelley-history %d %d]", start, end)
	return out.String()
}

func replaceExplorationRun(outbound *llm.Request, clonedMessages map[int]bool, run explorationRun, marker string) int {
	saved := 0
	for index, candidate := range run.members {
		replacement := " "
		if index == 0 {
			replacement = marker
		}
		replacementTokens := estimateTextTokens(replacement)
		if candidate.tokens <= replacementTokens {
			continue
		}
		replaceToolResult(outbound, clonedMessages, candidate, replacement)
		saved += candidate.tokens - replacementTokens
	}
	return saved
}

func replaceToolResult(outbound *llm.Request, clonedMessages map[int]bool, candidate elisionCandidate, replacement string) {
	if !clonedMessages[candidate.messageIndex] {
		message := outbound.Messages[candidate.messageIndex]
		message.Content = append([]llm.Content(nil), message.Content...)
		outbound.Messages[candidate.messageIndex] = message
		clonedMessages[candidate.messageIndex] = true
	}
	content := outbound.Messages[candidate.messageIndex].Content[candidate.contentIndex]
	content.ToolResult = llm.TextContent(replacement)
	outbound.Messages[candidate.messageIndex].Content[candidate.contentIndex] = content
}

func shortCommand(command string) string {
	command = strings.Join(strings.Fields(command), " ")
	if len(command) <= 160 {
		return command
	}
	return command[:157] + "..."
}

func shortRunCommand(command string) string {
	command = strings.Join(strings.Fields(command), " ")
	if len(command) <= 96 {
		return command
	}
	return command[:93] + "..."
}

func cloneRequestForElision(req *llm.Request) *llm.Request {
	outbound := *req
	outbound.Messages = append([]llm.Message(nil), req.Messages...)
	return &outbound
}

func estimateRequestTokens(req *llm.Request) int {
	total := 0
	for _, system := range req.System {
		total += estimateTextTokens(system.Text)
	}
	for _, message := range req.Messages {
		total += estimateMessageTokens(message)
	}
	for _, tool := range req.Tools {
		if tool == nil {
			continue
		}
		total += estimateTextTokens(tool.Name) + estimateTextTokens(tool.Description) + estimateTextTokens(string(tool.InputSchema))
	}
	return total
}

func estimateMessageTokens(message llm.Message) int {
	return estimateContentsTokens(message.Content)
}

func estimateContentsTokens(contents []llm.Content) int {
	total := 0
	for _, content := range contents {
		total += estimateTextTokens(content.Text)
		total += estimateTextTokens(content.Thinking)
		total += estimateTextTokens(content.ToolName)
		total += estimateTextTokens(string(content.ToolInput))
		total += estimateContentsTokens(content.ToolResult)
	}
	return total
}

func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}
