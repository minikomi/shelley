# Automatic compaction: plan and status

One feature flag switches compaction from *manual, task-report* to *automatic,
source-mapped*. The underlying mechanism does not change: same cut point, same
verbatim recent tail, same generation bump, same rollback. Only the trigger and
the summarizer change.

    automatic-compaction = off   ->  today's behavior, exactly
    automatic-compaction = on    ->  self-triggering + checkpoint summary + [seq:N] retrieval

Deliberately NOT doing what the parked `custom` branch did: no new
`compaction` message type, no context-assembly change, no `conversation_read`
tool. That branch appended a message and taught `partitionMessages` about
cutoffs — a second mechanism living beside compaction. Reusing the generation
bump means the only new code is prompt, reduction, and trigger.

## Pipeline

The ordering is the point: reduce for free, then spend one call, then append
facts we already know.

    raw history -> deterministic reduction -> ONE llm call -> + host repo facts

Stage 1 costs nothing and no round-trip. Stage 2 is one call — no retry loop, no
classification pass, no second model deciding what matters. Compaction sits
between turns and the failure it guards against is a context window filling up;
a fix that doubles the latency of the fix is not a fix. Stage 3 appends branch,
commit, and files patched rather than paying output tokens for the model to
infer them from the transcript and occasionally get them wrong.

## Status

| # | Piece | State |
|---|---|---|
| 1 | `automatic-compaction` flag registered | done, but **inert** — nothing reads it |
| 2 | `SHELLEY_DB` exported to bash/terminals | done |
| 3 | Stage 1: deterministic reduction | done (`server/compact_checkpoint.go`) |
| 4 | Stage 2: checkpoint prompt + `[seq:N]` + validation | done |
| 5 | Pointer sanitization | done |
| 6 | Stage 3: host repo facts | done |
| 7 | Retrieval suffix (sqlite directive) | done |
| 8 | Fast: workhorse summarizer, thinking off, no thinking in input | done |
| 9 | Provenance on the summary message | done |
| 10 | Thread `checkpoint bool` through the distill path | ~90%, 3 compile errors |
| 11 | **Read the flag; decide `checkpoint`** | **not started** |
| 12 | **Automatic trigger** | **not started** |
| 13 | Extract compaction setup from the HTTP handler | not started — biggest piece |
| 14 | Tests | not started |
| 15 | UI (status wording, provenance, flag toggle copy) | not started |

Roughly: the summarizer half is written, the trigger half is not.

## What is written

`server/compact_checkpoint.go` (416 lines, new):

- **Reduction budgets.** A flat cap spends the transcript budget evenly across
  messages worth wildly different amounts. Tool results are the bulk of a coding
  transcript and the least dense per byte (600 tok); user messages carry the
  requirements the summary exists to preserve (4000 tok); assistant prose sits
  between (1500). Last 30 messages get 3x, because detail near the cut point is
  what the next turn most likely needs.
- **Middle truncation.** The start of a command carries intent, the end carries
  the result, so the middle is the least valuable part to cut. One omission
  marker spelling, generated from one format string, quoted in the prompt from
  that same constant — a second spelling would silently defeat the instruction
  that teaches the model to read it.
- **Failed/empty tool results collapse to one line, never to zero.** "grep found
  nothing" is negative knowledge, the same class as an approach tried and
  abandoned, and negative knowledge is what summarizers are measured losing.
  Every "this output did not matter" heuristic scores an empty result as noise,
  which is backwards.
- **Thinking dropped from summarizer input.** Bulkiest content, least
  load-bearing: anything it concluded that mattered was acted on, so it is
  visible in what followed.
- **Transcript cap** at 60k tokens, dropping OLDEST entries first, with a marker
  saying the dropped span is still in the database.
- **Every entry keeps its `[seq:N]` marker even when its body is cut to one
  line.** This is what makes aggressive reduction safe rather than merely small.
- **Prompt** asks for a small task graph in a fenced `state` block (max 8 lines,
  `done|active|blocked|rejected`) then short sourced notes. The graph exists
  mainly for `rejected`: prose buries a rejected approach in a clause, and an
  unrecorded rejected approach gets attempted a second time. The cap is not
  decoration — uncapped, summarizers emit every passing finding as a node,
  measured at 9-15 per conversation for +33% output tokens and no measured gain.
  Directives are ranked explicitly above identifiers: a reader who loses a path
  can search for it, a reader who loses a constraint breaks it.
- **`sanitizeCheckpointPointers`** validates *existence in this conversation*,
  not membership in the summarized span, and marks removals visibly. A
  fabricated pointer is worse than no citation: the reader runs a query, gets
  something unrelated, and has no signal the citation was invented. Existence
  rather than span, because the prompt tells the model to carry pointers forward
  from a previous summary — those cite messages from before this compaction's
  input, and rejecting them would delete correct citations for following
  instructions, erasing exactly the oldest evidence that recursive summarizing
  can least reconstruct.
- **Retrieval suffix** states outright that history was not deleted and gives
  the `sqlite3 "$SHELLEY_DB"` query. This is the join that makes a lossy summary
  correct rather than just small, and it needs no new tool: compaction starts a
  new generation and leaves the old rows in place, and `SHELLEY_DB` /
  `SHELLEY_CONVERSATION_ID` are already in every bash environment.

