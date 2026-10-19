# Shelley HTTP/SSE API

This document describes the API contract between a Shelley server and its
clients (the web UI, the iOS app, the CLI in `client/`, and tests). All
routes are mounted under `/api/` unless noted; the version endpoint is at
`/version`.

## Capabilities

```
GET /version
```

Returns build info plus `capabilities` (string list). Capabilities
advertise optional, additive features that clients can opt into when
present; a client that doesn't recognize a capability just doesn't use
it, and an older server that doesn't ship the field is equivalent to
advertising none.

Capabilities advertise optional behavior. Current capabilities include
`thinking-levels`, `drafts`, and `queued-transcriptions`; clients that do not
recognize one ignore it.

## Stream architecture

The server derives the conversation list from the database and publishes
RFC-6902 JSON Patch diffs on a single unified SSE stream:

- `GET /api/stream2` — unified SSE: per-conversation messages **and**
  conversation-list patches.
- `GET /api/conversations/snapshot` — seed the patch stream with the
  current list and its content hash.
- Previews are embedded in each conversation list row (`preview`,
  `preview_updated_at`).
- `GET /api/conversation/<id>/stream` survives for non-web clients (iOS,
  CLI, tests) but new clients should use `/api/stream2`.

The patch stream is driven exclusively by `Pool.OnCommit`: every
successful write transaction triggers a recompute, and a `recomputeMu`
serializes recomputes so concurrent commits can't publish events out of
order. Each event carries `old_hash`/`new_hash`, letting clients
reconcile their state on every patch.

## Endpoints

### Versioning

- `GET /version` — `{tag, commit, commit_time, capabilities: [...]}`.
- `GET /version-check` — `{has_update, current_tag, latest_tag, ...}`.
- `GET /version-changelog` — markdown changelog.

### Conversation list

Unless noted, results exclude **archived** conversations.

- `GET /api/conversations?limit=&offset=` — top-level (non-subagent)
  unarchived conversations as plain rows. Used by iOS and the CLI.
- `GET /api/conversations/snapshot` — the current unarchived list
  including subagents, plus per-row state (working, git info, subagent
  count, preview) and the patch-stream hash. Used by the web UI on load.
  Response:
  ```json
  {
    "conversations": [ConversationWithState, ...],
    "hash": "<sha256 hex>"
  }
  ```
- `GET /api/conversations/archived` — archived list.
- `POST /api/conversations/new` — create a conversation and post the
  first user message. The request accepts `message`, optional `model` and
  `cwd`, and optional `conversation_options`:
  ```json
  {
    "message": "run the tests",
    "model": "glm-5.3-fireworks",
    "conversation_options": {
      "thinking_level": "high",
      "tool_overrides": {"bash": "off"},
      "disable_all_tools": false,
      "disable_notifications": true
    }
  }
  ```
  `thinking_level` is one of `off`, `minimal`, `low`, `medium`, `high`,
  `xhigh`, or `max`; the server also validates it against the selected model's
  advertised levels. Tool override values are `on` or `off`. With
  `disable_all_tools`, an explicit `on` override re-enables that tool.
  The conversation starts from the settings of the profile named by
  `conversation_options.profile`, or the default profile (see Profiles
  below). A non-empty `model` overrides the profile's, as do the
  `thinking_level`, `tool_overrides`, `compact_nudge_tokens`, and
  `system_prompt` options where present, even as `""`, `{}`, or `0`.
- `POST /api/conversations/distill-new-generation` — compact the current
  conversation into the next generation of the same conversation. The
  optional `method` field (`default` or `compact`) is accepted for
  compatibility but is ignored: compaction is always used.

`ConversationWithState` row shape:

| field | meaning |
|---|---|
| `conversation_id`, `slug`, `created_at`, `updated_at`, `cwd`, `archived`, `parent_conversation_id`, `model`, `conversation_options`, `current_generation`, `agent_working`, `user_initiated` | DB columns |
| `working` | mirror of `agent_working`, kept for the patch-stream contract |
| `git_repo_root`, `git_worktree_root`, `git_commit`, `git_subject` | optional, from a cached HEAD lookup keyed by `cwd` |
| `subagent_count` | number of subagent conversations whose `parent_conversation_id` matches this row |
| `preview`, `preview_updated_at` | trailing text of the most recent agent message and its timestamp (RFC 3339); empty if no agent reply yet, or if this conversation is outside the 500-most-recent window the server tracks for previews |

