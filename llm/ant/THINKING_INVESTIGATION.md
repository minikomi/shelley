# Thinking history investigation

Investigated September 9, 2026 against `dca552b2`. The original investigation
below describes the old policy. The follow-up implements a controlled change;
it does not establish a response-quality improvement.

## Cache follow-up with the new adapter (September 9, 2026)

Measured against production adapter `cfe8d7dd`. The A/B test changes only
age-based stripping: the old-policy arm also sends the new binding controls,
so this isolates preservation rather than comparing every old transport detail.
Both arms use the same generated signed history. The live fixture uses actual
production request construction, but a direct non-streaming HTTP call rather
than `Service.Do`; the separate binding test above/below covers streaming.

The former trivial lookup elicited no thinking from Opus 5, so that attempt
stopped after one request and is not a cache result. The fixture now requires a
calculated tool-argument checksum and high effort in both arms. The first seed
must still contain real signed thinking. No signatures are fabricated or logged.

Two completed Opus 5 runs used opposite replay orders. A means old stripping;
B means production preservation. Each run uses a unique system prefix, two seed
requests, then four replays. Seed B warms the preserved prefix in both orders,
matching the growing-tool-history scenario being tested. Repeated requests
within each run are deliberately cache-warm and are not independent samples.

| Run/order | Replay | Uncached input | Cache write | Cache read | Output | Latency ms |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| ABBA | Old first | 2 | 15,128 | 0 | 280 | 5,183 |
| ABBA | New first | 2 | 79 | 15,476 | 32 | 2,441 |
| ABBA | New repeat | 2 | 0 | 15,555 | 31 | 3,259 |
| ABBA | Old repeat | 2 | 0 | 15,128 | 268 | 5,339 |
| BAAB | New first | 2 | 79 | 15,328 | 32 | 3,652 |
| BAAB | Old first | 2 | 15,128 | 0 | 274 | 6,619 |
| BAAB | Old repeat | 2 | 0 | 15,128 | 289 | 6,926 |
| BAAB | New repeat | 2 | 0 | 15,407 | 31 | 3,836 |

**Objective:** on the first replay after appending another tool round, preservation
reused the prior cache and needed 99.48% fewer cache-write tokens. Both policies
hit their own cache when replayed unchanged. All eight answers passed the strict
612-only check; no input transformations were reported. This is not a general
quality or latency benchmark, nor a 99.48% reduction in total cost: cached reads,
outputs, and additional retained thinking also matter.

Fable 5.1 refused its checksum seed (`stop_reason=refusal`), so that attempt stopped
after one request. There is no comparable Fable cache result from this follow-up.

Read-only usage metadata from the actual preview corroborates cache reuse despite
repeated model-binding drops. After the first Opus 5 request, 15 subsequent
requests recorded 643,659 cache-read tokens, 9,414 cache-write tokens, and 30
uncached input tokens: 98.55% cache-read share of total input, with individual
requests ranging from 96.40% to 99.81%. The first Opus request wrote 38,276 tokens
and read zero. This is observation under the new adapter, not a paired old/new
comparison. No conversation content was replayed for this analysis.

Reproduce either order (six generation calls per invocation, explicit opt-in):

```sh
ANTHROPIC_THINKING_LIVE=1 \
ANTHROPIC_THINKING_MODEL=anthropic/claude-opus-5 \
ANTHROPIC_THINKING_ORDER=ABBA \
go test ./llm/ant -run '^TestThinkingInvestigationLive$' -count=1 -v
# Repeat with ANTHROPIC_THINKING_ORDER=BAAB for the opposite order.
```

## Follow-up: preserve history and report provider drops

Production request construction now preserves older signed/redacted thinking.
Existing malformed-block filtering and the bounded invalid-signature strip-all
retry remain. Active-thinking requests for recognized Claude models opt into
`thinking-binding-controls-2026-08-01` with
`thinking.block_binding.prefix_mismatch_behavior = "drop_block"`.

Provider input-transformation metadata is logged with its reported reason, block
path, model, response ID, and conversation correlation. Thinking drops do not
create conversation banners. Neither thinking text nor signatures are logged.

