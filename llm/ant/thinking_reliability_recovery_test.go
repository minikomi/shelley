package ant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// These are synthetic transport characterizations, not provider conformance tests.
// DEFECT tests deliberately witness unsafe baseline behavior; passing is not a safety pass.
func recoveryReliabilityService(fn func(*http.Request) (*http.Response, error)) *Service {
	return &Service{Model: Claude46Opus, ThinkingLevel: llm.ThinkingLevelMedium,
		URL: "https://recovery.invalid/messages", Backoff: []time.Duration{0},
		HTTPC: &http.Client{Transport: &roundTripFunc{fn: fn}}}
}

func recoveryReliabilityResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func recoveryReliabilityLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &logs
}

func recoveryReliabilityWire(t *testing.T, req *http.Request) request {
	t.Helper()
	var wire request
	if err := json.NewDecoder(req.Body).Decode(&wire); err != nil {
		t.Fatal("cannot decode synthetic request")
	}
	return wire
}

func recoveryReliabilityThinkingCount(wire request) int {
	n := 0
	for _, m := range wire.Messages {
		for _, c := range m.Content {
			if c.Type == "thinking" || c.Type == "redacted_thinking" {
				n++
			}
		}
	}
	return n
}

func recoveryReliabilityStream(signature, blockStop, messageStop bool) string {
	s := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"recovery-message\",\"role\":\"assistant\",\"model\":\"synthetic-model\"}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"synthetic-private-thought\"}}\n\n"
	if signature {
		s += "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"synthetic-private-signature\"}}\n\n"
	}
	if blockStop {
		s += "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	}
	// A stop reason alone must not turn an interrupted generation into success.
	s += "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"
	if messageStop {
		s += "data: {\"type\":\"message_stop\"}\n\n"
	}
	return s
}

func TestThinkingRecoveryReliabilityValidBaseline(t *testing.T) {
	logs := recoveryReliabilityLogs(t)
	calls := 0
	s := recoveryReliabilityService(func(req *http.Request) (*http.Response, error) {
		calls++
		wire := recoveryReliabilityWire(t, req)
		if recoveryReliabilityThinkingCount(wire) != 2 || !wire.Stream {
			t.Fatal("valid signed/redacted history not preserved on initial streaming request")
		}
		return recoveryReliabilityResponse(200, recoveryReliabilityStream(true, true, true)), nil
	})
	r := &llm.Request{Messages: thinkingRound("valid")}
	before := thinkingJSON(t, r.Messages)
	out, err := s.Do(context.Background(), r)
	if err != nil || out == nil || calls != 1 {
		t.Fatalf("baseline failed: calls=%d error=%t", calls, err != nil)
	}
	if len(out.Content) != 1 || out.Content[0].Signature == "" || out.Content[0].Thinking == "" {
		t.Fatal("completed thinking/signature not returned")
	}
	if !bytes.Equal(before, thinkingJSON(t, r.Messages)) || logs.Len() != 0 {
		t.Fatal("baseline changed history or emitted unexpected diagnostics")
	}
}

func TestThinkingRecoveryReliabilityDEFECTSecondRequestRepeatsRecovery(t *testing.T) {
	recoveryReliabilityLogs(t)
	var counts []int
	var retries []llm.RetryEvent
	s := recoveryReliabilityService(func(req *http.Request) (*http.Response, error) {
		wire := recoveryReliabilityWire(t, req)
		n := recoveryReliabilityThinkingCount(wire)
		counts = append(counts, n)
		if wire.Thinking == nil {
			t.Fatal("recovery disabled request-level thinking")
		}
		if n > 0 {
			return recoveryReliabilityResponse(400, "Invalid `signature` synthetic-private-signature"), nil
		}
		return recoveryReliabilityResponse(200, mockSSEResponse("recovered", Claude46Opus, "answer", 1, 1)), nil
	})
	r := &llm.Request{Messages: thinkingRound("corrupt"), OnRetry: func(e llm.RetryEvent) { retries = append(retries, e) }}
	before := thinkingJSON(t, r.Messages)
	out, err := s.Do(context.Background(), r)
	if err != nil || out == nil {
		t.Fatal("first recovery failed")
	}
	if !bytes.Equal(before, thinkingJSON(t, r.Messages)) {
		t.Fatal("baseline unexpectedly repaired input history")
	}
	// Model the caller's normal append-only persisted history, then issue a NEW Do.
	r.Messages = append(r.Messages, out.ToMessage(), llm.UserStringMessage("next"))
	out, err = s.Do(context.Background(), r)
	if err != nil || out == nil || !reflect.DeepEqual(counts, []int{2, 0, 2, 0}) || len(retries) != 2 {
		t.Fatalf("DEFECT witness changed: counts=%v retries=%d error=%t", counts, len(retries), err != nil)
	}
	// DEFECT: both successful turns pay a failed full-history request and strip-all retry.
}

func TestThinkingRecoveryReliabilityDEFECTSSESignatureRetryAndPrivacy(t *testing.T) {
	for _, status := range []int{400, 200} {
		name := "HTTP400_safe_bounded"
		if status == 200 {
			name = "HTTP200_DEFECT_16_unchanged_attempts_and_raw_diagnostics"
		}
		t.Run(name, func(t *testing.T) {
			logs := recoveryReliabilityLogs(t)
			const sentinel = "synthetic-private-signature"
			body := "Invalid `signature` " + sentinel
			if status == 200 {
				body = "data: " + string(thinkingJSON(t, map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": body}})) + "\n\n"
			}
			var counts []int
			var retries []llm.RetryEvent
			s := recoveryReliabilityService(func(req *http.Request) (*http.Response, error) {
				counts = append(counts, recoveryReliabilityThinkingCount(recoveryReliabilityWire(t, req)))
				return recoveryReliabilityResponse(status, body), nil
			})
			out, err := s.Do(context.Background(), &llm.Request{Messages: thinkingRound("corrupt"), OnRetry: func(e llm.RetryEvent) { retries = append(retries, e) }})
			if out != nil || err == nil {
				t.Fatal("repeated rejection must not return a successful response")
			}
			wantCalls := 2
			if status == 200 {
				wantCalls = 16
			}
			if len(counts) != wantCalls || len(retries) != wantCalls-1 {
				t.Fatalf("retry bound changed: calls=%d retries=%d", len(counts), len(retries))
			}
			for i, n := range counts {
				want := 2
				if status == 400 && i > 0 {
					want = 0
				}
				if n != want {
					t.Fatalf("call %d thinking count=%d want=%d", i+1, n, want)
				}
			}
			for channel, diagnostic := range map[string]string{"logs": logs.String(), "error": err.Error(), "retry": retries[0].Err} {
				if strings.Contains(diagnostic, sentinel) != (status == 200) {
					t.Fatalf("%s diagnostic privacy characterization changed", channel)
				}
			}
		})
	}
}

func TestThinkingRecoveryReliabilityIncompleteNeverReturnsSuccess(t *testing.T) {
	for _, signed := range []bool{false, true} {
		for _, recover := range []bool{false, true} {
			name := "unsigned"
			if signed {
				name = "signed"
			}
			if recover {
				name += "_then_complete"
			} else {
				name += "_exhausted"
			}
			t.Run(name, func(t *testing.T) {
				logs := recoveryReliabilityLogs(t)
				calls, deltas := 0, 0
				s := recoveryReliabilityService(func(*http.Request) (*http.Response, error) {
					calls++
					if recover && calls == 2 {
						return recoveryReliabilityResponse(200, mockSSEResponse("complete", Claude46Opus, "finished", 1, 1)), nil
					}
					return recoveryReliabilityResponse(200, recoveryReliabilityStream(signed, signed, false)), nil
				})
				r := &llm.Request{Messages: []llm.Message{llm.UserStringMessage("synthetic")}, OnStream: func(d llm.StreamDelta) {
					if d.Type == "thinking" {
						deltas++
					}
				}}
				before := thinkingJSON(t, r.Messages)
				out, err := s.Do(context.Background(), r)
				if recover {
					if err != nil || out == nil || calls != 2 || deltas != 1 || len(out.ToMessage().Content) != 1 || out.Content[0].Text != "finished" {
						t.Fatal("partial thinking escaped into recovered successful response")
					}
				} else if err == nil || out != nil || calls != 16 || deltas != 16 {
					t.Fatal("unfinished stream returned success or changed retry bound")
				}
				if !bytes.Equal(before, thinkingJSON(t, r.Messages)) || strings.Contains(logs.String(), "synthetic-private") {
					t.Fatal("incomplete stream changed history or leaked private diagnostics")
				}
			})
		}
	}
}

// Return the context error only after the parser consumed a partial thinking delta.
// OnStream cancels synchronously, so this needs neither sleeps nor a goroutine.
type recoveryReliabilityCancelBody struct {
	prefix *strings.Reader
	ctx    context.Context
	closed bool
}

func (b *recoveryReliabilityCancelBody) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *recoveryReliabilityCancelBody) Close() error {
	b.closed = true
	return nil
}

func TestThinkingRecoveryReliabilityCancelDuringThinking(t *testing.T) {
	recoveryReliabilityLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &recoveryReliabilityCancelBody{prefix: strings.NewReader(recoveryReliabilityStream(false, false, false)), ctx: ctx}
	calls, deltas, retries := 0, 0, 0
	s := recoveryReliabilityService(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: body}, nil
	})
	r := &llm.Request{Messages: []llm.Message{llm.UserStringMessage("synthetic")},
		OnStream: func(d llm.StreamDelta) { deltas++; cancel() },
		OnRetry:  func(llm.RetryEvent) { retries++ }}
	before := thinkingJSON(t, r.Messages)
	out, err := s.Do(ctx, r)
	if out != nil || !errors.Is(err, context.Canceled) || calls != 1 || deltas != 1 || retries != 0 || !body.closed {
		t.Fatalf("cancel contract failed: calls=%d deltas=%d retries=%d closed=%t", calls, deltas, retries, body.closed)
	}
	if !bytes.Equal(before, thinkingJSON(t, r.Messages)) {
		t.Fatal("cancelled response changed input history")
	}
}

