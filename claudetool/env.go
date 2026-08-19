package claudetool

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"shelley.exe.dev/gitstate"
)

// ShelleyEnv holds the conversation context that Shelley exposes to commands it
// runs as SHELLEY_* environment variables. It is the single source of truth for
// both code paths that run shell commands:
//
//   - the agent's bash and shell tools, and
//   - interactive "!" terminals spawned from the UI (see buildTerminalEnv).
//
// Keeping the logic in one place ensures the same variables are injected
// regardless of who launched the command, so hooks and scripts behave
// identically.
type ShelleyEnv struct {
	// ConversationID is exposed as SHELLEY_CONVERSATION_ID.
	ConversationID string
	// ConversationSlug is exposed as SHELLEY_CONVERSATION_SLUG.
	ConversationSlug string
	// Model is exposed as SHELLEY_MODEL.
	Model string
	// UserEmail is the exe.dev auth email, exposed as SHELLEY_USER_EMAIL.
	UserEmail string
	// Port is the TCP port the shelley server listens on locally. When >0,
	// SHELLEY_PORT and SHELLEY_URL (http://localhost:<port>) are exported so
	// scripts on the VM can reach the shelley API without the auth proxy.
	Port int
	// DBPath is the path to shelley's SQLite database, exposed as SHELLEY_DB
	// so the agent can read its own conversation history (e.g. resolve
	// [seq:N] pointers from a compaction summary) with sqlite3.
	DBPath string
	// BinDir holds shelley-provided helper scripts (currently
	// shelley-history). When set it is PREPENDED to PATH, so the agent can run
	// them by name. Prepended rather than appended because these are shelley's
	// own tools and a stale same-named binary earlier in PATH would silently
	// shadow them.
	BinDir string
}

// shelleyEnvKeys lists every environment variable name ShelleyEnv may set. We
// strip these from an inherited environment before appending fresh values so a
// stale value from a parent process can't leak through.
var shelleyEnvKeys = []string{
	"SHELLEY_CONVERSATION_ID",
	"SHELLEY_CONVERSATION_SLUG",
	"SHELLEY_MODEL",
	"SHELLEY_USER_EMAIL",
	"SHELLEY_CWD",
	"SHELLEY_GIT_ROOT",
	"SHELLEY_PORT",
	"SHELLEY_URL",
	"SHELLEY_DB",
	// PATH is managed here only when BinDir is set, and the replacement is
	// built from the live PATH, so stripping it first prevents two PATH
	// entries in the same environment.
	"PATH",
}

// Environ returns the SHELLEY_* "KEY=VALUE" entries for this context. cwd is the
// working directory the command will run in; it is used to derive SHELLEY_CWD
// and SHELLEY_GIT_ROOT. Empty values are omitted so hooks can use ordinary
// "is it set?" tests.
func (e ShelleyEnv) Environ(cwd string) []string {
	var out []string
	add := func(k, v string) {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	add("SHELLEY_CONVERSATION_ID", e.ConversationID)
	add("SHELLEY_CONVERSATION_SLUG", e.ConversationSlug)
	add("SHELLEY_MODEL", e.Model)
	add("SHELLEY_USER_EMAIL", e.UserEmail)
	add("SHELLEY_CWD", cwd)
	if cwd != "" {
		if st := gitstate.GetGitState(cwd); st != nil && st.IsRepo && st.Worktree != "" {
			add("SHELLEY_GIT_ROOT", st.Worktree)
		}
	}
	if e.Port > 0 {
		add("SHELLEY_PORT", fmt.Sprintf("%d", e.Port))
		add("SHELLEY_URL", fmt.Sprintf("http://localhost:%d", e.Port))
	}
	add("SHELLEY_DB", e.DBPath)
	// PATH is always re-emitted, because stripShelleyEnv removes it from the
	// inherited environment: returning nothing here would hand the command an
	// environment with no PATH at all. BinDir goes first when set, so shelley's
	// own helpers cannot be shadowed by a same-named binary elsewhere.
	path := os.Getenv("PATH")
	switch {
	case e.BinDir != "" && path != "":
		out = append(out, "PATH="+e.BinDir+string(os.PathListSeparator)+path)
	case e.BinDir != "":
		out = append(out, "PATH="+e.BinDir)
	case path != "":
		out = append(out, "PATH="+path)
	}
	return out
}

// stripShelleyEnv returns env with any ShelleyEnv-managed entries removed. It
// mutates and returns env (like slices.DeleteFunc); pass a copy if the original
// must be preserved.
func stripShelleyEnv(env []string) []string {
	return slices.DeleteFunc(env, func(s string) bool {
		for _, k := range shelleyEnvKeys {
			if strings.HasPrefix(s, k+"=") {
				return true
			}
		}
		return false
	})
}
