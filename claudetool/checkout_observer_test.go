package claudetool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/gitstate"
)

func TestJobCheckoutObservation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@localhost"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "dirty"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	jobs := make(recordingJobs, 1)
	var edits, subjects []string
	b := BashTool{WorkingDir: NewMutableWorkingDir(t.TempDir()), Jobs: jobs,
		OnCheckoutEdit: func(_ context.Context, _ VM, dir string) { edits = append(edits, dir) },
		OnJobCommit:    func(_ context.Context, _ VM, state *gitstate.GitState) { subjects = append(subjects, state.Subject) },
	}
	run := func(command string, background bool, wantError bool) {
		t.Helper()
		out := b.run(t.Context(), bashInput{CWD: root, Command: command, Background: background})
		if (out.Error != nil) != wantError {
			t.Fatalf("%s: %v", command, out.Error)
		}
		if background {
			waitClosed(t, (<-jobs).exited, "job checkout observation")
		}
	}
	run("cat dirty", false, false)
	if len(edits) != 0 {
		t.Fatal("read-only visit marked dirty repo", edits)
	}
	run("printf after >dirty; exit 1", false, true)
	run("printf again >dirty", true, false)
	if !slices.Equal(edits, []string{root, root}) {
		t.Fatalf("checkout edits: %q", edits)
	}
	run("git add dirty && git commit -qm first && git commit --allow-empty -qm second", false, false)
	want := []string{"second"}
	probe := exec.Command("git", "-c", "hook.probe.event=post-commit", "-c", "hook.probe.command=true", "hook", "list", "post-commit")
	probe.Dir = root
	if out, _ := probe.Output(); strings.Contains(string(out), "probe") {
		want = []string{"first", "second"}
	}
	if !slices.Equal(subjects, want) {
		t.Fatalf("commits = %q, want %q", subjects, want)
	}
}
