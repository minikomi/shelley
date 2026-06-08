# LazyCue

We may want Playwright/Selenium/Cypress/etc. tests, but we don't want
to maintain them. If we're being honest, we don't want to write them
either.

LazyCue is an experiment in "self-healing" browser automation tests. The
tests themselves are free form instructions, and an agent, at run time,
interprets those instructions. Then, the instructions are cached, and the
cached instructions are re-used over and over again. When that eventually
fails, an agent is invoked again to fix them up.

Since we, as an industry, are not entirely comfortable with CI tooling that
modifies the commit under test, the cache is externalized, but not too far:
we use the git repo that LazyCue is being invoked from, and hide the
cache in some git refs.

## Quick Start

```bash
go run github.com/boldsoftware/shelley/lazycue/cmd/lazycue@latest \
  --base-url http://localhost:3000 \
  'Navigate to / and verify the page title is "My App". The login button should be visible.'
```

Or from Go tests:

```go
var app = lazycue.New(lazycue.Options{BaseURL: "http://localhost:3000"})

func TestHomepage(t *testing.T) {
    app.Test(t, `Navigate to / and verify the page title is "My App". The login button should be visible.`)
}
```

## Workflow of a Run

1. Hash description → check `refs/lazycue/<hash>/<commit>/v<N>` in git
2. If cached: execute DSL steps. If passes → done (fast path, ~1-2s)
3. If cached but fails mechanically: spawn LLM agent to fix → save new version
4. If cached but app is genuinely broken: **fail the test with explanation**
5. If not cached: spawn LLM agent to generate → save v1

### DSL

Cached tests are stored as JSON as arrays of steps, for example:

```json
[
  {"action": "navigate", "url": "/"},
  {"action": "assert_title", "text": "My App"},
  {"action": "wait_visible", "selector": "#login-button", "timeout": "10s"},
  {"action": "click", "selector": "#login-button"},
  {"action": "wait_text", "text": "Welcome back", "timeout": "10s"}
]
```

### Self-Healing vs Genuine Failures

The agent distinguishes between:
- **Mechanical failures**: wrong selectors, timing issues, missing waits → self-heals
- **Genuine failures**: app doesn't match the description → fails with explanation

The test description is the source of truth. If the description says "title should be X"
and the app shows "Y", that's a genuine failure.

## Usage

### CLI

```bash
# Single test
go run github.com/boldsoftware/shelley/lazycue/cmd/lazycue@latest \
  --base-url http://localhost:3000 "Navigate to / and verify the title is My App"

# Multiple tests
go run github.com/boldsoftware/shelley/lazycue/cmd/lazycue@latest \
  --base-url http://localhost:3000 "test one" "test two" "test three"
```

### CI Workflow

In CI, use `--no-push` to save cache locally during the build, then `promote`
to push refs to the remote only after the build succeeds:

```bash
LAZY=github.com/boldsoftware/shelley/lazycue/cmd/lazycue@latest

# Run tests without pushing cache to remote
go run $LAZY --no-push --base-url http://localhost:3000 "test one" "test two"

# If CI passes, push cached refs to remote for future runs
go run $LAZY promote --commit $(git rev-parse HEAD)
```

This prevents broken tests from polluting the shared cache.

### Go Tests

```go
package myapp_test

import (
    "testing"

    lazycue "github.com/boldsoftware/shelley/lazycue"
)

var app = lazycue.New(lazycue.Options{BaseURL: "http://localhost:3000"})

func TestLogin(t *testing.T) {
    app.Test(t, "Navigate to /login, fill email with user@test.com and password with secret, click Submit, verify the dashboard heading appears")
}

func TestHomepage(t *testing.T) {
    app.Test(t, "Navigate to / and verify the page title is My App")
}
```

`Test` calls `t.Fatal` on failure and logs each step result via `t.Log`.
The agent discovers app structure automatically via screenshots and `git grep`.

## Configuration

| Flag / Option | Default | Description |
|---------------|---------|-------------|
| `--base-url` | | App URL (**required**) |
| `--remote` | `origin` | Git remote for cache |
| `--model` | `claude-sonnet-4-6` | LLM model |
| `--api-url` | `ANTHROPIC_BASE_URL` or `https://api.anthropic.com` | Anthropic API base URL |
| `--api-key` | `ANTHROPIC_API_KEY` | Anthropic API key |
| `--verbose` | false | Verbose output |
| `--no-push` | false | Save cache locally only (use with `promote`) |
| `--no-fetch` | false | Use local cache only — don't fetch refs from the remote |
| `--commit` | HEAD | Pin cache to a specific commit SHA; controls which cached tests are visible via git ancestry |

### Subcommands

**`promote`** — push locally saved cache refs to the remote:

```bash
go run github.com/boldsoftware/shelley/lazycue/cmd/lazycue@latest promote
go run github.com/boldsoftware/shelley/lazycue/cmd/lazycue@latest promote --commit abc123...
```

Used after `--no-push` in CI to publish cache only when the build is green.

## How the Cache Works

Cached tests live inside your git repo as **git refs pointing to blobs** —
not as files in the working tree. Nothing shows up in `git status` or diffs.

### Storage

When the agent generates or heals a test, it:

1. Serializes the DSL steps + metadata to JSON
2. Writes the JSON as a git blob (`git hash-object -w`)
3. Creates a ref pointing to that blob (`git update-ref`)
4. Pushes the ref to the remote (unless `--no-push`)

The ref path encodes everything needed for lookup:

```
refs/lazycue/<desc_hash>/<commit_sha>/v<version>-<random>
│                    │            │            │         │
│                    │            │            │         └─ collision avoidance
│                    │            │            └─ version (increments on heal)
│                    │            └─ commit the test was generated against
│                    └─ SHA-256 of the test description (first 16 hex chars)
└─ namespace
```

### Lookup

On each run, the tool:

1. Checks **local** refs first (no network)
2. If no local hit, fetches refs from the remote (`git fetch origin refs/lazycue/*`)
3. Finds all refs matching the description hash
4. Filters by **git ancestry**: only refs whose commit is an ancestor of HEAD (or `--commit`)
5. Picks the highest version number

Ancestry filtering gives you branch isolation for free: a feature branch
sees main's cache (main is an ancestor), but main doesn't see feature
branch caches (the feature branch commit isn't an ancestor of main).

### What's in the blob

```json
{
  "steps": [
    {"action": "navigate", "url": "/"},
    {"action": "assert_title", "text": "My App"}
  ],
  "metadata": {
    "created_at": "2025-06-07T...",
    "model": "claude-sonnet-4-6",
    "input_tokens": 12450,
    "output_tokens": 890,
    "estimated_cost_usd": 0.051,
    "git_sha": "abc123...",
    "mode": "generated"
  }
}
```

### Inspecting

```bash
# List all cached test refs
git for-each-ref --format='%(refname)' 'refs/lazycue/'

# View a cached test
git cat-file blob $(git rev-parse refs/lazycue/<hash>/<commit>/v1-abc123) | jq .
```
