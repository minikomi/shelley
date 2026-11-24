package claudetool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/gitstate"
)

func TestJobCheckoutObservation(t *testing.T) {
	for _, tc := range []struct {
		name string
		vm   VM
	}{{"local", nil}, {"vm", localVM{}}} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			git := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-q", "-b", "main")
			git("config", "user.name", "Test")
			git("config", "user.email", "test@localhost")
			git("commit", "-qm", "original", "--allow-empty")
			legacyHook := filepath.Join(root, ".git", "hooks", "post-commit")
			if err := os.WriteFile(legacyHook, []byte("#!/bin/sh\nprintf x >>local-hook-ran\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			probe := exec.Command("git", "-C", root, "-c", "hook.probe.event=post-commit", "-c", "hook.probe.command=true", "hook", "list", "post-commit")
			out, _ := probe.Output()
			supportsHook := strings.Contains(string(out), "probe")

			workingDir := NewMutableWorkingDir(t.TempDir())
			jobs := make(recordingJobs, 1)
			var states []gitstate.GitState
			b := BashTool{
				WorkingDir: workingDir, HostDir: workingDir, VM: tc.vm, Jobs: jobs,
				OnJobCommit: func(_ context.Context, vm VM, state *gitstate.GitState) {
					if vm != tc.vm {
						t.Errorf("commit machine = %v, want %v", vm, tc.vm)
					}
					states = append(states, *state)
				},
				CommandEnv: func(context.Context) ([]string, error) {
					return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=observation.marker", "GIT_CONFIG_VALUE_0=present"}, nil
				},
			}
			var target *string
			if tc.vm != nil {
				name := tc.vm.Name()
				target = &name
			}
			run := func(ctx context.Context, cwd, command string, background bool) {
				t.Helper()
				result := b.run(ctx, bashInput{CWD: cwd, Command: command, Background: background, SSHTarget: target})
				if result.Error != nil {
					t.Fatalf("%s: %v", command, result.Error)
				}
			}
			run(t.Context(), root, "printf dirt >untracked && git status --short", false)
			run(t.Context(), root, "test \"$(git config observation.marker)\" = present && git commit --allow-empty -qm first && git commit --allow-empty -qm second", false)
			second := git("rev-parse", "HEAD")
			run(t.Context(), root, "git reset --hard HEAD^", false)
			first := git("rev-parse", "HEAD")
			g := newGate(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			run(ctx, "", fmt.Sprintf("cd %q && %s && git commit --allow-empty -qm later", root, g.wait()), true)
			bg := <-jobs
			bg.cleanup(t)
			workingDir.Set(t.TempDir())
			cancel()
			g.release(t)
			waitClosed(t, bg.exited, "background commit observation")
			later := git("rev-parse", "HEAD")
			if status := bg.job.Outcome().ExitCode; status == nil || *status != 0 {
				t.Fatalf("background command failed: %s", bg.job.Outcome().Notice())
			}

			var subjects, commits []string
			for _, state := range states {
				if state.Worktree != root || state.Branch != "main" {
					t.Errorf("commit checkout = %s (%s), want %s (main)", state.Worktree, state.Branch, root)
				}
				subjects = append(subjects, state.Subject)
				commits = append(commits, state.FullCommit)
			}
			var wantSubjects, wantCommits []string
			if supportsHook {
				wantSubjects = []string{"first", "second", "later"}
				wantCommits = []string{first, second, later}
			}
			if !slices.Equal(subjects, wantSubjects) || !slices.Equal(commits, wantCommits) {
				t.Errorf("commit events = %q %q, want %q %q", subjects, commits, wantSubjects, wantCommits)
			}
			if data, err := os.ReadFile(filepath.Join(root, "local-hook-ran")); err != nil || string(data) != "xxx" {
				t.Errorf("existing post-commit hook ran %q: %v", data, err)
			}
		})
	}
}

type failingCommitObservationVM struct {
	localVM
	failScript string
	logPath    string
	called     bool
}

func (v *failingCommitObservationVM) Command(script string, args ...string) []string {
	switch {
	case script == v.failScript:
		v.called = true
		return []string{"false"}
	case script == createCommitLog:
		return []string{"printf", "%s\n", v.logPath}
	default:
		return v.localVM.Command(script, args...)
	}
}

func TestJobCheckoutObservationFailurePreservesExit(t *testing.T) {
	for name, script := range map[string]string{"setup": createCommitLog, "collection": readCommitLog} {
		t.Run(name, func(t *testing.T) {
			wd := NewMutableWorkingDir(t.TempDir())
			vm := &failingCommitObservationVM{failScript: script, logPath: filepath.Join(t.TempDir(), "events")}
			target := vm.Name()
			b := BashTool{WorkingDir: wd, HostDir: wd, VM: vm, OnJobCommit: func(context.Context, VM, *gitstate.GitState) {
				t.Error("observed a commit after observation failure")
			}}
			result := b.run(t.Context(), bashInput{Command: "printf marker; exit 17", SSHTarget: &target})
			if !vm.called {
				t.Fatal("observation stage did not run")
			}
			if result.Error == nil || !strings.Contains(result.Error.Error(), "marker") {
				t.Fatalf("lost command output: %v", result.Error)
			}
			display := result.Display.(BashDisplayData)
			if display.ExitCode == nil || *display.ExitCode != 17 {
				t.Errorf("lost command exit status: %+v", display)
			}
		})
	}
}
