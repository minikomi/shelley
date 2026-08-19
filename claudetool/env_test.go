package claudetool

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestShelleyEnvEnviron(t *testing.T) {
	e := ShelleyEnv{
		ConversationID:   "conv1",
		ConversationSlug: "happy-otter",
		Model:            "claude",
		UserEmail:        "a@b.com",
		Port:             8123,
	}
	got := e.Environ("") // no cwd -> no SHELLEY_CWD / SHELLEY_GIT_ROOT
	want := []string{
		"SHELLEY_CONVERSATION_ID=conv1",
		"SHELLEY_CONVERSATION_SLUG=happy-otter",
		"SHELLEY_MODEL=claude",
		"SHELLEY_USER_EMAIL=a@b.com",
		"SHELLEY_PORT=8123",
		"SHELLEY_URL=http://localhost:8123",
	}
	for _, entry := range want {
		if !slices.Contains(got, entry) {
			t.Errorf("Environ() missing %q in %v", entry, got)
		}
	}
	if !slices.ContainsFunc(got, func(entry string) bool { return strings.HasPrefix(entry, "PATH=") }) {
		t.Errorf("Environ() did not restore PATH after stripShelleyEnv removed it: %v", got)
	}
}

func TestShelleyEnvOmitsEmpty(t *testing.T) {
	got := ShelleyEnv{ConversationID: "x"}.Environ("")
	if !slices.Contains(got, "SHELLEY_CONVERSATION_ID=x") {
		t.Fatalf("missing conversation id: %v", got)
	}
	if len(got) != 2 || !slices.ContainsFunc(got, func(entry string) bool { return strings.HasPrefix(entry, "PATH=") }) {
		t.Fatalf("expected only conversation id plus PATH, got %v", got)
	}
}

func TestShelleyEnvPrependsBinDir(t *testing.T) {
	got := ShelleyEnv{BinDir: "/shelley/bin"}.Environ("")
	if !slices.ContainsFunc(got, func(entry string) bool {
		return strings.HasPrefix(entry, "PATH=/shelley/bin:")
	}) {
		t.Errorf("BinDir did not lead PATH: %v", got)
	}
}

func TestShelleyEnvCwdAndGitRoot(t *testing.T) {
	// Use the repo itself as a known git worktree.
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not in a git repo: %v", err)
	}
	dir := strings.TrimSpace(string(root))
	got := ShelleyEnv{}.Environ(dir)
	if !slices.Contains(got, "SHELLEY_CWD="+dir) {
		t.Errorf("missing SHELLEY_CWD=%s in %v", dir, got)
	}
	if !slices.Contains(got, "SHELLEY_GIT_ROOT="+dir) {
		t.Errorf("missing SHELLEY_GIT_ROOT=%s in %v", dir, got)
	}
}

func TestStripShelleyEnv(t *testing.T) {
	in := []string{
		"PATH=/bin",
		"SHELLEY_CONVERSATION_ID=old",
		"SHELLEY_URL=http://localhost:1",
		"HOME=/root",
		"SHELLEY_GIT_ROOT=/x",
	}
	got := stripShelleyEnv(slices.Clone(in))
	// PATH is Shelley-managed now: ShelleyEnv.Environ re-emits it, optionally
	// with the shelley-history helper directory prepended. Keeping the inherited
	// PATH here would leave two PATH entries when environments are joined.
	want := []string{"HOME=/root"}
	if !slices.Equal(got, want) {
		t.Fatalf("stripShelleyEnv = %v, want %v", got, want)
	}
}
