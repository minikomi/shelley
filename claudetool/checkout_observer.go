package claudetool

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"shelley.exe.dev/gitstate"
)

// Existing config pairs and repository hooks coexist with the job hook.
const configureCommitHook = `
if [[ -n ${SHELLEY_COMMIT_LOG:-} ]]; then
  n=${GIT_CONFIG_COUNT:-0}
  export "GIT_CONFIG_KEY_$n=hook.shelley-job.event" "GIT_CONFIG_VALUE_$n=post-commit"
  ((n+=1))
  export "GIT_CONFIG_KEY_$n=hook.shelley-job.command" "GIT_CONFIG_VALUE_$n=$SHELLEY_COMMIT_OBSERVER"
  export GIT_CONFIG_COUNT=$((n+1))
fi
`

const commitObserver = `(
[ -f "$SHELLEY_COMMIT_LOG" ] &&
root=$(git rev-parse --show-toplevel) &&
branch=$(git symbolic-ref --short -q HEAD || :) &&
oid=$(git rev-parse HEAD) && subject=$(git show -s --format=%s HEAD) &&
printf '%s\0%s\0%s\0%s\0' "$root" "$branch" "$oid" "$subject" >>"$SHELLEY_COMMIT_LOG"
) 2>/dev/null || :`

// Git 2.54 added config-based hooks.
const createCommitLog = `
[[ $(git version) =~ ^git\ version\ ([0-9]+)\.([0-9]+) ]] &&
  (( BASH_REMATCH[1] > 2 || (BASH_REMATCH[1] == 2 && BASH_REMATCH[2] >= 54) )) || exit 0
exec mktemp "${TMPDIR:-/tmp}/shelley-commits.XXXXXXXXXX"
`

const readCommitLog = `
if [[ -f $1 ]]; then head -c 1048576 -- "$1"; fi
s=$?
rm -f -- "$1"
exit "$s"
`

const commitObservationTimeout = 2 * time.Second

func runObservation(ctx context.Context, vm VM, script string, args ...string) ([]byte, error) {
	argv := append([]string{"bash", "-c", script, "shelley"}, args...)
	if vm != nil {
		argv = vm.Command(script, args...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = 100 * time.Millisecond
	return cmd.Output()
}

// observeJob captures the execution machine before launch. The hook records
// whichever checkout git commits in, even if the job changes directories.
func (b *BashTool) observeJob(ctx context.Context, vm VM) ([]string, func()) {
	if b.OnJobCommit == nil {
		return nil, func() {}
	}
	setupCtx, cancel := context.WithTimeout(ctx, commitObservationTimeout)
	path, err := runObservation(setupCtx, vm, createCommitLog)
	cancel()
	if err != nil {
		slog.DebugContext(ctx, "create job commit log", "error", err)
		return nil, func() {}
	}
	logPath := strings.TrimSuffix(string(path), "\n")
	if !filepath.IsAbs(logPath) {
		return nil, func() {}
	}
	finish := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitObservationTimeout)
		defer cancel()
		out, err := runObservation(ctx, vm, readCommitLog, logPath)
		if err != nil {
			slog.DebugContext(ctx, "read job commit events", "error", err)
			return
		}
		parts := strings.Split(string(out), "\x00")
		for len(parts) > 4 && ctx.Err() == nil {
			root, branch, oid, subject := parts[0], parts[1], parts[2], parts[3]
			parts = parts[4:]
			if root != "" && oid != "" {
				b.reportJobCommit(ctx, vm, root, branch, oid, subject)
			}
		}
	}
	return []string{"SHELLEY_COMMIT_LOG=" + logPath, "SHELLEY_COMMIT_OBSERVER=" + commitObserver}, finish
}

func (b *BashTool) reportJobCommit(ctx context.Context, vm VM, root, branch, oid, subject string) {
	if vm == nil && b.GitTracker != nil {
		b.GitTracker.AcknowledgeCommit(root, oid)
	}
	b.OnJobCommit(ctx, vm, &gitstate.GitState{IsRepo: true, Worktree: root, Branch: branch, Commit: oid, FullCommit: oid, Subject: subject})
}
