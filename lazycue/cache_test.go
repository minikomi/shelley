package lazycue

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initBareRepo creates a bare git repo (to act as "origin") and a clone of it.
// Returns (clone dir, bare dir, cleanup func).
func initBareRepo(t *testing.T) (string, string, func()) {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "bare.git")
	clone := filepath.Join(dir, "clone")

	run := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		cmd.Env = append(
			os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), d, err, out)
		}
	}

	os.MkdirAll(bare, 0o755)
	run(bare, "init", "--bare")
	run(dir, "clone", bare, "clone")

	// Create an initial commit so HEAD exists.
	os.WriteFile(filepath.Join(clone, "README"), []byte("init"), 0o644)
	run(clone, "add", ".")
	run(clone, "commit", "-m", "init")
	run(clone, "push", "origin", "main")

	return clone, bare, func() {}
}

func getHEAD(t *testing.T, dir string) string {
	t.Helper()
	out, err := gitExec(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func makeCommit(t *testing.T, dir, msg string) string {
	t.Helper()
	file := filepath.Join(dir, msg)
	os.WriteFile(file, []byte(msg), 0o644)
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	cmd.Env = append(
		os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", msg)
	cmd.Dir = dir
	cmd.Env = append(
		os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	return getHEAD(t, dir)
}

func TestParseRefs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []parsedRef
	}{
		{
			name:  "legacy format",
			input: "refs/lazycue/abc123/v1-f3a9b2\nrefs/lazycue/abc123/v2-deadbe",
			want: []parsedRef{
				{ref: "refs/lazycue/abc123/v2-deadbe", commit: "", version: 2},
				{ref: "refs/lazycue/abc123/v1-f3a9b2", commit: "", version: 1},
			},
		},
		{
			name:  "new format with commit",
			input: "refs/lazycue/abc123/" + strings.Repeat("a", 40) + "/v1-f3a9b2",
			want: []parsedRef{
				{ref: "refs/lazycue/abc123/" + strings.Repeat("a", 40) + "/v1-f3a9b2", commit: strings.Repeat("a", 40), version: 1},
			},
		},
		{
			name: "mixed legacy and new",
			input: "refs/lazycue/abc123/v1-f3a9b2\n" +
				"refs/lazycue/abc123/" + strings.Repeat("b", 40) + "/v2-cafe01",
			want: []parsedRef{
				{ref: "refs/lazycue/abc123/" + strings.Repeat("b", 40) + "/v2-cafe01", commit: strings.Repeat("b", 40), version: 2},
				{ref: "refs/lazycue/abc123/v1-f3a9b2", commit: "", version: 1},
			},
		},
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRefs(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseRefs returned %d refs, want %d", len(got), len(tt.want))
			}
			for i, g := range got {
				w := tt.want[i]
				if g.ref != w.ref || g.commit != w.commit || g.version != w.version {
					t.Errorf("ref[%d] = {ref:%q commit:%q version:%d}, want {ref:%q commit:%q version:%d}",
						i, g.ref, g.commit, g.version, w.ref, w.commit, w.version)
				}
			}
		})
	}
}

