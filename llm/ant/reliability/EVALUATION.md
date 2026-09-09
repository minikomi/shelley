# Historical thinking failures: focused evaluation plan

September 9, 2026; investigation baseline `1300b889`. **Plan only; no new live evidence.**
Only this note is changed. No live calls, production changes, restart, commit, or push.
The previous 31/113-request proposals are **superseded, unapproved, and not additive**.

## Phase A — recover the motivating evidence first (offline)

- Inspect `bd271d9b` (March 5): reported invalid thinking signature; older-assistant stripping added.
- Inspect `3f02a1d3` (March 5): recurrence in `race-check-sse-stream-tests` despite that fence; strip-all recovery added.
- Recover locally available linked incident artifacts, request/model metadata, and relevant tests; record precisely what is missing. Do not replay user conversations or expose thinking/signatures.
- Trace related fixes: `49cea70b` (empty thinking field serialization), `0f22f567` (missing signature), `22893320`/`113f77a4` (incomplete streams), `2df6f4a9` (SSE framing), and `e031777f` (reverted control-character workaround). These are candidate mechanisms, not established explanations of the March signature incident.
- For each incident record: observed error/status, affected model, older versus latest block, preceding event, source artifact, minimal reconstruction, and whether the current adapter still admits that shape.
- **Alias rotation is UNPROVEN.** Commit explanations are not captured causal evidence. Do not infer rotation merely because a model switch or changed prefix fails today.
- Label cases separately: **historically reported**, **historically captured/reproducible**, **reconstructed mechanism**, or **modern live control**. A modern synthetic 400 cannot retrospectively prove the March cause.
- Prioritize two case slots: invalid older history (why age-stripping might help), then invalid latest history (why it was insufficient). If actual incident bytes/conditions cannot be recovered, say so; propose labeled reconstructions rather than claiming historical reproduction.
- Keep unsigned/empty/truncated cases offline when existing sanitizers already prevent transmission. Do not disable those sanitizers just to manufacture a provider 400.

## Existing evidence and reusable pieces

- `THINKING_INVESTIGATION.md`: two Opus 5 high-effort checksum runs, each two seeds/four replays, ABBA/BAAB. All eight answers passed; first preserved replays reused the deliberately warmed prefix. This is cache evidence, **not coding quality or total-cost savings**.
- Fable refused its checksum seed; no comparable Fable cache result. Separate live Fable streaming probes accepted unchanged history, rejected strict changed-prefix history, reported production drops, and recovered corrupted signatures. They did not test the next request after recovery.
- Reuse `thinking_binding_live_test.go`'s `Service.Do` streaming/attempt guard; extend with strategy selection, next-request checks, and per-attempt usage. Do not run the existing fixture unchanged or build a general evaluation framework.
- Reuse `thinking_investigation_test.go` for wire comparisons, `thinking_binding_test.go` for mock recovery/diagnostics, and the workers' recovery/history findings as offline gates. Mock signatures establish no provider validity.
- Reuse `loop/loop_test.go`'s local tool/recording scaffolding for task execution. Avoid `NewClaudeTestHarness` and broad credential-enabled suites: they can make extra paid calls.

## Three strategies: compare the full behavior honestly

| Arm | Request and recovery behavior |
| --- | --- |
| **A — historical strategy** | Pin the pre-preservation `dca552b2` request/recovery implementation: remove thinking from every assistant message except the latest; retain its existing strip-all-on-signature-error fallback. No newly introduced binding beta or drop controls. |
| **B — strict diagnostic control** | Preserve historical thinking; request strict binding (`error`) where supported; exactly one attempt, no silent strip/drop recovery. Keep ordinary malformed-block sanitizers. |
| **C — new strategy** | Current preservation plus binding `drop_block`, provider-drop diagnostics, and existing signature-error fallback. |

Confirm A against the historical source, including headers, sanitizer ordering, and retry trigger; `oldThinkingPolicyRequest` alone is **not** exact A because it inherits new binding controls. Keep the production binaries untouched; use a test-only strategy seam with offline wire assertions. External safety caps apply to all arms.
This is a **full adapter-strategy comparison**, not an isolated retention experiment. The prior cache A/B held new binding controls constant and changed only age-stripping; keep that result separate. B diagnoses rejection, not task quality.

## Phase B — smallest useful live protocol batch (approval required)

- Proposed model: Fable 5.1, where a strict-prefix 400 was previously observed; fixed high effort, identical prompts/tools/settings, and genuine signed synthetic history generated under that exact prefix. Freeze the case manifest after Phase A.
- Two sequential tool-call seeds must yield usable signed blocks in older and latest positions. Keep complete blocks in memory; never fabricate a valid signature or transplant one onto edited thinking. A deliberate corrupt copy is a negative control only.
- First run unchanged-history A/B/C. Then test the two selected cases. Candidate reconstructions, if archival evidence is insufficient: an older signed block made invalid and a latest signed block made invalid; a prefix mutation is an explicitly modern binding control, not evidence of alias rotation.
- **Each case must actually produce Anthropic HTTP 400 in B with the intended signature/binding error.** A local mock, client validation failure, refusal, or unrelated 400 does not qualify. If B accepts or fails differently, stop: report case not reproduced, with no replacement search using leftover calls.
- Fork the same frozen history into A/B/C; keep tool outputs identical. A/C must either avoid the error or recover transparently within one retry. Append each successful response to its own original stored history, then make a follow-up request without silently repairing that history. Record whether recovery repeats.
- Run B first to qualify each case; counterbalance A/C versus C/A across the two cases. Shared seeds warm shared caches: these are frozen protocol replays, **not independent samples or cache-isolated latency measurements**.

