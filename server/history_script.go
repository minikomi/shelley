package server

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// shelleyHistoryScript is the retrieval helper cited by a compaction summary's
// [seq:N] pointers. It is embedded and written to disk at startup rather than
// documented as a SQL snippet in the prompt, because the query is not something
// a model can be expected to reconstruct: a message's content is a list of
// blocks whose text lives in a different field per block kind, and tool results
// nest theirs one level deeper again. The obvious json_extract on
// Content[0].Text returns blank rows for exactly the tool-heavy messages worth
// reading, which would teach the agent that pointers do not resolve.
//
//go:embed scripts/shelley-history
var shelleyHistoryScript embed.FS

// installHistoryScript writes shelley-history into dir and returns the path to
// it. Rewritten on every startup so an upgraded binary cannot leave a stale
// script behind claiming to speak for it.
func installHistoryScript(dir string) (string, error) {
	body, err := shelleyHistoryScript.ReadFile("scripts/shelley-history")
	if err != nil {
		return "", fmt.Errorf("reading embedded shelley-history: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating script dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "shelley-history")
	if err := os.WriteFile(path, body, 0o755); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// historyScriptDir is where installHistoryScript puts the helper: next to the
// database, which is the one directory shelley already owns and which sits
// outside any workspace the agent might commit.
func historyScriptDir() string {
	if DBPath == "" {
		// No database path (tests): use a temp dir so the script still exists
		// and PATH stays valid.
		return filepath.Join(os.TempDir(), "shelley-bin")
	}
	return filepath.Join(filepath.Dir(DBPath), "bin")
}
