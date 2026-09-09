# History / original-400 evidence

September 9, 2026; production-code baseline `1300b889`. Local work only: no
provider calls, push, deployment, restart, or commits by this worker. New files:
`thinking_reliability_history_test.go` and this note. After clarification, stopped
expanding generic coverage and prioritized the original incident history.

## PM summary: knowns, not synthetic-signature claims

1. **The March 5 signature failure and its recurrence are recorded user reports.**
   The two commits do not contain the failing request, actual model-version
   transition, provider request ID, or a controlled reproduction. Alias rotation
   is the commit author's explanation, **not an established root cause**.
2. **Several other replay 400s have concrete client-side mechanisms:** omitted
   empty thinking, unsigned stored thinking, missing/null tool input, null
   `caller`, split server-tool pairs, and empty text. Removing old thinking does
   not fix all of these; existing targeted serialization/stream fixes must stay.
3. **Age stripping definitely changed earlier request content on the next tool
   round.** Existing tests reproduce that historical defect. Previous live
   experiments measured cache-write savings from preservation, not recovery of
   the March incident and not a general coding-performance improvement.
4. New tests find no source mutation through normal conversion/storage reload.
   **Actual loop message bytes are not perfectly prefix-stable:** the cache marker
   moves to the newest tool result. After removing that metadata-only difference,
   old content is unchanged. Do not present complete message-byte equality from
   fixtures with fixed cache flags as production-wide proof.
5. Model switch, compaction, edited old summary, system/tool changes are real
   caller paths that can change binding inputs. Their API acceptance and task/cache
   impact remain **live-untested here**. A reported drop after a real prefix/model
   change need not be a provider bug. No new history-side provider bug is proven.

## Original errors and nearby fixes: evidence ledger

All entries below are **historical recorded reports plus local git/code
inspection**, not newly reproduced HTTP errors. Full commit messages/diffs are
available with `git show <hash> -- llm/ant/ant.go llm/ant/ant_test.go` (omit path
filters to see other changed packages). Dates below use UTC where specified.

| Date / commit | What was actually reported / established | What is NOT established; relevance |
| --- | --- | --- |
| March 5, 2026 20:46 UTC, `bd271d9b` | User prompt records “LLM request failed: Invalid signature in thinking block”. Patch strips signed/redacted blocks from every assistant message except the last; tests enforce that shape. | No captured failing exchange. Alias rotation, “older thinking not needed for context quality,” and token-benefit assertions are not supported by measurements in this commit. Status: historical error; historical prefix-churn DEFECT (see existing `TestThinkingInvestigationPrefixChurn`). |
| March 5, 2026 23:48 UTC, `3f02a1d3` | Prompt: “happened again in race-check-sse-stream-tests even with this new code”. Adds lazy strip-all payload and 4xx substring matcher `Invalid \`signature\``; request history remains intact. | The slug is a conversation reference, not proof the race/SSE test itself generated bad signatures. Recurrence shows the old fence did not eliminate the reported problem; does not prove rotation between adjacent calls. No original request/400 body fixture is added. |
| February 26, `49cea70b` | Stuck `llm-one-shot-tool`; recorded `messages.N.content.0.thinking.thinking: Field required`. Plain string plus `omitempty` omitted `thinking:""`; pointer fixes presence. | Client serialization defect, not evidence of cryptographic invalidity or model rotation. The “API now requires” chronology is an assertion; no provider change is independently established. New `TestThinkingHistoryAppendOnlyStorageRoundTrip` covers signed empty text through actual request encoding and message reload. |
| February 26, `0f22f567` | Same reported stuck conversation also had empty signature. Filters unsigned thinking and resulting empty messages. | Exact origin of missing signature is not demonstrated by this commit. Keeping malformed unsigned blocks is not required for cache continuity. Existing `TestFromLLMMessageSkipsCorruptThinking`. |
| February 27, `22893320`, `113f77a4` | Incomplete SSE could be returned as success. Added `message_stop` completion check, retries, recorded-response and truncation tests. `113f77a4` references issue #131. | A recorded healthy SSE fixture is not the missing March signature-failure exchange. `113f77a4` prose says “stop_reason” check, but its actual diff still checks `messageDone`; do not infer stronger validation from prose. Existing `TestParseSSEStreamIncomplete`, `TestParseSSEStreamRecordedResponse`, `TestParseSSEStreamTruncated`, `TestDoRetriesOnTruncatedStream`. Recovery worker owns present-day stream defects. |
| March 5, `2df6f4a9` | Recorded “unexpected end of JSON input” after retries/context cancellation. Fixes SSE event framing, multi-line data and larger event buffer. | Parser failure is not an Anthropic signature 400. This occurred before both signature commits that UTC day, but temporal proximity is not causation. |
| March 2 `0acdbce1`, reverted March 6 UTC (`March 5 -08:00`) `e031777f` | Form-feed JSON parse error initially blamed on invalid provider JSON; workaround later reverted with explicit statement it was “a different bug,” not Anthropic invalid JSON. | Strong reason not to repeat the original provider-blame attribution as fact. Revert does not identify the different bug or connect it to signatures. |
| February 23 UTC (February 22 -08:00), `c6b8f9e1`, `58810c42`, `4f4434a7` | Reported tool-use `input: Field required` 400. Empty streamed input left nil; parser-only fix did not heal saved history; reload turns JSON null into non-nil `RawMessage("null")`; outbound normalization to `{}` fixes both. | Concrete replay mechanism, separate from thinking age. Existing `TestParseSSEStreamToolUseEmptyInput` and converter tests; API revalidation unnecessary until regression suspected. |
| May 24, `281abb1c` | Recorded repeated `server_tool_use.caller: Input should be an object` 400. JSON storage/reload transformed nil RawMessage to literal null; outbound `nonNullRawMessage` fixes caller/citations. | Important example of real storage-induced wire drift, now fixed. New message roundtrip/citation tests pass; existing `TestFromLLMContentDropsNullRawMessages` directly covers the defect. |
| June 18, `ad645296` | Recorded `patch-tools-code-agents` 400: server web-search use missing matching result. Commit explains split messages after pause/client tool interleaving, masked by warm cache until cold validation. Adds merge-pauses fix, sanitizer, and opt-in API test. | Cache masking is recorded historical diagnosis, not newly measured here. This has a much more specific known bad shape than the March signature report. Existing `TestSanitizeServerToolBlocks`, `TestServerToolBlocksLiveAnthropic`, `TestLoopResolvesPauseTurn`; new sanitizer test characterizes effects on retained thinking. |
| July 2, `b76feba9` | Recorded `team-only-sharing-fork` 400 “text content blocks must be non-empty”. Opened text blocks without deltas persisted and poisoned retries. Parser and outbound filters heal new and old histories. | Client malformed-history defect; not a reason to delete valid old thinking. Existing `TestParseSSEStreamDropsEmptyTextBlock`, `TestFromLLMMessageSkipsEmptyTextBlocks`. |

