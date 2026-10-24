# Reasoning rules and tests

`reasoning_replay.go` owns outgoing Chat Completions reasoning policy: resolving
replay settings, identifying provider routes, and handling absent or blank
reasoning. `chat_request.go` applies that policy after filtering by origin.
The SDK serializes ordinary requests, including omitted and meaningful reasoning.
Only requests needing an explicit empty reasoning field use Shelley's serializer:
its `*string` distinguishes omission from `""`. That body is supplied through the
SDK's HTTP-client hook, preserving authentication, response decoding, and streaming.
Stored history, incoming responses, and stream deltas are not normalized.

`reasoning_policy_test.go` is the serialized-request matrix for Fireworks,
DeepSeek, Moonshot, and OpenAI. It covers missing/blank/meaningful thinking,
replay settings, model switches, tool calls, streaming, and history preservation.
Fireworks route cases are in `reasoning_replay_test.go`; DeepSeek hostname cases
are in `oai_test.go`.

`reasoning_live_test.go` contains the live endpoint/model table and expected API
contracts. These expectations are independent of the implementation. Each route
checks omitted, null, empty, space, and meaningful `reasoning_content`, followed
by real adapter tool loops and model switches with and without streaming.
An accepted field does not imply the model uses it or generates useful thinking.
Successful responses must finish with the expected answer, without truncation
or whitespace-only thinking. Adapter checks also require a valid tool call and
arguments, successful model-switch replay, and exact stream accumulation.
These are regression checks for observable failures, not a reasoning-quality
benchmark; an absent reasoning field is allowed.

The reasoning live suite requires `SHELLEY_OAI_LIVE_TESTS=1`, even when API keys
are present.
Run the suite with credentials in the test process's environment:

```sh
SHELLEY_OAI_LIVE_TESTS=1 go test -count=1 -v ./llm/oai -run '^TestLiveReasoning$'
```

| Variable | Tested route |
| --- | --- |
| `FIREWORKS_API_KEY` | Fireworks GLM-5.3, direct inference API |
| `OPENAI_API_KEY` | OpenAI GPT-6 Luna, Chat Completions, reasoning effort `none` |
| `DEEPSEEK_API_KEY` | DeepSeek Flash, direct API |
| `ZAI_API_KEY` | Z.ai GLM-5.3, standard API endpoint |
| `MOONSHOT_API_KEY` | Moonshot Kimi K3, direct API |

Luna's Chat Completions tool calls require `reasoning_effort: "none"`; reasoning
with tools uses the Responses API, outside this Chat Completions replay suite.
See the [Luna model documentation](https://developers.openai.com/api/docs/models/gpt-6-luna).

Unset keys skip their provider. `-short` skips the whole live suite. Unexpected
API responses fail the wire checks. Adapter checks use production retries with
a 90-second deadline per call, so transient errors may recover before passing.
`-count=1` disables Go's result cache. Tests use synthetic number-lookup tools;
they do not execute model-generated commands or use the Shelley database.

The suite makes eleven logical calls per provider, sequentially within each
provider; production retries can add requests.
For low-rate accounts, select individual cases with `-run`, for example:

```sh
SHELLEY_OAI_LIVE_TESTS=1 go test -count=1 -v ./llm/oai -run '^TestLiveReasoning$/^moonshot$/^wire$/^empty$'
```

Allow the provider's rate window to recover between invocations. Tests add no
pacing sleeps. Adapter calls retain production backoff; persistent rate limits
can expire the call's deadline. A 429 is an account-limit failure, not evidence
that a reasoning field value is unsupported.

When adding a rule, add its serialized-request case and a live contract case.
Keep endpoint/model expectations specific: one provider's acceptance does not
establish another provider's contract. DeepSeek requires the field on tool-call
history even when replay is disabled; its explicit empty string must never be
collapsed into omission or null.
