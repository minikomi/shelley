package claudetool

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"shelley.exe.dev/gitstate"
)

//go:embed checkout_snapshot.sh
var checkoutSnapshot string

// Register an additive hook in this job's process environment. Older Git
// ignores these settings. Existing config pairs and repository hooks coexist.
const configureCommitHook = `
if [[ -n ${SHELLEY_COMMIT_LOG:-} ]]; then
  n=${GIT_CONFIG_COUNT:-0}
  export "GIT_CONFIG_KEY_$n=hook.shelley-job.event" "GIT_CONFIG_VALUE_$n=post-commit"
  ((n+=1))
  export "GIT_CONFIG_KEY_$n=hook.shelley-job.command" "GIT_CONFIG_VALUE_$n=$SHELLEY_COMMIT_OBSERVER"
  export GIT_CONFIG_COUNT=$((n+1))
fi
`

const commitObserver = `root=$(git rev-parse --show-toplevel) &&
branch=$(git symbolic-ref --short -q HEAD || :) &&
oid=$(git rev-parse HEAD) && subject=$(git show -s --format=%s HEAD) &&
printf '%s\0%s\0%s\0%s\0' "$root" "$branch" "$oid" "$subject" >>"$SHELLEY_COMMIT_LOG"`

type jobCheckout struct {
	root, fingerprint, branch, commit, subject string
}

func runObservation(ctx context.Context, vm VM, script string, args ...string) ([]byte, error) {
	argv := append([]string{"bash", "-c", script, "shelley"}, args...)
	if vm != nil {
		argv = vm.Command(script, args...)
	}
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		return nil, fmt.Errorf("observe checkout: %w", err)
	}
	return out, nil
}

func readCheckout(ctx context.Context, vm VM, wd string) (jobCheckout, error) {
	out, err := runObservation(ctx, vm, checkoutSnapshot, wd)
	if err != nil || len(out) == 0 {
		return jobCheckout{}, err
	}
	fields := strings.Split(string(out), "\x00")
	if len(fields) != 6 {
		return jobCheckout{}, fmt.Errorf("invalid checkout snapshot for %s", wd)
	}
	return jobCheckout{fields[0], fields[1], fields[2], fields[3], fields[4]}, nil
}

// observeJob captures the execution machine and directory before launch.
// finish runs before the foreground result or background completion notice.
func (b *BashTool) observeJob(ctx context.Context, vm VM, wd string) ([]string, func(), error) {
	if b.OnCheckoutEdit == nil {
		return nil, func() {}, nil
	}
	before, err := readCheckout(ctx, vm, wd)
	if err != nil {
		return nil, nil, err
	}
	path, err := runObservation(ctx, vm, `mktemp "${TMPDIR:-/tmp}/shelley-commits.XXXXXXXXXX"`)
	if err != nil {
		return nil, nil, err
	}
	logPath := strings.TrimSuffix(string(path), "\n")
	finish := func() {
		ctx := context.WithoutCancel(ctx)
		out, err := runObservation(ctx, vm, `cat -- "$1" && rm -- "$1"`, logPath)
		if err != nil {
			slog.ErrorContext(ctx, "read job commit events", "error", err)
		}
		parts := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
		observed := make(map[string]string)
		for len(parts) >= 4 {
			root, branch, oid, subject := string(parts[0]), string(parts[1]), string(parts[2]), string(parts[3])
			parts = parts[4:]
			observed[root] = oid
			b.reportJobCommit(ctx, vm, root, branch, oid, subject)
		}
		after, err := readCheckout(ctx, vm, wd)
		if err != nil {
			slog.ErrorContext(ctx, "inspect finished job checkout", "error", err)
			return
		}
		if after.root == "" {
			return
		}
		if after.commit != "" && before.commit != after.commit && (before.root == "" || before.branch == after.branch) && observed[after.root] != after.commit {
			b.reportJobCommit(ctx, vm, after.root, after.branch, after.commit, after.subject)
		} else if after.fingerprint != before.fingerprint && (before.root != "" || after.fingerprint != "") {
			b.OnCheckoutEdit(ctx, vm, after.root)
		}
	}
	return []string{"SHELLEY_COMMIT_LOG=" + logPath, "SHELLEY_COMMIT_OBSERVER=" + commitObserver}, finish, nil
}

func (b *BashTool) reportJobCommit(ctx context.Context, vm VM, root, branch, oid, subject string) {
	if vm == nil && b.GitTracker != nil {
		b.GitTracker.AcknowledgeCommit(root, oid)
	}
	if b.OnJobCommit != nil {
		b.OnJobCommit(ctx, vm, &gitstate.GitState{IsRepo: true, Worktree: root, Branch: branch, Commit: oid, Subject: subject})
	}
}
