package ant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// Opt-in, synthetic smoke experiment, not a quality benchmark. At most six
// generation calls, no retries/sleeps, 90 seconds per call. Genuine signatures
// stay in memory. Only capped final synthetic text is logged, never thinking,
// signatures, full responses, or error bodies.
// Seed two warms the preserved prefix intentionally. ABBA replays expose cold
// versus repeated requests, but shared caches/order still confound comparisons.
func TestThinkingInvestigationLive(t *testing.T) {
	if os.Getenv("ANTHROPIC_THINKING_LIVE") != "1" {
		t.Skip("set ANTHROPIC_THINKING_LIVE=1 and ANTHROPIC_THINKING_MODEL to opt in")
	}
	model := os.Getenv("ANTHROPIC_THINKING_MODEL")
	if !strings.HasPrefix(model, "anthropic/claude-") {
		t.Fatal("ANTHROPIC_THINKING_MODEL must be an advertised anthropic/claude- model")
	}
	const gateway = "https://llm.int.exe.xyz/v1/"
	client := &http.Client{Timeout: 90 * time.Second}
	// The Anthropic-Version header selects the Anthropic model catalog.
	modelBody, err := thinkingHTTP(client, http.MethodGet, gateway+"models", nil)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Data []struct{ ID string }
	}
	if err := json.Unmarshal(modelBody, &catalog); err != nil {
		t.Fatal("invalid model catalog JSON")
	}
	found := false
	for _, m := range catalog.Data {
		found = found || m.ID == model
	}
	if !found {
		t.Fatal("selected model is not advertised by the gateway")
	}
	s := &Service{Model: model, MaxTokens: 2048, ThinkingLevel: llm.ThinkingLevelLow}
	r := &llm.Request{
		System: []llm.SystemContent{{Text: fmt.Sprintf("Synthetic test run %d. Follow the lookup protocol exactly.", time.Now().UnixNano())}},
		Tools: []*llm.Tool{{
			Name: "lookup", Description: "Return the value for key A or B.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","enum":["A","B"]}},"required":["key"],"additionalProperties":false}`),
		}},
		Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: llm.TextContent(
			"Call lookup for A only. After its result, call lookup for B only. Do not combine calls. Ignore padding fields. Once both results arrive, calculate (A*7+B*11) modulo 997 and reply with only the decimal integer, no tools or explanation.")}},
	}
	call := func(label string, wire *request) (*response, error) {
		start := time.Now()
		body, err := thinkingHTTP(client, http.MethodPost, gateway+"messages", thinkingJSON(t, wire))
		if err != nil {
			t.Logf("%s latency_ms=%d error=%v", label, time.Since(start).Milliseconds(), err)
			return nil, err
		}
		var out response
		if err := json.Unmarshal(body, &out); err != nil || out.Type != "message" {
			return nil, fmt.Errorf("%s: invalid message response", label)
		}
		t.Logf("%s model=%s latency_ms=%d input=%d cache_write=%d cache_read=%d output=%d stop=%s",
			label, out.Model, time.Since(start).Milliseconds(), out.Usage.InputTokens,
			out.Usage.CacheCreationInputTokens, out.Usage.CacheReadInputTokens, out.Usage.OutputTokens, out.StopReason)
		return &out, nil
	}
	for i, key := range []string{"A", "B"} {
		out, err := call("seed-"+key, preserveThinkingRequest(s, r))
		if err != nil {
			t.Fatal(err)
		}
		var toolID string
		signed, calls := 0, 0
		for _, c := range out.Content {
			if c.Type == "thinking" && c.Signature != "" {
				signed++
			}
			if c.Type == "tool_use" {
				calls++
				var args struct{ Key string }
				if err := json.Unmarshal(c.ToolInput, &args); err != nil || args.Key != key || c.ToolName != "lookup" {
					t.Fatal("seed did not follow the lookup protocol")
				}
				toolID = c.ID
			}
		}
		t.Logf("seed-%s signed_blocks=%d tool_calls=%d tool_id_present=%t stop=%s",
			key, signed, calls, toolID != "", out.StopReason)
		if i == 0 && signed == 0 {
			t.Fatal("seed-A needs genuine signed thinking to measure its later removal")
		}
		if calls != 1 || toolID == "" || out.StopReason != "tool_use" {
			t.Fatalf("seed-%s: expected exactly one completed tool call with an ID", key)
		}
		result := fmt.Sprintf("%s=%d", key, []int{23, 41}[i])
		if i == 0 {
			// Large suffix AFTER the first thinking block: removing that block
			// changes the prefix before this cacheable synthetic data.
			var padding strings.Builder
			for n := 0; n < 800; n++ {
				fmt.Fprintf(&padding, "\npadding_%04d=synthetic ignored record %04d", n, n)
			}
			result += padding.String()
		}
		r.Messages = append(r.Messages, toLLMResponse(out).ToMessage(), llm.Message{
			Role: llm.MessageRoleUser, Content: []llm.Content{{
				Type: llm.ContentTypeToolResult, ToolUseID: toolID, ToolResult: llm.TextContent(result), Cache: true,
			}},
		})
	}
	stock, preserved := s.fromLLMRequest(r), preserveThinkingRequest(s, r)
	if bytes.Equal(thinkingJSON(t, stock), thinkingJSON(t, preserved)) {
		t.Fatal("experiment failed to create different histories")
	}
	for i, preserve := range []bool{false, true, true, false} {
		wire, arm := stock, "stock"
		if preserve {
			wire, arm = preserved, "preserve"
		}
		label := fmt.Sprintf("replay-%d-%s", i+1, arm)
		out, err := call(label, wire)
		if err != nil {
			t.Errorf("%s: %v", label, err)
			continue
		}
		var text strings.Builder
		for _, c := range out.Content {
			if c.Type == "text" && c.Text != nil {
				text.WriteString(*c.Text)
			}
		}
		answer := strings.TrimSpace(text.String())
		exactFormat := answer == "612"
		// Diagnostic only: the final numeric token can be right despite extra
		// prose. It is not a semantic grader and never relaxes the strict check.
		numbers := regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`).FindAllString(answer, -1)
		lastNumberCorrect := false
		if len(numbers) > 0 {
			n, err := strconv.ParseFloat(numbers[len(numbers)-1], 64)
			lastNumberCorrect = err == nil && n == 612
		}
		correct := exactFormat && out.StopReason == "end_turn"
		visible := []rune(answer)
		t.Logf("%s correct=%t exact_format=%t last_number_correct=%t visible_text=%q truncated=%t",
			label, correct, exactFormat, lastNumberCorrect, string(visible[:min(len(visible), 512)]), len(visible) > 512)
		if !correct {
			t.Errorf("%s: objective arithmetic/format check failed", label)
		}
	}
}

// Return only sanitized error categories: provider errors can echo signatures.
func thinkingHTTP(client *http.Client, method, url string, payload []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("request construction failed")
	}
	req.Header.Set("X-API-Key", "implicit")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("transport failure (deadline=%t)", ctx.Err() != nil)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("response read failed")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d (invalid_signature=%t)", resp.StatusCode,
			bytes.Contains(body, []byte("Invalid `signature`")))
	}
	return body, nil
}
