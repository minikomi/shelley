# Anthropic thinking reliability investigation

Started September 9, 2026. Baseline: `1300b889`.

## Scope and constraints

- Local investigation only. No push, deployment, or production/preview restart.
- **Primary objective:** recreate historically reported HTTP-400 failure cases on actual Anthropic, then measure caching and useful coding-task performance. Mock tests support this; they cannot establish provider compatibility.
- The generic live matrix and its provisional budgets are superseded. Scope each targeted real-provider batch explicitly before running it; none has run in this investigation phase.
- Test code and research notes only; propose production fixes separately.
- Do not replay user conversations or log thinking/signatures.
- Keep thinking-drop diagnostics logs-only. Do not reintroduce banners.
- Parent coordinates integration and commits; workers do not commit.

## Workstreams

| Owner | Scope | Owned artifacts | Status |
| --- | --- | --- | --- |
| Recovery worker | Corrupt signatures, stream interruptions, cancellation, recovery then subsequent request | `../thinking_reliability_recovery_test.go`, `RECOVERY.md` | Offline evidence complete; live untested |
| History worker | Model switches/back, compaction, message/system/tool edits, serialization round trips | `../thinking_reliability_history_test.go`, `HISTORY.md` | Offline evidence complete; live untested |
| Evaluation worker | Matched coding tasks, objective scoring, rollout gates, bounded live-test proposal | `EVALUATION.md` | Focused proposal complete; live unrun |
| Coordinator | Review evidence, integrate tests, track blockers and prepare later write-up | This file | Offline evidence integrated; live case/cap selection next |

Paths in the table are relative to this directory.

## Evidence rules

For every finding record:

1. Scenario and expected safe behavior.
2. Reproduction command and exact test name.
3. Actual observation and practical impact.
4. Evidence class: local code inspection, deterministic mock, previous live observation, or untested hypothesis.
5. Status: passes intended contract, reproduced defect, or untested.
6. Smallest proposed fix and the test needed to validate it.

A passing characterization test that reproduces bad behavior is a **reproduced
defect**, not a reliability pass. Synthetic signatures cannot establish that
Anthropic will accept a request. Do not confuse stable request bytes with
measured cache reuse.

## Decision gates

- Ordinary append-only, same-model history: no unexpected transformations in live validation.
- Intentionally incompatible history: completes with accounted-for drops, without misleading diagnostics.
- Invalid signatures: bounded recovery; no recurring full-history retry tax hidden on subsequent requests.
- Cancellation and incomplete streams: no corrupt partial response persisted as successful history.
- Model switches/compaction: no unexplained failures or accidental deletion of saved thinking.
- Matched tasks: report success and unrecovered errors first, then retries, token usage, and latency.
- No rollout decision from cache-hit percentage alone.

## Checkpoints

1. Recover the evidence behind `bd271d9b` and `3f02a1d3`: distinguish the reported signature failures from the unproven model-rotation explanation. Where original payloads are unavailable, explicitly label reconstructed cases.
2. Run targeted **actual Anthropic** reproductions: historical stripping/recovery, preserve-only diagnostic control, and the new protected adapter. A scenario must demonstrably produce the relevant 400 in its control to count as a reproduction of that failure mechanism. Check the request after apparent recovery.
3. Compare old versus new protected adapters on representative multi-tool coding tasks with executable correctness checks. Measure task success, total time, attempts/retries, cache reads/writes, uncached input, and output—including failures. Do not substitute arithmetic/checksum accuracy for coding-task quality.
4. Keep full-strategy old/new comparisons separate from experiments that hold binding controls constant to isolate retention. Record shared-cache/order limitations.
5. Prepare the later write-up: original evidence, reconstructed failures, real-provider results, useful-task outcomes, measured efficiency, and concrete remaining risks. No rollout conclusion from mock passes or cache-hit percentage alone.

## Activity log

- September 9: baseline clean; investigation planned and three independent workstreams assigned.
- Existing evidence: cache-reuse improvement reproduced on Opus 5; live Fable prefix mismatch/drop and corrupt-signature recovery probes passed. Those are limited scenarios, not a general reliability guarantee.
- Known test-suite caveat: `TestReflectionProbeCachedAndCollapsed` has intermittently failed in the full server suite and passed in isolation. Track separately from new failures.
- September 9 review: all three workers completed offline artifacts; coordinator ran combined recovery/history tests and their race suite successfully. A pass here includes tests deliberately witnessing defects; it is not provider conformance.
- Recovery priorities: the next request can repeat a full strip/retry; signature errors inside HTTP-200 SSE bypass the bounded HTTP-400 recovery path; malformed thinking completion is insufficiently validated. All three are currently mock/code evidence, not newly observed Anthropic incidents.
- Historical request bytes remain unavailable in inspected artifacts; alias rotation remains unproven. Corrected two copied cache/latency numbers in the history note against the primary investigation table.
- Live proposals in worker notes are alternatives, not cumulative authorization. No new live batch or coding benchmark is running. Next coordinator action: choose the smallest historically anchored case manifest, publish its attempt/output cap, and run the real-provider comparison before drawing conclusions.
- Updates: report real-400 reproduction, old/new task results, blockers/decisions, and write-up readiness here. External push updates have been offered but not enabled.
