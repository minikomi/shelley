package ant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// Opt-in six-request conformance probe through the production streaming path.
// Synthetic history only; signatures remain in memory. Each probe has a hard
// network-attempt limit; only the corrupt-signature probe allows recovery.
func TestThinkingBindingLive(t *testing.T) {
	if os.Getenv("ANTHROPIC_THINKING_BINDING_LIVE") != "1" {
		t.Skip("set ANTHROPIC_THINKING_BINDING_LIVE=1 to run six synthetic requests")
	}
	const model = "anthropic/claude-fable-5-1"
	type outcome struct {
		response *llm.Response
		status   int
		invalid  bool
		err      error
		statuses []int
		retries  int
	}
	probe := func(label string, r *llm.Request, strict bool, maxAttempts int) outcome {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		attempts := 0
		var result outcome
		s := &Service{
			URL: "https://llm.int.exe.xyz/v1/messages", APIKey: "implicit",
			Model: model, MaxTokens: 2048, ThinkingLevel: llm.ThinkingLevelLow,
			Backoff: []time.Duration{0},
		}
		s.HTTPC = &http.Client{Transport: &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts > maxAttempts {
				cancel()
				return nil, fmt.Errorf("probe exceeded its network attempt limit")
			}
			if !strings.Contains(req.Header.Get("Anthropic-Beta"), "thinking-binding-controls-2026-08-01") {
				cancel()
				return nil, fmt.Errorf("production request omitted binding beta")
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, fmt.Errorf("cannot read synthetic request")
			}
			var wire map[string]any
			if err := json.Unmarshal(body, &wire); err != nil {
				return nil, fmt.Errorf("invalid synthetic request JSON")
			}
			thinking, _ := wire["thinking"].(map[string]any)
			binding, _ := thinking["block_binding"].(map[string]any)
			if binding["prefix_mismatch_behavior"] != "drop_block" {
				cancel()
				return nil, fmt.Errorf("production request omitted drop-block behavior")
			}
			if strict {
				binding["prefix_mismatch_behavior"] = "error"
				body, err = json.Marshal(wire)
				if err != nil {
					return nil, fmt.Errorf("cannot encode strict probe")
				}
			}
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			resp, err := http.DefaultTransport.RoundTrip(req)
			if err != nil {
				return nil, err
			}
			result.status = resp.StatusCode
			result.statuses = append(result.statuses, resp.StatusCode)
			if resp.StatusCode != http.StatusOK {
				errorBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				result.invalid = bytes.Contains(errorBody, []byte("Invalid `signature`"))
				resp.Body = io.NopCloser(bytes.NewReader(errorBody))
				if attempts >= maxAttempts {
					cancel() // Expose rejection unless this probe explicitly tests recovery.
				}
				if readErr != nil {
					return nil, fmt.Errorf("cannot read provider rejection")
				}
			}
			return resp, nil
		}}}
		start := time.Now()
		requestCopy := *r
		requestCopy.OnRetry = func(event llm.RetryEvent) {
			result.retries++
			if !strings.Contains(event.Err, "without historical thinking") {
				t.Error("signature recovery warning omitted the thinking removal reason")
			}
		}
		result.response, result.err = s.Do(ctx, &requestCopy)
		t.Logf("%s status=%d latency_ms=%d invalid_signature=%t success=%t",
			label, result.status, time.Since(start).Milliseconds(), result.invalid, result.err == nil)
		t.Logf("%s statuses=%v retries=%d", label, result.statuses, result.retries)
		if result.response != nil {
			for _, drop := range result.response.InputTransformations {
				t.Logf("%s transformation type=%s path=%s reason=%s", label, drop.Type, drop.Path, drop.Reason)
			}
		}
		return result
	}
	seed := &llm.Request{
		System:   []llm.SystemContent{{Text: fmt.Sprintf("Synthetic binding probe %d. Solve the requested arithmetic.", time.Now().UnixNano())}},
		Messages: []llm.Message{llm.UserStringMessage("Compute 982451653 multiplied by 961748941. Return only the integer.")},
	}
	first := probe("seed", seed, false, 1)
	if first.err != nil || first.response == nil {
		t.Fatal("seed failed (provider body withheld)")
	}
	hasSignedThinking := false
	for _, c := range first.response.Content {
		hasSignedThinking = hasSignedThinking || c.Type == llm.ContentTypeThinking && c.Signature != ""
	}
	if !hasSignedThinking {
		t.Fatal("seed must produce genuine signed thinking")
	}
	replay := &llm.Request{
		System: seed.System,
		Messages: append(append([]llm.Message(nil), seed.Messages...),
			first.response.ToMessage(), llm.UserStringMessage("Reply with exactly: ok")),
	}
	exact := probe("unchanged-history", replay, false, 1)
	if exact.err != nil || exact.response == nil {
		t.Fatal("unchanged history failed (provider body withheld)")
	}
	if len(exact.response.InputTransformations) != 0 {
		t.Fatal("unchanged history unexpectedly transformed input")
	}
	changed := *replay
	changed.System = []llm.SystemContent{{Text: seed.System[0].Text + " This sentence deliberately changes the signed prefix."}}
	strict := probe("changed-history-strict", &changed, true, 1)
	if strict.err == nil || !strict.invalid || strict.status != http.StatusBadRequest {
		t.Fatal("strict changed-prefix probe must reject a thinking signature")
	}
	dropped := probe("changed-history-drop", &changed, false, 1)
	if dropped.err != nil || dropped.response == nil {
		t.Fatal("drop-block recovery failed (provider body withheld)")
	}
	found := false
	for _, transformation := range dropped.response.InputTransformations {
		found = found || transformation.Type == "thinking_dropped" && transformation.Reason == "prefix_binding_mismatch"
	}
	if !found {
		t.Fatal("changed history must report a thinking drop with prefix_binding_mismatch")
	}
	corrupt := *replay
	corrupt.Messages = append([]llm.Message(nil), replay.Messages...)
	corrupt.Messages[1].Content = append([]llm.Content(nil), replay.Messages[1].Content...)
	for i := range corrupt.Messages[1].Content {
		if corrupt.Messages[1].Content[i].Type == llm.ContentTypeThinking {
			corrupt.Messages[1].Content[i].Signature = "deliberately-corrupted-signature"
		}
	}
	recovered := probe("corrupt-signature-recovery", &corrupt, false, 2)
	if recovered.err != nil || recovered.response == nil || !recovered.invalid || recovered.retries != 1 ||
		len(recovered.statuses) != 2 || recovered.statuses[0] != http.StatusBadRequest || recovered.statuses[1] != http.StatusOK {
		t.Fatal("corrupt signature must recover with exactly one explained strip-all retry")
	}

}