No user conversation was opened or replayed. Investigation used the git messages,
patches, checked-in tests and `llm/ant/THINKING_INVESTIGATION.md`; no external issue
fetch was performed. An original captured signature failure was **not found in
these artifacts**; this is not a claim that none exists anywhere.

## Cache and task-performance evidence already available

Source: checked-in `THINKING_INVESTIGATION.md`, **previous live observation**, not
runs performed by this worker:

- Opus 5: two six-call runs, opposite ABBA/BAAB replay orders. First old replay
  wrote 15,128 cache tokens; preserved wrote 79 and read 15,476 / 15,328. Reported
  99.48% fewer cache-write tokens; not 99.48% lower total bill. Latencies for that
  comparison: 5,183 vs 2,441 ms and 6,619 vs 3,652 ms. Tiny warmed task, not fleet
  performance. All eight replies passed exact `612` output check.
- Earlier Sonnet 4.6 run: first old replay wrote 10,405 and read zero; preservation
  wrote 77 and read 10,405. All four final answers computed 612 but violated the
  integer-only contract; **task-format FAIL**, not a successful quality trial.
- Fable 5.1 six-request binding probe: genuine thinking seed; unchanged replay
  succeeds; changed-system strict binding fails HTTP 400; production drop-block
  succeeds with `prefix_binding_mismatch`; corrupt signature yields 400 then
  recovery 200. This is a real demonstrated binding failure mechanism, **not a
  reproduction of the March incident**. Fable cache experiment refused its seed,
  so no Fable paired cache result exists in that follow-up.

No matched coding tests establish whether preservation improves task success.
Use task success/unrecovered errors first, then all-attempt token/latency costs.

## Actual callers (not imagined edits)

- Storage: `db/db.go:marshalMessageJSON` marshals each `llm.Message`;
  `server/server.go:convertToLLMMessage` unmarshals it. New tests exercise that
  serialization boundary without claiming full DB integration.