A six-request live conformance test through `Service.Do` and the SSE parser
passed on Fable 5.1 on September 9, 2026:

| Probe | Result |
| --- | --- |
| Generate genuine signed thinking | HTTP 200 |
| Replay unchanged history | HTTP 200, no drops |
| Change system prompt; force strict binding | HTTP 400, invalid signature |
| Same changed history; production drop-block mode | HTTP 200, `thinking_dropped`, `prefix_binding_mismatch`, `messages.1.content.0` |
| Corrupted signature; existing recovery | HTTP 400 then HTTP 200, exactly one retry with explained warning |

This establishes one real failure mechanism and targeted recovery, not the cause
of the original March incident. Model switching and broader task-quality testing
remain follow-ups. The first four probes each permit only one network
attempt, so prefix-mismatch success cannot be explained by the old strip-all
retry. Only the fifth probe permits two attempts, explicitly testing that recovery.

```sh
ANTHROPIC_THINKING_BINDING_LIVE=1 \
go test ./llm/ant -run '^TestThinkingBindingLive$' -count=1 -v
```

The older cache experiment now reconstructs the former stripping policy only in
test code, comparing it against production preservation. Its strict formatting
check remains strict; its earlier failures are recorded below.

## Original investigation and decision

Evaluate removing **proactive age-based stripping**, separately from unsigned-block
filtering and signature-error recovery. The cache benefit has a small live
reproduction. Response-quality benefit and safe recovery were not established by
the original cache experiment alone.

## Why the fence exists

- `bd271d9b` (March 5, 2026) records an invalid-thinking-signature failure and
  introduces removal from every assistant message except the last.
