package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallHistoryScript checks the host-installed helper cited by every
// checkpoint summary. The suffix must never point at a nonexistent or
// non-executable command: then [seq:N] looks recoverable but is not.
func TestInstallHistoryScript(t *testing.T) {
	t.Parallel()
	path, err := installHistoryScript(t.TempDir())
	if err != nil {
		t.Fatalf("installHistoryScript: %v", err)
	}
	if filepath.Base(path) != "shelley-history" {
		t.Errorf("script path = %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat installed script: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("installed script is not executable: %v", info.Mode())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed script: %v", err)
	}
	for _, want := range []string{"SHELLEY_DB", "SHELLEY_CONVERSATION_ID", "json_each", "ToolResult"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("installed script no longer handles %q", want)
		}
	}
}