func TestThinkingRecoveryReliabilityDEFECTMessageStopAcceptsIncompleteThinking(t *testing.T) {
	for _, signed := range []bool{false, true} {
		name := "missing_signature"
		if signed {
			name = "missing_block_stop"
		}
		t.Run(name, func(t *testing.T) {
			recoveryReliabilityLogs(t)
			calls := 0
			s := recoveryReliabilityService(func(*http.Request) (*http.Response, error) {
				calls++
				return recoveryReliabilityResponse(200, recoveryReliabilityStream(signed, !signed, true)), nil
			})
			out, err := s.Do(context.Background(), &llm.Request{Messages: []llm.Message{llm.UserStringMessage("synthetic")}})
			if err != nil || out == nil || calls != 1 || len(out.ToMessage().Content) != 1 {
				t.Fatal("DEFECT witness changed: malformed completed stream no longer succeeds")
			}
			stored := out.ToMessage()
			if stored.Content[0].Type != llm.ContentTypeThinking || (stored.Content[0].Signature != "") != signed {
				t.Fatal("DEFECT witness changed: malformed thinking no longer persists through ToMessage")
			}
			// Send-time unsigned filtering mitigates replay rejection but does not repair storage.
			want := 0
			if signed {
				want = 1
			}
			if len(fromLLMMessage(stored).Content) != want {
				t.Fatal("send-time mitigation changed")
			}
		})
	}
}