## What remains

### 11. Read the flag

`performPiDistillation` takes `checkpoint bool` but nothing sets it. One call to
`s.featureFlagEnabled(ctx, FlagAutomaticCompaction)` at the two entry points
(the HTTP handler, and the new trigger). The flag is read per-compaction, not
cached, so toggling it takes effect on the next compaction.

### 12. Automatic trigger

`maybeScheduleCompaction(ctx, conversationID, createdMsg)`, called from the two
`markAgentDone` sites in `server/server.go` (~1153 in `recordMessage`, ~1308 in
`recordMessages`). Never blocks, never returns an error: failing to schedule
just means no compaction this turn.

Guards, in order:

- flag off -> return
- no active manager -> return
- **`manager.HasPendingWork()` -> return.** A turn ending is not the conversation
  going idle. If the user typed while the agent ran, or a subagent finished, the
  drain feeds that straight back into the loop — compacting now would summarize
  a conversation about to keep growing *and* stall that queued work behind a
  summarization call. Needs adding to `ConversationManager` (exists on the parked
  branch; `len(cm.pendingBatches) > 0`).
- `calculateContextWindowSizeFromMsg(createdMsg)` vs
  `svc.TokenContextWindow() * 0.7` -> under, return
- `manager.BeginDistillingSetup()` fails -> return (a manual compaction is
  already running; do not race it)

Then `go` the same work the handler does.

Threshold: 0.7 of the window, matching the parked branch. Open question whether
it should be absolute or window-derived — window-derived for now, since the
window is knowable and an absolute number would be wrong on every model at once.

No guard on *predicted savings*. Benefit is `tokens_saved x turns_alive` and
`turns_alive` is unknowable at decision time, so any cutoff on `saved` filters on
noise — measured: one compaction freed 264 tokens, then ran 49 turns and returned
6.1x. The guard worth having is on new material since the last compaction, which
the existing cut-point calculation already gives (`findPiCutPoint` returning 0
means nothing to do).

### 13. Extract compaction setup

The trigger cannot call `handleDistillNewGeneration` — it is an
`http.HandlerFunc`, and ~80 lines of its body are the compaction setup sequence
that both entry points need, inline:

    validate model -> BeginDistillingSetup -> update cwd/model
    -> IncrementConversationGeneration -> ResetLoop
    -> insert "Compacting…" status -> Hydrate -> notify

Extract as `func (s *Server) startCompaction(ctx, conversationID, modelID, instructions string, checkpoint bool) error`,
with the handler reduced to request decoding plus a call to it. This is the only
structural refactor in the plan, and it is mechanical. It matters because every
failure path in there rolls back the generation bump — duplicating that logic in
the trigger is how you get a conversation stranded on an empty generation.

### 14. Tests

- `reduceCheckpointTranscript`: budgets applied per role; recent boost; oldest
  dropped at the cap; **`[seq:N]` present on every entry including collapsed
  ones**; failed and empty results collapse to one line and not zero.
- `sanitizeCheckpointPointers`: valid kept; fabricated marked; a pointer from
  before the summarized span kept (the carry-forward case); reversed range
  rejected; ids from another conversation rejected.
- `validateCheckpointSummary`: refusal text rejected.
- `truncateMiddleForSummary`: UTF-8 boundaries; marker matches the constant.
- `checkpointHostFacts`: patch paths counted and sorted; non-repo cwd omits the
  block.
- Trigger, with the predictable model: under threshold does nothing; over
  threshold with pending work does nothing; over threshold and idle compacts
  once and not twice; flag off never fires.
- Flag off produces byte-identical output to today (guards the whole premise).

No sleeps. The predictable model returns a structurally valid working state so
validation is exercised.

### 15. UI

- `DistillStatusMessage.vue`: "Checkpointing…/Checkpointed" for
  `distill_method: "checkpoint"`.
- `Message.vue`: title "Checkpoint summary"; provenance line
  `messages N–M · summarized by <model>` from the user_data already stamped.
- An automatic compaction appears with no user action, so the status message is
  the only thing telling the user why the conversation just changed. It must say
  it was automatic, not merely that it happened.
- Flag description must state what turning it on actually changes — both halves,
  not just "automatic".

## Then measure, before trusting any of it

The parked branch's eval harness (`server/testdata/checkpoint/`, three real
conversations with hand-checked answer keys, dual contained-vs-recoverable
scoring) is the tool for this and is worth porting *after* the above lands.

The two numbers that decide whether this ships on by default:

1. **Latency**, against the workhorse model. This is the whole "FAST FAST FAST"
   claim and it is currently unmeasured here. The parked branch measured 113-153s
   with a large model at 10-12k output tokens; the reduction stages and the
   smaller model should cut that hard, but "should" is not a measurement.
2. **Critical fact recall**, scored recoverable-not-just-contained. A checkpoint
   is allowed to drop a detail if it cites the message holding it — scoring
   containment alone reports every deliberate compression as a regression.

Known-unknown to state plainly: recoverability is a property of the *text*. It
says a reader could fetch the original. Whether the agent actually runs the
sqlite query when it needs detail is unmeasured, and the entire
lossy-but-recoverable design rests on it doing so. That is the first thing to
watch once this is on.