### Single conversation

- `GET /api/conversation/<id>` — full API message history (compressed) plus
  conversation metadata. Message rows include `message_id`,
  `conversation_id`, `sequence_id`, `type`, `llm_data`, `user_data`,
  `usage_data`, `other_usage_data`, `created_at`, `display_data`, `generation`,
  `end_of_turn`, `llm_api_url`, `model_name`, `forked_from_message_id`, and
  `user_email`. The JSON-valued database columns (`llm_data`, `user_data`,
  `usage_data`, `other_usage_data`, and `display_data`) are encoded as JSON
  strings on the wire. `llm_data.Content` carries text, thinking, tool-use,
  and tool-result blocks. Provider continuation secrets and inline image data
  are removed before serving.
- `GET /api/conversation/<id>/stream` — **legacy** SSE: messages, state,
  no list patches. Used by iOS, CLI, and Go tests; new clients should
  use `/api/stream2`. Query params:
    - `?last_sequence_id=<n>` — resume from `sequence_id > n`.
    - `?tail=<n>` — first frame contains only the last `n` messages.
  A `{"snapshot_complete": true}` frame follows the initial replay
  and precedes live updates.
- `POST /api/conversation/<id>/chat` — send a user message. A message of
  `/transcription <absolute uploaded-media path>` creates a durable specialized
  queued message and returns `202 {"status":"queued"}`. The path must name a
  regular file inside `/tmp/shelley-uploads`. A hidden low-reasoning child
  performs the transcription independently of the HTTP request. While it is
  working or failed, the item reserves its FIFO position; when ready, the
  ordinary queue drainer creates the immutable parent user turn. Optional
  context follows the path on subsequent lines of the command and is prepended
  to the transcript, preserving text and ready attachments that were present
  before recording. Video results include the original recording and generated
  contact sheet paths in that turn.
- `POST /api/conversation/<id>/send-queued?queued_id=<id>` — interrupt the
  running turn and immediately drain the FIFO head. The supplied id must still
  name that head item.
- `POST /api/conversation/<id>/cancel-queued?queued_id=<id>` — cancel and remove
  one queued item. Omitting `queued_id` cancels the entire queue. Cancelling a
  working transcription also stops its hidden child.
- `POST /api/conversation/<id>/retry-queued?queued_id=<id>` — retry a failed
  queued transcription in place.
- `POST /api/conversation/<id>/cancel` — interrupt the running loop.
- `POST /api/conversation/<id>/archive` / `unarchive`.
- `POST /api/conversation/<id>/hooks` — register an end-of-turn webhook.
- `POST /api/conversation/<id>/tags` — replace the conversation's tag list.
- `GET /api/conversation/<id>/settings` — the settings later turns use, and
  the profile they came from (a label; either may have changed since):
  ```json
  {"profile": "Research", "model": "claude-opus-5.5", "thinking_level": "high",
   "tool_overrides": {"browser": "off"}, "compact_nudge_tokens": 200000,
   "system_prompt": ""}
  ```
- `POST /api/conversation/<id>/settings` — change them. The body has the same
  fields; those left out keep their values, and `profile`, if present, first
  replaces all of them with that profile's. `"model": ""` means the server's
  default. When `thinking_level` is left out, a model that lacks the level
  rounds it to one it has; an explicit level the model lacks is a `400`, as
  are unknown fields. Responds with the settings now in effect. Unless only
  the profile label changes, a running turn is stopped first, and queued
  messages then go out on the new settings (those the turn had already taken
  in end with it, as with Stop); `409` if another turn starts before the
  change lands.
  Changes record a `modelchange` message saying what changed (`profile_to`,
  `from`/`to`, `reasoning_to`, `tools_on`/`tools_off`, `compact_nudge_tokens`,
  `system_prompt_changed`, `text`, and the settings before, `previous`), just
  after a new `system` message if the template or the tools changed. A fork
  from before the change gets the settings before it. `409` if the
  conversation is archived or a draft (a draft's settings travel with its
  first message), for a custom `system_prompt` in a subagent, or for anything
  but the model and reasoning in a btw reader.