| Generation attempts, including seeds/failures/retries | Maximum |
| --- | ---: |
| Two seeds + unchanged-history A/B/C, one attempt each | 5 |
| Each of two cases: B (1), A (2), C (2), follow-up A (2), follow-up C (2) | 18 |
| **Phase B total** | **23** |

Add at most **one catalog GET**: **24 HTTP requests**, **2,048 output tokens per generation**, **47,104 output tokens total**. No retries except the designated A/C signature recoveries. A missing genuine seed stops the batch; no regeneration allowance.

## Phase C — actual multi-tool coding, A versus C (separate approval)

Only after the protocol results and worker defects are reviewed, run two compact dependency-free Go debugging tasks, with frozen source/prompts/public tests and hidden tests outside the tool sandbox:
1. **Interval merge bug:** inspect API and implementation separately, run the public failure, patch, test, submit. Hidden oracle exhausts small half-open interval combinations, overlaps versus touching boundaries, unsorted/nested inputs, and input immutability.
2. **Streaming line parser bug:** inspect reader and consumer, run the public failure, patch, test, submit. Hidden oracle covers every split point of fixed LF/CRLF inputs, unterminated final records, empty records under the stated contract, and no loss/duplication.
Use real executable hidden oracles, not checksum answers, last-number checks, or an LLM grader. Freeze allowed-file rules; test modification or malformed submission fails. Require at least two sequential assistant inspection/tool rounds so older history actually ages; report absent signed thinking as unexposed, never equivalence.
Each task gets one A/C pair: **four runs / two pairs**. Randomly assign which task runs AC and which CA. Same clean starting checkout, tools, prompts, model (proposed Opus 5/high effort), limits, and public results for identical actions; genuine trajectories may diverge. Never force canned successful tool outputs after divergent edits.
Start every arm with empty history and an independent equal-length random prefix nonce assigned before signing; isolate every cacheable prefix, including tools, or disclose residual sharing and do not claim cache isolation. No post-signing nonce edits. Cold-start first, natural within-run warming; no checksum padding or paid warm-up calls. Report when the task never reaches cache eligibility.
This is **interactive end-to-end** evaluation, not frozen-history replay. Keep each arm's own generated history, filesystem, tool results, and retries. All setup responses count toward its budget.
Each run permits **six primary generations plus one signature-recovery attempt total**, 12 local tool operations, and no automatic outer retry. Final submission must fit the six calls. No extra seeds, summaries, titles, slugs, subagents, or evaluator calls.
**Phase C cap: 28 generations + one catalog GET = 29 HTTP requests; 2,048 output tokens each; 57,344 output tokens total.** Both separately approved phases together cannot exceed **53 HTTP requests / 51 generations / 104,448 output tokens**. Unused slots cannot become extra samples.

## Safety, metrics, and decision

- Enforce a shared pre-send ledger across adapter/loop layers. Reserve the full 2,048 output tokens for every generation attempt, including failures with unknown usage; reject redirects and unmetered auxiliary calls. No dollars/pricing assumptions.
- One in-flight call; 90-second logical-request deadline including recovery, 15-second catalog timeout, 10-second local tool timeout, 30-second hidden-test timeout. Phase B wall cap 40 minutes; Phase C 60 minutes. No sleeps or unbounded agent loops.
- Stop on budget/time overrun, absent/refused seed, model change, unexpected stream/HTTP error, truncation, unexplained drops, or signature leakage. Designated A/C signature errors may finish their single recovery; recurring recovery on the follow-up is a defect and stops progression, even if successful. No automatic reruns.
- Ordinary hidden-test failure is scored without repair calls; finish its pair, then pause for review. Missing/unrun arms remain censored, not assumed successes or losses.
- Report task pass/fail first, then unrecovered errors, total attempts, retry layer/reason, repeated recovery, drops, end-to-end wall time and per-request latency. Include tool time and every failed attempt.
- Report **all** uncached input, cache-creation input, cache-read input, and output tokens per attempt and summed per run. Separate thinking/visible output only if reported; never double-count output or treat missing usage as zero. Include seeds/retries; cache hit share alone is not savings.
- Pilot success means qualified real-400 controls, explained bounded A/C outcomes, clean follow-ups, and scored coding results—not proof of noninferiority, historical causation, or production acceptance. Two task pairs cannot settle quality/reliability rates; no rollout authorized.
- PM write-up: **bugs and actual incident evidence first; cheap QOL fixes second; measured cache/task results third; hypotheses and remaining gaps last.** Retry-reason diagnostics are already fixed at baseline; no new thinking-drop banners.
- Pending decision only after Phase A: approve the named case manifest/model and Phase B's 24-request cap, or remain offline. Phase C is independently approved after protocol review. Parent owns integration/PLAN; workers own their tests.