func TestExtractDescHash(t *testing.T) {
	tests := []struct {
		ref  string
		want string
	}{
		{"refs/lazycue/abc123def456/v1-f3a9b2", "abc123def456"},
		{"refs/lazycue/abc123def456/" + strings.Repeat("a", 40) + "/v1-f3a9b2", "abc123def456"},
		{"refs/other/abc", ""},
		{"refs/lazycue/", ""},
	}
	for _, tt := range tests {
		got := extractDescHash(tt.ref)
		if got != tt.want {
			t.Errorf("extractDescHash(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestSaveCachedTestNoPush(t *testing.T) {
	clone, _, _ := initBareRepo(t)
	commit := getHEAD(t, clone)

	desc := "test save no push"
	steps := []byte(`[{"action":"navigate","url":"/new"}]`)
	meta := &CacheMetadata{Mode: "generated"}

	// Save without push.
	if err := SaveCachedTest(clone, "origin", desc, steps, 1, meta, commit, false); err != nil {
		t.Fatal(err)
	}

	// Should be findable locally.
	got, hit, err := GetCachedTest(clone, "origin", desc, commit)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Version != 1 {
		t.Fatalf("version = %d, want 1", hit.Version)
	}
	if got == nil {
		t.Fatal("expected cached test, got nil")
	}

	// Should NOT be on origin (check the bare repo has no refs).
	refs, _ := gitExec(clone, "ls-remote", "origin", "refs/lazycue/*")
	if refs != "" {
		t.Fatalf("expected no remote refs, got: %s", refs)
	}
}

func TestSaveCachedTestWithPush(t *testing.T) {
	clone, _, _ := initBareRepo(t)
	commit := getHEAD(t, clone)

	desc := "test save with push"
	steps := []byte(`[{"action":"navigate","url":"/new"}]`)
	meta := &CacheMetadata{Mode: "generated"}

	if err := SaveCachedTest(clone, "origin", desc, steps, 1, meta, commit, true); err != nil {
		t.Fatal(err)
	}

	// Should be on origin.
	refs, _ := gitExec(clone, "ls-remote", "origin", "refs/lazycue/*")
	if refs == "" {
		t.Fatal("expected remote refs after push")
	}
}

func TestAncestryFiltering(t *testing.T) {
	clone, _, _ := initBareRepo(t)

	// Create a linear history: A -> B -> C
	commitA := getHEAD(t, clone) // "init" commit
	commitB := makeCommit(t, clone, "second")
	commitC := makeCommit(t, clone, "third")

	desc := "ancestry test"
	steps := []byte(`[{"action":"navigate","url":"/new"}]`)

	// Save cache at commit A.
	if err := SaveCachedTest(clone, "origin", desc, steps, 1, &CacheMetadata{Mode: "generated"}, commitA, false); err != nil {
		t.Fatal(err)
	}

	// Cache at A should be visible from C (A is ancestor of C).
	got, hit, err := GetCachedTest(clone, "origin", desc, commitC)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || hit.Version != 1 {
		t.Fatalf("expected v1 from commitC, got ver=%d got=%v", hit.Version, got)
	}

	// Save cache at commit C (v2).
	steps2 := []byte(`[{"action":"navigate","url":"/updated"}]`)
	if err := SaveCachedTest(clone, "origin", desc, steps2, 2, &CacheMetadata{Mode: "healed"}, commitC, false); err != nil {
		t.Fatal(err)
	}

	// From C, should see v2 now (highest version among ancestors).
	got, hit, err = GetCachedTest(clone, "origin", desc, commitC)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Version != 2 {
		t.Fatalf("expected v2 from commitC, got v%d", hit.Version)
	}

	// From B, should still see v1 (C is not ancestor of B).
	got, hit, err = GetCachedTest(clone, "origin", desc, commitB)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Version != 1 {
		t.Fatalf("expected v1 from commitB, got v%d", hit.Version)
	}

	_ = got
}

func TestAncestryBranching(t *testing.T) {
	clone, _, _ := initBareRepo(t)

	// Create: init -> A (main) -> B (branch)
	//                           \ C (another branch)
	commitInit := getHEAD(t, clone)
	commitA := makeCommit(t, clone, "a")

	// Branch from A.
	cmd := exec.Command("git", "checkout", "-b", "branch1")
	cmd.Dir = clone
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v\n%s", err, out)
	}
	commitB := makeCommit(t, clone, "b")

	// Go back to A, make a second branch.
	cmd = exec.Command("git", "checkout", "main")
	cmd.Dir = clone
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "checkout", "-b", "branch2")
	cmd.Dir = clone
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v\n%s", err, out)
	}
	commitC := makeCommit(t, clone, "c")

	desc := "branching test"
	steps := []byte(`[{"action":"navigate","url":"/new"}]`)

	// Save cache at commitA (common ancestor).
	if err := SaveCachedTest(clone, "origin", desc, steps, 1, &CacheMetadata{Mode: "generated"}, commitA, false); err != nil {
		t.Fatal(err)
	}

	// Both branches should see it.
	_, hitB, _ := GetCachedTest(clone, "origin", desc, commitB)
	_, hitC, _ := GetCachedTest(clone, "origin", desc, commitC)
	if hitB.Version != 1 || hitC.Version != 1 {
		t.Fatalf("both branches should see v1: branch1=%d branch2=%d", hitB.Version, hitC.Version)
	}

	// Save branch-specific cache at commitB (v2).
	steps2 := []byte(`[{"action":"navigate","url":"/branch1"}]`)
	if err := SaveCachedTest(clone, "origin", desc, steps2, 2, &CacheMetadata{Mode: "healed"}, commitB, false); err != nil {
		t.Fatal(err)
	}

	// Branch1 sees v2, branch2 still sees v1.
	_, hitB, _ = GetCachedTest(clone, "origin", desc, commitB)
	_, hitC, _ = GetCachedTest(clone, "origin", desc, commitC)
	if hitB.Version != 2 {
		t.Fatalf("branch1 should see v2, got v%d", hitB.Version)
	}
	if hitC.Version != 1 {
		t.Fatalf("branch2 should still see v1, got v%d", hitC.Version)
	}

	_ = commitInit
}

func TestLegacyRefsAsFallback(t *testing.T) {
	clone, _, _ := initBareRepo(t)
	commit := getHEAD(t, clone)

	desc := "legacy fallback test"
	steps := []byte(`[{"action":"navigate","url":"/new"}]`)

	// Save in legacy format (no commit).
	if err := SaveCachedTest(clone, "origin", desc, steps, 1, &CacheMetadata{Mode: "generated"}, "", false); err != nil {
		t.Fatal(err)
	}

	// Should be visible from any commit.
	got, hit, err := GetCachedTest(clone, "origin", desc, commit)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || hit.Version != 1 {
		t.Fatalf("expected v1 legacy fallback, got ver=%d", hit.Version)
	}
}