- `/model` -> `ConversationManager.ApplyModelSettings` persists model/reasoning,
  cancels/resets loop, then `ensureLoopLocked` reloads DB context. Model-change
  markers are excluded from context and explicitly skipped by `partitionMessages`.
  `Hydrate` only generates a system prompt if absent: switching alone does **not**
  necessarily regenerate it. `ensureLoopLocked` rebuilds tools; `NewToolSet`
  changes server-side tools/image capabilities by model and applies overrides.
  Subagent tool schema embeds available model IDs, so catalog changes can also
  change tools on rebuild. Adapter A→B→A byte identity assumes those inputs stayed
  fixed; it is not an end-to-end server guarantee.
- Compaction: `handleDistillNewGeneration` increments generation, resets/hydrates
  (new generation gets a system prompt), then `performPiDistillation` chooses
  `findPiCutPoint` and prepends user-role summary to a verbatim recent tail. Cut
  never starts at a pure tool-result message, but may start at an assistant with
  thinking. Old generation stays saved; active prefix changes deliberately.
  Summarization separately uses thinking off and a plain-text transcript
  (`serializePiConversation` includes thinking text but not signed block objects).
- Ordinary saved messages form an immutable log; no general earlier-message edit
  endpoint was located. Actual earlier-context edit is an older distilled summary's
  editable temp file, read by `resolveDistilledContent` on reload and substituted
  by `applyDistillationContentOverride`. Current compact summaries are explicitly
  noneditable. The test models this substitution, not an invented raw DB edit API.
- `Loop.processLLMRequest` copies then caches last tool and last user content;
  previous cache marker disappears when another result is appended. Actual loop
  test proves only cache metadata differs in its earlier messages. Prior binding
  notes say cache-control is excluded; no cache hit can be inferred from bytes.
- `Service.Do` prepares citations before conversion. Native opaque citations stay;
  foreign URL citations become source-reference text, deterministically, without
  changing saved source. This can legitimately alter a prior cross-provider prefix.
  `sanitizeServerToolBlocks` strips broken split pairs, preserving other thinking.
  Normal `resolvePausedTurn` merges server continuations instead of splitting them.

## New deterministic evidence matrix

All seven tests use synthetic signatures. Evidence class: **deterministic mock**
(actual `Service.Do` + offline HTTP transport); the cache test also uses the actual
loop. No test establishes signature validity, provider acceptance, cache hits or
quality. All pass; none is to be advertised as a new provider-reliability result.

| Exact test | Observed structure / impact | Classification / smallest action |
| --- | --- | --- |
| `TestThinkingHistoryAppendOnlyStorageRoundTrip` | Repeated serialization and per-message JSON reload preserve wire bytes, signed-empty thinking field, signed/redacted order; fixed-cache append-only rounds preserve earlier messages; source unchanged. | Intended contract PASS. Keep serializer fix; no broad rewrite. |
| `TestThinkingHistoryActualLoopCacheMarkerMoves` | Two actual loop tool rounds move cache metadata from prior to latest result; all other earlier content identical; source/saved output not mutated. | Intended behavior PASS, caveat to byte-prefix claims. No fix justified. |
| `TestThinkingHistoryModelSwitchAndSettings` | A→B changes model but not history; A→B→A without appended output restores request; appended B blocks remain on return; low/high/off and non-Claude compatible endpoint retain historical blocks. Off disables request thinking/beta; non-Claude gets no binding control/beta. | Structural contract PASS; cross-model/off-history API acceptance UNTESTED. “Off” does not mean erase historical reasoning. Do not expand binding beta to unsupported endpoints. |
| `TestThinkingHistoryChangedPrefixPreservesSource` | System/schema/old-summary edit and compact-shaped tail change request; retained signed/redacted tail remains exact; drop-block setting present, source unchanged. | Legitimate prefix change; acceptance/cache/task effect UNTESTED. Don't blanket-delete saved thinking or call provider drop a defect. |
| `TestThinkingHistoryCitationPreparationStable` | Native citation objects remain; foreign URLs become text on request only, stable after reload; signed/redacted blocks retained. | Contract PASS; whether changed prefix invalidates later signatures requires genuine provider test. |
| `TestThinkingHistoryServerToolSanitizerPrefixChange` | Final orphan server use initially retained; becomes stripped when no longer final, split result stripped; later signed thinking survives; same-message pair retained, saved history unchanged. | Legitimate malformed-history repair (historical upstream DEFECT already fixed). Later binding effect UNTESTED, not contract failure. Keep sanitizer and pause merger. |
| `TestThinkingHistoryDiagnosticsExcludedAfterStorage` | `Response.ToMessage` -> message JSON -> reload -> transmitted request excludes transformation metadata; original response keeps diagnostics. | Intended logs-only/history-isolation contract PASS. |

