# Recovery reliability: local findings

September 9, 2026. Baseline `1300b889`. Only mock tests and this note changed;
no production fixes, live requests, UI changes, restarts, commits, pushes, or deployment.

**Priority after user clarification:** reconstruct the historical HTTP-400 failure
mechanisms, then compare cache behavior **and objective coding-task performance**;
this offline suite is supporting evidence, not the objective or a rollout gate by
itself. No generic live batch is recommended or executed here.

Historical evidence: the March 5, 2026 commit `bd271d9b` records an actual reported
"Invalid signature in thinking block" failure; `3f02a1d3` records its recurrence
in `race-check-sse-stream-tests` after older-turn stripping. Their commit messages
attribute the failures to model-alias rotation, but provide no captured failing
exchange establishing that cause. Those are historical incident reports, not an
experiment proving alias rotation. The present synthetic mocks reconstruct the
adapter's error handling; they **do not reproduce the historical incident**.

The most useful next targeted live case, only after separate approval: extend a
real signed-history corruption/recovery probe with the **next ordinary request**,
retaining saved original history. Observe whether rejection/recovery repeats;
record attempt count and sanitized token/cache usage. Pair relevant reconstructed
failure scenarios with matched real coding tasks and executable success checks;
cache reuse alone cannot establish useful performance. Our next-request `[2,0,2,0]`
witness remains **mock-only** until such a probe is run. The previously live Fable
HTTP-400 recovery cited below validates only that isolated targeted mechanism.

**Result: three reproduced defect groups, not a reliability pass.** All new
characterization tests pass precisely because their explicit `DEFECT` cases assert
unsafe baseline behavior. Synthetic signatures establish adapter behavior, not
provider acceptance, real billing, or model quality.

## Evidence and reproduction

All test names below begin `TestThinkingRecoveryReliability`. All execute actual
`Service.Do` with an in-memory `http.RoundTripper` and SSE bodies, a reserved
`.invalid` URL, and zero backoff. No network or sleeps; cancellation is synchronous
through `OnStream` and a context-aware reader. Diagnostics are captured in memory;
failures print counts/booleans, never payloads. Existing `ui/dist` was present;
no UI build or source changes were needed for this narrow package.

```sh
go test ./llm/ant -run '^TestThinkingRecoveryReliability' -count=1 -v
go test -race ./llm/ant -run '^TestThinkingRecoveryReliability' -count=1 -v
go test ./llm/ant -run '^(TestThinkingRecoveryReliability|TestThinkingSignatureRecoveryBoundedAndSafe|TestThinkingBindingProviderDiagnostics|TestParseSSEStreamThinking|TestParseSSEStreamIncomplete|TestParseSSEStreamConnectionReset|TestFromLLMMessageSkipsCorruptThinking)$' -count=1
```

Sanitized results: focused suite **PASS**, package time **0.006s**; race suite
**PASS**, **1.034s**, no race report; selected existing regression tests **PASS**,
**0.003s**. The last anchored pattern selects the listed existing tests (the new
suite is exercised separately by the first two commands). Six new top-level tests,
eight subtests. These commands exclude all opt-in live tests. A subsequent
`go test ./llm/ant -run '^TestThinkingRecoveryReliability' -count=20`
also passed (0.041s).

| Exact suffix / subtest | Observation | Evidence / status |
| --- | --- | --- |
| `ValidBaseline` | One request retains signed and redacted history; complete thinking/signature returned; history unchanged; no diagnostic logs. | Deterministic mock; passes intended contract. |
| `DEFECTSecondRequestRepeatsRecovery` | First successful call sends thinking counts `[2,0]`; append response and a new user message, call `Do` again: total counts `[2,0,2,0]`, two retry callbacks. Request-level thinking remains enabled. | Deterministic mock; **DEFECT: recurring recovery tax**. |
| `DEFECTSSESignatureRetryAndPrivacy/HTTP400_safe_bounded` | Repeated HTTP 400 signature rejection stops after two attempts; second strips historical thinking; error, logs, retry callback omit synthetic sensitive sentinel. No successful response. | Deterministic mock; passes intended contract. |
| `DEFECTSSESignatureRetryAndPrivacy/HTTP200_DEFECT_16_unchanged_attempts_and_raw_diagnostics` | Same signature rejection inside HTTP 200 SSE triggers 16 requests with unchanged thinking, 15 retry callbacks, terminal failure. Synthetic echoed signature appears in logs, returned error, and retry callback. | Deterministic mock; **DEFECT: recovery bypass and diagnostic disclosure**. |
| `IncompleteNeverReturnsSuccess/unsigned_exhausted`, `/signed_exhausted` | Even with `message_delta.stop_reason=end_turn`, absent `message_stop` causes 16 attempts then error/nil response. No successful message to persist. | Deterministic mock; passes intended contract. |
| `IncompleteNeverReturnsSuccess/unsigned_then_complete`, `/signed_then_complete` | Truncated first attempt then complete text response uses two requests; returned `ToMessage` contains only completed text, not partial thinking. Input history unchanged. | Deterministic mock; passes intended contract. |
| `CancelDuringThinking` | Cancel on partial thinking delta: one request, no retry callback, body closed, `errors.Is(err, context.Canceled)`, nil response and unchanged history. | Deterministic mock; passes intended contract. |
| `DEFECTMessageStopAcceptsIncompleteThinking/missing_signature` | Thinking block closes without a signature, then message stops: `Do` returns success; unsigned thinking survives `ToMessage`. Send-time filtering later removes it. | Deterministic mock; **DEFECT: malformed thinking reaches successful history representation**. |
| `DEFECTMessageStopAcceptsIncompleteThinking/missing_block_stop` | Signed thinking never receives `content_block_stop`, but `message_stop` arrives: successful response, thinking survives `ToMessage` and send-time conversion. | Deterministic mock; **DEFECT: missing block completion is not validated**. |