func TestPromoteRefs(t *testing.T) {
	clone, _, _ := initBareRepo(t)
	commit := getHEAD(t, clone)

	desc := "promote test"
	steps := []byte(`[{"action":"navigate","url":"/new"}]`)

	// Save locally without push.
	if err := SaveCachedTest(clone, "origin", desc, steps, 1, &CacheMetadata{Mode: "generated"}, commit, false); err != nil {
		t.Fatal(err)
	}

	// Verify not on remote.
	refs, _ := gitExec(clone, "ls-remote", "origin", "refs/lazycue/*")
	if refs != "" {
		t.Fatal("expected no remote refs before promote")
	}

	// Promote.
	pushed, err := PromoteRefs(clone, "origin", "")
	if err != nil {
		t.Fatal(err)
	}
	if pushed != 1 {
		t.Fatalf("pushed = %d, want 1", pushed)
	}

	// Verify on remote.
	refs, _ = gitExec(clone, "ls-remote", "origin", "refs/lazycue/*")
	if refs == "" {
		t.Fatal("expected remote refs after promote")
	}
}

func TestPromoteRefsWithCommitRetag(t *testing.T) {
	clone, _, _ := initBareRepo(t)
	commitOld := getHEAD(t, clone)
	commitNew := makeCommit(t, clone, "new-commit")

	desc := "promote retag test"
	steps := []byte(`[{"action":"navigate","url":"/new"}]`)

	// Save at old commit, locally.
	if err := SaveCachedTest(clone, "origin", desc, steps, 1, &CacheMetadata{Mode: "generated"}, commitOld, false); err != nil {
		t.Fatal(err)
	}

	// Promote with new commit — should re-file under new commit.
	pushed, err := PromoteRefs(clone, "origin", commitNew)
	if err != nil {
		t.Fatal(err)
	}
	if pushed < 1 {
		t.Fatalf("expected at least 1 pushed, got %d", pushed)
	}

	// Fetch back and verify the cache is now accessible from commitNew.
	if err := FetchCachedRefs(clone, "origin"); err != nil {
		t.Fatal(err)
	}

	got, hit, err := GetCachedTest(clone, "origin", desc, commitNew)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected cached test after promote with retag")
	}
	if hit.Version != 1 {
		t.Fatalf("version = %d, want 1", hit.Version)
	}

	_ = fmt.Sprintf("commits: %s %s", commitOld, commitNew)
}

func TestParseOneRef(t *testing.T) {
	commit40 := strings.Repeat("a", 40)
	tests := []struct {
		ref     string
		version int
		commit  string
		nil_    bool
	}{
		{"refs/lazycue/abc123/v1-f3a9b2", 1, "", false},
		{"refs/lazycue/abc123/v3-deadbe", 3, "", false},
		{"refs/lazycue/abc123/" + commit40 + "/v2-cafe01", 2, commit40, false},
		{"refs/lazycue/abc123/not-a-version", 0, "", true},
	}
	for _, tt := range tests {
		got := parseOneRef(tt.ref)
		if tt.nil_ {
			if got != nil {
				t.Errorf("parseOneRef(%q) = %+v, want nil", tt.ref, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("parseOneRef(%q) = nil, want non-nil", tt.ref)
		}
		if got.version != tt.version {
			t.Errorf("parseOneRef(%q).version = %d, want %d", tt.ref, got.version, tt.version)
		}
		if got.commit != tt.commit {
			t.Errorf("parseOneRef(%q).commit = %q, want %q", tt.ref, got.commit, tt.commit)
		}
	}
}

func TestStepSummary(t *testing.T) {
	tests := []struct {
		step Step
		want string
	}{
		{Step{Action: "navigate", URL: "/new"}, "navigate /new"},
		{Step{Action: "click", Selector: "#btn"}, "click #btn"},
		{Step{Action: "fill", Selector: "input", Value: "hello"}, "fill input hello"},
		{Step{Action: "wait_visible", Selector: ".loading"}, "wait_visible .loading"},
		{Step{Action: "wait_text", Text: "Hello world"}, "wait_text Hello world"},
		{Step{Action: "assert_text", Selector: "h1", Text: "Title"}, "assert_text h1 Title"},
		{Step{Action: "press_key", Key: "Enter"}, "press_key Enter"},
		{Step{Action: "screenshot"}, "screenshot"},
		{Step{Action: "eval", Expression: "document.title"}, "eval document.title"},
		{Step{Action: "eval", Expression: "1+1", Expect: "2"}, "eval 1+1 expect=2"},
		{Step{Action: "assert_count", Selector: "li", Count: 3}, "assert_count li 3"},
		{Step{Action: "sleep", Timeout: "1s"}, "sleep 1s"},
	}
	for _, tt := range tests {
		got := StepSummary(tt.step)
		if got != tt.want {
			t.Errorf("StepSummary(%+v) = %q, want %q", tt.step, got, tt.want)
		}
	}
}