- `3f02a1d3` (March 5, 2026 in the author's timezone) records recurrence after
  that fix and introduces strip-all-and-retry recovery.
- Both changes remain in the locally available upstream history; this is not a
  minikomi-only policy.
- The commits attribute failure to model-alias rotation. Neither contains a
  captured failing exchange establishing that cause.
- The first commit also asserts no quality loss and reduced token use. Its tests
  enforce the removal policy; they do not measure quality or billed cost.

Earlier fixes for missing signatures, omitted empty thinking fields, and
incomplete streams are alternative investigation leads, not proven explanations
of this incident. Do not repeat the model-rotation attribution as established fact.

## Objective local findings

- `Service.fromLLMRequest` retains the last assistant **message**, not the whole
  multi-tool turn. It strips signed and redacted thinking from earlier messages
  even when there has been no new ordinary user message.
- When another assistant message is appended, an earlier transmitted message
  changes. The test-only preserve converter keeps that earlier prefix unchanged.
- Conversion does not remove thinking from persisted input history.
- The preserve converter retains existing unsigned-thinking, empty-text, and
  orphaned-server-tool sanitizers. It changes only age-based removal.
- Signature recovery strips all thinking, retaining request-level thinking
  settings. It matches an exact error substring on HTTP 4xx responses. The
  existing mock test does not prove recovery against a real provider rejection.

See `thinking_investigation_test.go`. Byte-prefix stability alone is not a
measurement of provider cache reuse.

## Live experiment

`thinking_live_test.go` is an opt-in, synthetic six-generation-call smoke
experiment, using the documented attached exe.dev LLM gateway. Its Anthropic
model catalog is selected by the `Anthropic-Version` header.

1. Generate a signed assistant tool call for A.
2. Return A=23 and approximately 10k tokens of irrelevant synthetic padding;
   generate a second tool call for B. This warms the preserved prefix.
3. Return B=41; replay the same history stock/preserve/preserve/stock.
4. Record usage, latency, protocol errors, and an exact final-answer check.

Signatures stay in memory. Logs contain usage and capped synthetic final text,
not thinking or signatures. No sleeps, automatic retries, or user history.
This bypasses `Service.Do` transport/recovery, intentionally exposing raw
rejections rather than hiding them behind fallback. It does not validate the
production streaming or recovery path.

Run:

```sh
go test ./llm/ant -run '^TestThinkingInvestigation' -count=1 -v
ANTHROPIC_THINKING_LIVE=1 \
ANTHROPIC_THINKING_MODEL=anthropic/claude-sonnet-4-6 \
go test ./llm/ant -run '^TestThinkingInvestigationLive$' -count=1 -v
```

The live test skips unless explicitly enabled, requires an advertised Claude
model, and limits each call to 90 seconds and 2,048 output tokens.

### Observed results: Sonnet 4.6

The initial attempt stopped after two seeds because the second response had no
thinking block. That is not a protocol error: the experiment only needs genuine
signed thinking in the first response to test its subsequent removal. The
harness was corrected accordingly.

Two subsequent complete experiments reproduced the cache effect. Last run:

| Request | Uncached input | Cache write | Cache read | Latency ms |
| --- | ---: | ---: | ---: | ---: |
| Seed A | 682 | 0 | 0 | 2,536 |
| Seed B | 1 | 10,405 | 0 | 2,556 |
| Stock first replay | 1 | 10,405 | 0 | 2,254 |
| Preserve first replay | 1 | 77 | 10,405 | 1,943 |
| Preserve repeat | 1 | 0 | 10,482 | 3,953 |
| Stock repeat | 1 | 0 | 10,405 | 2,228 |

**Objective:** removing the earlier thinking caused a cache rewrite in this
workload. Preserving it reused the warmed prefix. Both unchanged variants hit
their own caches on repetition. No signature errors occurred in either complete
experiment.

**Quality:** in the final run, all four responses calculated 612 but included
explanatory text, failing the explicit integer-only requirement. The strict live
test therefore fails; do not describe it as passing or silently relax its oracle.
The diagnostic last-number check is not a general quality evaluator.

**Limits:** one task/model, synthetic padding, warm preserved seed, fixed ABBA
order, shared caches, and tiny sample. This is a reproduction of cache churn,
not a fleet cost estimate, latency benchmark, or quality study. Preserved
thinking itself can also increase billed context tokens.

## Provider requirements to test, not assume

Official sources checked September 9, 2026:

- https://platform.claude.com/docs/en/build-with-claude/extended-thinking
- https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- https://platform.claude.com/docs/en/build-with-claude/preserved-thinking
- https://platform.claude.com/docs/en/about-claude/models/extended-thinking-models

The docs distinguish older models that discard thinking across ordinary user
turns from newer models that preserve it. A tool-result exchange is not an
ordinary new user turn.

The preserved-thinking documentation describes prefix-bound signatures for
Fable 5.1: preceding system prompts, tools, and messages must remain stable;
cache-control metadata is excluded. Oldest-first removal is permitted, but
removing thinking in the middle invalidates later retained thinking. Account
enforcement differs; explicitly enable enforcement for an applicable-model test.
That is a current recovery rationale, not evidence of what caused March's error.

## Restoration gates

1. **Protocol/recovery:** real signed and redacted blocks; multiple tool rounds;
   ordinary user turns; interrupted streams; history round-trip persistence;
   compaction; citations/server-tool sanitization; model/provider switches;
   changed system/tools. Test both warm and cold caches. Exercise an actual
   signature rejection, bounded recovery, and the next subsequent request.
2. **Cache/cost:** replay matched histories with independent run prefixes;
   counterbalance ordering; test both growing tool chains and ordinary turns.
   Measure uncached input, cache creation/read, output, failures, and total cost
   using the selected model's current prices. Include the tool/system cache
   prefixes that production sets.
3. **Quality:** paired coding/debugging tasks with executable hidden tests,
   including long tool sequences and corrections. Keep model, effort, tool
   results, and prompts fixed; change only history preservation. Use multiple
   seeds, objective success/constraint scores, retries, and cost-to-success.
   Report uncertainty; an arithmetic smoke test cannot settle this.
4. **Rollout:** after the gates pass, remove only age-based stripping in a
   controlled trial. Keep validated corruption filtering and tested recovery.
   Record removal reason/count and recovery outcomes without logging signatures.
   Roll back on increased unrecovered errors or measured task-success regression.

## Cheap follow-up, independent of restoration

The signature-retry branch does not set `lastErrSummary`; retry notifications can
therefore lack the reason. A focused test and small fix would improve diagnosis.
Do not broaden retry matching blindly or conflate this with changing history
policy.