- `POST /api/conversation/<id>/live-session` — start a GPT-Live voice call:
  `{"sdp", "model"?, "cwd"?, "conversation_options"?}` (the settings only for
  a draft) → `201 {"session":{"id"},"transport":{"type","sdp"}}`. Shelley
  writes the voice prompt and holds the key. The session id also names
  Shelley's side of the call (capability `live-call-events`), which decides
  what the user's speech becomes, delivers it as `Spoken request:` chat
  messages (promoting a draft with the call's settings), and tells Live
  about typed messages and final replies. The client relays and follows:
  - `POST /api/conversation/<id>/live-session/<session>/events` —
    `{"events":[<Live data-channel event>...]}`, verbatim and in order, one
    request at a time. Shelley reads `session.input_transcript.delta`,
    `session.output_transcript.delta`, `session.delegation.created` and
    `session.closed`. `204`; `400` `Invalid live events`; `404` `Live call
    not found`.
  - `GET /api/conversation/<id>/live-session/<session>/stream` — SSE of
    instructions, `id` numbered; resume with `?after=<id>` or
    `Last-Event-ID`. `{"type":"send","event"}`: send `event` to Live;
    `{"type":"error","message"}`: show the error, `null` clears it;
    `{"type":"activity","handing_off"}`; `{"type":"stop","message"}`: end the
    call showing `message`. Unknown types are ignored. The call ends a
    minute after its last stream detaches.
  - `DELETE /api/conversation/<id>/live-session/<session>` — hang up: pending
    speech is dropped. `204`.
- `POST /api/conversation/<id>/live-message` — for clients without
  `live-call-events`: deliver one delegation the client decided on,
  `{"transcript", "voice_context"?, "model"?, "cwd"?,
  "conversation_options"?}` → chat's receipt.
- `GET /api/conversation/<id>/subagents` — direct child conversations. Clients
  can recurse through this endpoint when they need the complete descendant
  tree (for example, aggregate usage reporting).
- `GET /api/conversation-by-slug/<slug>` — lookup by slug.

### Unified stream

```
GET /api/stream2?conversation=<id>&conversation_list_hash=<h>&last_sequence_id=<n>
```

SSE stream. A single connection delivers per-conversation events
(messages, tool progress, stream deltas, conversation/state updates) for
**all** active conversations on the server, plus the conversation-list
patch stream. Every per-conversation event carries a top-level
`conversation_id` field for client-side routing.

All query params are optional:

- `conversation` — if set, the first frames replay that conversation's
  message history before live updates begin. It governs **backfill
  only**: live events for every conversation flow regardless.
- `conversation_list_hash` — the `hash` from the most recent snapshot
  or patch event the client successfully applied. The server uses it to
  decide whether to replay history or send a fresh reset event.
- `last_sequence_id`, `tail` — refine the `conversation` backfill, with
  the same semantics as on `/api/conversation/<id>/stream`. A
  `snapshot_complete` frame separates the initial replay (and an empty
  replay on connections without `conversation`) from live updates.

Event payload (`data: <json>`):

```ts
interface StreamResponse {
  // Routing key for per-conversation events. Always set on messages,
  // conversation, conversation_state, context_window_size, tool_progress,
  // and stream_delta. Empty for connection-scoped frames
  // (conversation_list_patch, heartbeat, snapshot_complete) and for
  // global events that already carry their own conversation reference
  // (notification_event).
  conversation_id?: string;

  // Per-conversation event payload. With a single stream serving every
  // active conversation, clients dispatch based on conversation_id.
  messages?: APIMessage[];
  conversation?: Conversation;
  conversation_state?: { conversation_id, working, model };
  context_window_size?: number;
  tool_progress?: ToolProgress;
  stream_delta?: StreamDelta;
  notification_event?: NotificationEvent;

  // Conversation-list patch stream:
  conversation_list_patch?: {
    old_hash: string | null,        // null on a reset event
    new_hash: string,
    patch: RFC6902Op[],             // ops with paths like "/0", "/0/working", etc.
    at: string,                     // RFC 3339
    reset?: true,                   // true for the seed event
  };

  heartbeat?: true;                 // sent every 30s if nothing else to say
  models_changed?: true;            // re-fetch the model list and provider status
  snapshot_complete?: true;         // once, after the initial replay
}
```

The `conversation_list_patch` operates on a document that is exactly the
`conversations` array returned by `/api/conversations/snapshot`. Clients
should:

1. `GET /api/conversations/snapshot` once to obtain `(state, hash)`.
2. Open `/api/stream2?conversation_list_hash=<hash>`.
3. For each `conversation_list_patch` event:
   - If `event.old_hash == null` or `event.reset`, replace local state
     with `event.patch[0].value`.
   - Otherwise, require `event.old_hash == currentHash`; apply
     `event.patch` via RFC 6902; assert `hashList(state) == new_hash`.
   - On any mismatch, drop local state and resume with the snapshot.

Reconnect semantics: the server keeps the last 100 patch events in
memory. If the client's `conversation_list_hash` matches one of those
boundaries, the server replays the missed patches; otherwise it sends a
fresh reset event.

### Git

- `GET /api/git/repos` — repo discovery.
- `GET /api/git/diffs?cwd=` — staged/unstaged file lists.
- `GET /api/git/diffs/<commit>?cwd=` — committed file lists.
- `GET /api/git/file-diff/<path>?cwd=&base=&head=` — unified diff.
- `GET /api/git/graph?cwd=` — commit graph.
- `GET /api/git/commit-detail?cwd=&sha=` — single commit.
- `GET /api/git/commit-messages?cwd=` — recent commit messages.
- `POST /api/git/amend-message` — amend HEAD message.
- `POST /api/git/create-worktree` — `git worktree add`.

### Files & directories

- `GET /api/find-files?dir=&q=&content=skip&include_dirs=true` — fuzzy name
  search. `dir` is absolute; `q` is optional. `include_dirs` is an optional
  boolean (default false); enabling it includes nested and empty folders in
  name results. Invalid or repeated `include_dirs` values return 400.
  Results contain `search_dir` and `matches`; paths are relative to
  `search_dir`. Folder matches have `is_dir: true` and a trailing `/` in
  `path`; file matches omit `is_dir`. `content=only` always returns file-content
  hits, never folders. An explicit existing directory path without a trailing
  slash offers that folder itself when `include_dirs` is true; a trailing
  slash browses inside it. File-only callers retain their existing behavior.
- `GET /api/list-directory?path=` — directory listing.
- `POST /api/create-directory` — `mkdir -p`.
- `POST /api/write-file` — write a file.
- `POST /api/upload` — binary upload (multipart).
- `POST /api/upload/raw?filename=` — binary upload with the file content as
  the request body (no multipart framing). Newer clients prefer this to
  avoid building a multipart body on device. Older servers return 404/405;
  clients should fall back to the multipart endpoint.
- `GET /api/upload/raw` — empty `200 OK` if the server supports the raw
  upload endpoint; clients use this as a capability probe (older servers
  return 404/405).

- `GET /api/read?path=` — read a file (images served as `image/*`).
- `POST /api/validate-cwd` — check whether a path is a valid working
  directory.
- `GET /api/user-agents-md` / `POST` — read/write the per-user
  AGENTS.md.

### Models, tools, notifications

- `GET /api/models` — available models.
- `POST /api/models/refresh` — refresh integration/catalog sources and return
  the model array. Saved provider discovery is separate; this endpoint keeps
  its existing response and failure behavior.
- `GET /api/tools` — registered tool definitions.
- `GET/POST/PUT/DELETE /api/custom-models[/<id>]` — custom model CRUD.
  OpenAI-compatible models accept `reasoning_replay` as `auto`,
  `reasoning_content`, or `none`; responses include
  `resolved_reasoning_replay` when catalog resolution is known.
- `POST /api/custom-models-test` — test a custom model config.
- `GET/POST/PUT/DELETE /api/notification-channels[/<id>]`,
  `GET /api/notification-channel-types` — notification CRUD.

### Saved model providers

A connection stores one endpoint and credential in `model_providers`; that is
all that is saved. Like integrations, its models are discovered at startup and
held in memory. Existing custom models remain independent, including
deliberately configured per-model overrides.

- `GET /api/model-providers` — connections and their current discovery status.
- `POST /api/model-providers/refresh` — refresh saved connections independently
  and return their connection/status array. A discovery failure appears in the
  affected connection's `state` and `error`; other connections can succeed.
  Database failures return HTTP `500`. Without a provider manager, returns `[]`.
- `POST /api/model-providers` — discover and save a connection.
- `POST /api/model-providers/test[?provider_id=<id>]` — check the proposed
  configuration without saving it or publishing routes. Uses the same model
  filtering as Connect and returns `{"chat": ["Display name", ...],
  "key_checked": true}`. `chat` contains the names of exactly the models that
  would be added, not every upstream model ID.
- `PUT /api/model-providers/<id>` — discover proposed settings, then save and
  replace that connection's published routes. An empty `api_key` retains the current key
  only when the endpoint origin is unchanged. Changing scheme, host, or
  non-default port requires the key to be entered again; Test applies the same
  rule when `provider_id` is supplied.
- `DELETE /api/model-providers/<id>` — remove the connection; its routes stop
  resolving.
- `POST /api/model-providers/<id>/refresh` — rediscover only this connection.
  Failure returns a non-success HTTP status and preserves the published catalog.

Create/update bodies contain `provider_type`, `endpoint`, `api_key`, and optional
`display_name`. Provider types are `anthropic`, `openai` (Chat Completions), and
`openai-responses`. The endpoint is an API base including its version/prefix,
for example `https://api.anthropic.com/v1` or
`https://example.com/tenant/api/v1`. Discovery appends `/models`; inference
appends the selected protocol's path. Shelley does not switch protocols or
credentials when discovery fails.

Hostnames and default ports are normalized; custom prefixes and path case are
preserved. Saving the same canonical endpoint and key twice returns `409`,
including across Chat Completions and Responses connections. Test also checks
for this duplicate, excluding the connection being edited. Different keys can
use the same endpoint; an omitted account label gets a host-based name with a
numeric suffix when necessary.

Successful discovery is not always an authentication check: some providers
publish their model list publicly. OpenRouter's public listing is followed by
its authenticated key-information request. For compatible endpoints without
an authentication-specific check, `key_checked` is false when listing succeeds
without a key. This is not a successful inference probe, and no inference
request is made automatically.

Responses contain `provider_id`, `provider_type`, `endpoint`, `display_name`,
`has_api_key`, `created_at`, `updated_at`, `state` (`ready`, `error`, or
`discovering`), `model_count`, and optional
`last_success` and `error`. They never return the saved API key. Credentials
are stored in the local SQLite database; response redaction is not encryption
at rest. Protect the database and its backups accordingly.

Model responses identify ownership using `source_id`, independently of the
human-readable source label. Runtime IDs encode both the connection ID and
upstream model ID; `api_model_name` retains the wire ID. Rotating credentials
does not rename routes, and matching model names on two accounts do not merge
their routes.

Discovery determines available IDs. Provider metadata and the embedded
models.dev snapshot enrich capabilities, limits, and names. A Shelley catalog
entry is not required when an exact metadata match establishes text input,
text output, and tool support. Capability provenance distinguishes exact catalog
matches from advisory matches and unknown fields. Catalog-reported support is
not verification that this endpoint/key can run the model. In particular,
OpenAI model listing does not determine Chat Completions versus Responses
support.

Eligible routes are supported Shelley catalog entries or exact metadata matches
establishing agent compatibility, unless authoritative metadata establishes
incompatibility. Known incompatible, unknown, and advisory-only models are not
automatically added. Check and Connect apply this identical filter, so their
counts agree. Picker tiers affect prominence, not availability: discovering a
newer model does not remove an older model that is still listed and eligible.
Use Add single model for deliberate configuration of an excluded model. This
conservative policy can omit newly released usable models until the catalog
gains sufficient metadata.

The optional `capabilities.provenance` map marks individual provider-supplied
fields as `provider`; remaining fields retain the catalog's `match` strength.
Reporting one provider capability never promotes unrelated advisory metadata.
Reported reasoning support alone does not establish a wire vocabulary, so
models without a known reasoning vocabulary have reasoning controls disabled.
Reasoning controls are separate from replaying a model's reasoning state during
tool continuations; saved routes retain the existing transport's origin checks
and replay behavior.
Saved routes use an authoritative output limit when available; missing or
advisory-only limits use the conservative 8192-token allowance. Their
context limit is the reported technical limit, not a pricing-tier threshold.

The UI's Refresh All calls the existing model-refresh and saved-provider-refresh
endpoints independently, then reads the current model list. Provider failures
remain visible without changing the legacy API's response contract or the
existing integration refresh policy.

Startup does not wait for discovery: connections are discovered in the
background, with bounded concurrency, publishing results in saved-source order.
Until then a connection's `state` is `discovering`; one that fails is retried
once after 30 seconds. Catalog and status changes notify open clients over
`/api/stream2` using `models_changed: true`; clients re-fetch the current lists
without triggering another discovery. Once provider state has changed, a
stream's initial notification also covers changes between page load and
subscription, including reconnects after the last provider was deleted.
An installation that has not published any provider state sends no such frame.
Within each connection, accepted models are
ordered newest-first by the available release date; undated models follow
dated models and equal dates retain the provider's order. Check, Add, and
Refresh share this ordering. There is no further timer-based refresh: sources
that still fail are retried with Refresh. A failed refresh keeps the published
catalog and `last_success`; a failed startup discovery publishes nothing and
reports the error, while a database failure is an initialization error. Label-only edits
make no request and keep the catalog and status. An unchanged refresh records
its success without replacing the running services.

Discovery and key checks run outside the provider mutation lock. Edits and
deletions can complete while another request is still discovering models.
Only current results are published: a late refresh cannot overwrite newer
settings or recreate a deleted connection. An edit superseded by a newer
configuration change returns `409`; duplicate endpoint/key pairs are checked
again when saving.

A route ID that the published catalog does not list (discovery failed, or the
provider stopped listing the model) is rebuilt from the ID and its
connection's current endpoint and key, with metadata from the embedded
catalog. Existing conversations therefore keep their model and account, and
the provider itself reports whether the model still works. Such routes are
never offered for new chats. Once the connection is removed they fail with an
explicit error and need a replacement model rather than another account.
Connections do not implicitly become recording/transcription credential
sources.

Refreshing or editing a source preserves its registry position and does not
silently change the fallback default account, including when individual and
saved-provider models coexist. A failed conversation-service rebuild retains
the selected route; retries must not substitute the default account.
The existing `shelley models` CLI remains database-independent and lists
integration/catalog sources, not DB-backed providers or individual models.
Saved routes offered to subagent
and one-shot tools are scoped to the parent conversation's provider connection;
at most 32 saved routes are offered, retaining the parent and preferring the
newest release dates with deterministic ties. This bound affects helper tool
schemas, not the conversation picker. Legacy integration/custom-model
selection behavior remains separate. Helper
selection for a saved route must use its stable source identity, not an account
label shared by another connection.

The storage migration is additive: one `model_providers` table, without
rewriting conversations or existing custom models. For application rollback,
leave that table and the migration record intact and run the previous binary.
That binary does not know saved-provider route IDs, so conversations using
those routes require an explicit model change; their history is preserved.
Re-upgrading resolves those routes again. Restoring an older database
backup is not the normal rollback path because it discards newer conversations.

### Profiles

A profile is a named set of conversation settings: `model` (`""` means the
server's default model), `thinking_level` (`""` means the model's default),
`tool_overrides`, `compact_nudge_tokens` (`0` means the default), and
`system_prompt`, a Go [text/template](https://pkg.go.dev/text/template)
(`""` means Shelley's built-in prompt, which changes with Shelley). Exactly
one profile is the default; new conversations start from it unless they name
another. A conversation records the profile it came from in
`conversation_options.profile`. Its settings are copy-on-write: until a
change of its own sets `conversation_options.settings_edited`, the
conversation takes the profile's edits at its next turn, with a marker saying
what changed. Switching to a profile clears the flag. A deleted profile
leaves the settings as they are.

Shelley pre-installs `Default` (the coding profile, initially selected) and
`Assistant` (an opt-in, text-message-first personal assistant for life and
work). Assistant enables `message_user` and `compact_in_place` with a
130k-token compaction nudge, disables `llm_one_shot` and `output_iframe`,
replies in plain text through `message_user`, compacts in place after 8 idle
minutes and keeps the prompt cache warm until then. Both profiles use the
server's default model. Profiles can be edited; a non-default
profile can be deleted. Built-in profiles have `builtin: true` and start with
`user_edited: false`; Shelley refreshes them on startup until an actual user
edit sets `user_edited: true`. Unchanged saves and default selection do not
count as edits. Restore defaults resets their settings and clears that flag.
Deleted profiles and pre-existing custom profiles with the same name stay
untouched. Restoring follows the same copy-on-write rule as edits.

New messaging webhook and web chats start from `Assistant` when it exists;
otherwise they use the server's default model and built-in prompt. They
always enable `message_user`. A first message's explicit `system_prompt`
overrides the profile's prompt and makes the chat's settings its own. Later
events' prompts are ignored.

A profile also has a `voice_prompt` (trimmed, up to 4000 characters; `""`
means none): who voice calls speak as. It is not a setting: conversations
don't copy it, and a call reads the current voice prompt of the profile its
conversation names.

- `GET /api/profiles` — all profiles, default first:
  `[{"name", "default", "builtin", "user_edited", "model", "thinking_level", "tool_overrides",
  "compact_nudge_tokens", "system_prompt", "voice_prompt"}]`.
- `POST /api/profiles` — create one: `name` (trimmed; up to 64 bytes, no
  `/` or control characters, not `.` or `..`), the settings fields and
  optionally `voice_prompt` (unknown fields are a `400`); `409` if the name
  is taken.
- `PUT /api/profiles/<name>` — replace a profile's settings; the body has
  only the settings fields and optionally `voice_prompt`, which keeps its
  value when absent. Profiles can't be renamed.
- `POST /api/profiles/voice-prompt/generate` — suggest a voice prompt:
  `{"model", "system_prompt"}` → `{"voice_prompt"}`. `""` means the server's
  default model or the built-in system prompt; an unknown model is a `400`.
  A model of the same provider and account writes it from the system prompt
  alone; nothing is saved. `502` if it fails.
- `POST /api/profiles/<name>/default` — make it the default profile.
- `POST /api/profiles/<name>/restore` — restore a built-in's current shipped
  settings and voice prompt, re-enabling automatic updates. Returns the
  profile; `400` for a custom profile, `404` if missing. Default selection
  and existing conversations are unchanged.
- `DELETE /api/profiles/<name>` — `409` for the default profile.
- `GET /api/system-prompt` — the built-in template and the variables a
  template can use: `{"template", "variables": [{"name", "description"}]}`.
  Templates are rendered with `missingkey=error` and checked on save, with
  every optional variable both set and unset (outside a git repository,
  `.GitInfo` is nil); a template that fails is a `400` saying where and why,
  e.g. `Invalid system_prompt: line 2: unknown variable .Nope`.
- `POST /api/system-prompt/check` — check a template as a save would:
  `{"template"}` → `{"error": null}` or
  `{"error": {"line", "column", "message"}}`, the line and the column (in
  characters) counted from 1 and left out when unknown.

### Shell

- `WS /api/exec-ws?cwd=` — websocket for an interactive shell session.
  `cmd=` starts a new persistent session; `term_id=` reattaches to an existing
  one and never reruns the command. `conversation_id=` records which
  conversation owns a newly spawned terminal.
- `GET /api/terminals` — all persistent terminals, unfiltered.
  `conversation_id` is the owning conversation, or `null` for a terminal shown
  in every conversation.
- `PUT /api/terminals/<id>/scope` — move a terminal between conversations.
  Body `{"conversation_id": "<id>"}` confines it to that conversation;
  `{"conversation_id": null}` shows it in all of them. `null` is the only
  accepted spelling of global: an absent field or an empty string is a 400.
  Returns the updated terminal.
- `DELETE /api/terminals/<id>`, `POST /api/terminals/<id>/kill` — terminate a
  terminal and drop its record.

### Debug

- `GET /debug/` — index of the debug pages.
- `GET /debug/conversation-stream/history` — JSON dump of the last 100
  patch events.