## Root causes, practical impact, smallest follow-up

### 1. Recovery does not repair future replay

`Service.Do` keeps `strippedPayload` local to one call (`ant.go:1284–1286,
1417–1426`). It deliberately does not mutate caller history. A subsequent call
rebuilds from original corrupt signed/redacted history; a successful repaired
request therefore is not durable recovery. The mock appends the result exactly as
a normal caller would, rather than merely repeating the same request object.

Impact: a rejected full-context attempt plus strip-all replay on every later turn
until offending history disappears. Default production backoff adds at least
15 seconds before the first repair retry on each affected turn (inspection;
zero-backoff tests do not measure real latency). Repeated removal can sacrifice
reasoning continuity and cache-prefix reuse; real dollar/token effects are
**unmeasured**, and a rejected request must not be counted as billed generation
without evidence. Stripping all historical thinking also removes valid blocks.

Minimal proposal: return a content-free, explicit recovery marker describing the
historical boundary actually stripped; have the conversation owner persist a
replay-exclusion policy for that boundary, without silently deleting saved original
thinking or stripping newly generated thinking. A local variable or process-only
cache will not fix reloads. Specify reset/compaction semantics before implementation.
Follow-up: assert next `Do` and a serialize/reload round trip send no known-rejected
history on their **first** attempt, preserve newly generated thinking, and succeed
without a second retry. No changes made here.

### 2. HTTP-200 SSE errors bypass private, bounded signature recovery

The SSE parser returns the whole raw error event (`ant.go:1208–1209`). `Do` treats
all parse errors as transient (`1366–1370`), while signature classification and
sanitization exist only in the HTTP-4xx branch (`1406–1426`). Retried error summaries
flow into `slog` and `OnRetry`; accumulated raw errors reach the caller. The test
checks all three channels, using synthetic sentinels only.

Impact: potentially unrecovered turn, up to 16 repeated requests instead of the
HTTP-400 path's two, with avoidable request/rate-limit pressure and sensitive data
exposure if an upstream stream error echoes it. Production default backoff totals
4h 38m 45s across the 15 waits before jitter, if no context/outer deadline ends the
operation sooner (inspection, not a measured outage duration). Interrupted
*generated* responses may additionally consume output work; only final successful
response usage is returned by `Do`, so retries need separate accounting. Neither
live frequency nor billed cost was measured.

Minimal proposal: parse SSE error envelopes into a typed, sanitized provider error;
share signature classification and the one-strip/one-retry state machine across
HTTP and SSE paths. Preserve the safe reason, attempt, provider/model, and request
correlation; never put raw thinking/signature-bearing data in diagnostics. Do not
classify deterministic invalid requests as generic stream transport failures.
Follow-up: turn the SSE defect witness into a two-attempt recovery/terminal contract,
assert stripped second wire request, and assert sentinel absent from every channel.
Also cover malformed SSE JSON and other error statuses: current parser error
construction includes raw data (`1121–1122`), but that additional disclosure surface
was **inspected, not independently reproduced here**.

### 3. `message_stop` alone is insufficient output validation

The parser tracks only a message completion boolean; `content_block_stop` is a
no-op (`1182–1183`). Its terminal filtering removes empty text, not unsigned
thinking (`1254–1260`). Send-time unsigned filtering mitigates provider rejection
but does not prevent malformed thinking entering a successful message. Incomplete
streams *without* `message_stop` are rejected correctly, with or without a signature.
Partial `OnStream` deltas are observable on failed attempts, but are not returned
as a successful response; tests distinguish these two contracts.

Persistence evidence boundary: tests exercise `Response.ToMessage`, not a database.
Inspection of `loop/loop.go:503–552` shows successful ordinary end-turn responses
become history and are passed to `recordMessage`, whereas request errors take the
error branch before this. Thus the adapter supplies malformed content to the
normal persistence path; actual DB persistence/restart behavior is **not tested**.
The cancellation test verifies partial thinking interruption, not every possible
cancellation race after a complete buffered response.

Minimal proposal: validate open/closed content blocks at `message_stop`, and reject
or explicitly exclude unsigned thinking before returning successful content; use a
sanitized error rather than a hidden malformed-success fallback. Preserve valid
empty *signed* thinking and redacted thinking. Decide max-token truncation semantics
explicitly rather than overgeneralizing the `end_turn` fixtures here. Follow-up:
malformed streams must return nil/error (with bounded retries) and no ordinary
successful DB message; complete signed/redacted variants and provider-valid
max-token responses must keep working.

## Existing coverage and limits

Existing tests already cover the first HTTP-400 successful and terminal recovery,
unchanged input history, safe diagnostics (`TestThinkingSignatureRecoveryBoundedAndSafe`,
`TestDoRetriesOnInvalidThinkingSignature`), provider transformation metadata/logs
(`TestThinkingBindingProviderDiagnostics`), thinking delta assembly, unsigned send
filtering, connection reset/truncation, and generic retry exhaustion. This work adds
second-call durability, status-equivalent SSE rejection/privacy, thinking-specific
interruption/cancellation, and malformed-completion witnesses rather than rebuilding
all that coverage. The HTTP-400 subtest is a deliberately narrow control for SSE.

`THINKING_INVESTIGATION.md` records a previous live Fable 5.1 HTTP-400-to-200 corrupt
signature recovery and provider drops on September 9, 2026. That is **previously
live evidence**, not rerun here, and does not validate second-call repair, SSE error
behavior, or malformed output. All new behavioral evidence is synthetic. No model
quality, cache-hit rate, total cost, actual provider malformed-stream incidence,
or rollout-readiness claim follows from these passing characterizations.