The historical age-stripping **DEFECT** remains witnessed by existing
`TestThinkingInvestigationPrefixChurn` / `TestThinkingInvestigationMultipleToolRounds`:
passing characterization tests demonstrate bad **old-policy** behavior, not a
reliability pass for that policy. Recovery worker separately records current
retry/stream DEFECT witnesses; this workstream does not duplicate or relabel them.

## Verification performed

`ui/dist` was present; no UI changes/build/deploy were needed. All below passed;
only explicit offline names selected, no live-test opt-in:

```sh
go test ./llm/ant -run '^TestThinkingHistory' -count=1

go test -race ./llm/ant ./loop -run '^(TestThinkingHistory|TestThinkingInvestigation(PrefixChurn|MultipleToolRounds|ImmutabilityAndSanitizers)|TestThinkingBinding(RequestGating|StreamMetadata|ProviderDiagnostics)|TestInputTransformationsDoNotCreateWarnings|TestThinkingDropLogsAcrossToolRoundsAndUserTurns)' -count=1

go test ./server -run '^(TestFindPiCutPointNeverLandsOnToolResult|TestPiDistillCopiesRecentMessagesIntoNewGeneration|TestPiDistillForcesSummaryWhenOverBudget|TestPiReDistillPreservesPriorSummary|TestModelCommandSwitch|TestModelCommandReasoningOnly)$' -count=1

go test -race ./llm/ant -run '^(TestFromLLMMessageSkipsCorruptThinking|TestFromLLMMessageSkipsEmptyTextBlocks|TestFromLLMRequestSkipsEmptyMessages|TestParseSSEStreamDropsEmptyTextBlock|TestParseSSEStreamToolUseEmptyInput|TestParseSSEStreamThinking|TestParseSSEStreamIncomplete|TestParseSSEStreamTruncated|TestParseSSEStreamTruncatedMidContentBlock|TestParseSSEStreamRecordedResponse|TestFromLLMContentDropsNullRawMessages|TestSanitizeServerToolBlocks|TestSanitizeServerToolBlocksDoesNotMutateInput)$' -count=1
```

New tests add no sleeps. Existing server tests substantiate real caller behavior,
not genuine Anthropic-signed history through DB compaction.

## Smallest proposed live work (NOT approved / NOT run)

Do not execute a generic twenty-request matrix. Start with one selected model and
one deterministic coding/tool task, clean isolated state per replay, objective
patch/unit-test oracle, generated non-user history, real signed seed output only.

**Core proposal: at most seven requests, staged approval.** Reuse the existing
cache/binding harness design rather than create a broad new framework:

1. Two same-model signed tool rounds seed/warm a small coding task. Stop if no
   signed thinking, refusal, or seed task/tool failure. This anchors the exact
   age-stripping prefix change; it does not simulate alias rotation.
2. Two matched continuations from the identical seeded state: reconstructed old
   age policy versus preserved production policy. Capture all input/cache-write/
   cache-read/output tokens, retries, elapsed time, strict patch correctness and
   actual unit-test result. One pair is a smoke comparison, not significance.
3. Only if earlier stages work, use the preserved seed and **one minimal system
   edit**, anchored to the already-observed Fable strict-binding 400: one strict
   call (no recovery) and one production drop-block call. Score the same task, not
   only HTTP status. This is a known mechanism check, not “the March repro.”
4. One append-only request after that production success to measure whether
   transformations/400/retry costs recur and whether the next task step succeeds.

Per request: cap 90 seconds and 2,048 output tokens; aggregate at most seven
network attempts / 14,336 output tokens / 630 seconds, plus a pre-approved input
and dollar cap. Strict/control legs allow one attempt each. If automatic retry
would exceed the total cap, stop; this proposal does not silently buy recovery
calls. No signature/body logging; only safe counters/reason/path/status and task
scores. A second reversed replay order is a separately approved follow-up, not
hidden in this budget. If task needs more output, redesign before approval.

**Optional, separately approved targeted branch**, only if PM prioritizes the
other historically captured 400: reproduce `ad645296`'s split server-pair shape
from fresh real server-tool output and compare raw rejection versus existing
sanitizer, including cache/task effects. Existing opt-in test is the starting
point. This is stronger incident anchoring than speculative A→B→A or arbitrary
compaction. Do not spend live calls on fake encrypted/signature strings.

Model-switch, compaction, thinking-off, and arbitrary prefix edits remain later
hypotheses unless actual failure evidence prioritizes one. Cheap fixes now are
preserving targeted serialization/stream safeguards and fixing concrete recovery
worker defects, not removing all historical thinking or inventing model alias
provenance from synthetic data.
