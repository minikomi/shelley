package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUIDirServesFromDisk verifies the SHELLEY_UI_DIR development path: assets
// come from a directory per request rather than from the binary, and no
// checksums are reported so the server cannot serve a 304 for a file that has
// since been rebuilt. Both properties are what make an asset rebuild visible on
// reload without a Go build or a restart.
//
// The package reads the variable in init(), so this runs in a subprocess.
func TestUIDirServesFromDisk(t *testing.T) {
	if os.Getenv("SHELLEY_UI_DIR_TEST_CHILD") != "1" {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("from-disk"), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=TestUIDirServesFromDisk", "-test.v")
		cmd.Env = append(os.Environ(), "SHELLEY_UI_DIR_TEST_CHILD=1", UIDirEnv+"="+dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("subprocess failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "PASS") {
			t.Fatalf("subprocess did not pass:\n%s", out)
		}
		return
	}

	dir := os.Getenv(UIDirEnv)
	if DevDir() != dir {
		t.Fatalf("DevDir() = %q, want %q", DevDir(), dir)
	}

	// The probe file exists only on disk, never in the embedded filesystem.
	f, err := Assets().Open("/probe.txt")
	if err != nil {
		t.Fatalf("opening probe.txt from %s: %v", dir, err)
	}
	defer f.Close()
	buf := make([]byte, 32)
	n, _ := f.Read(buf)
	if got := string(buf[:n]); got != "from-disk" {
		t.Errorf("probe.txt = %q, want %q", got, "from-disk")
	}

	// A file written after startup is served too: assets are read per request,
	// not snapshotted at init.
	if err := os.WriteFile(filepath.Join(dir, "later.txt"), []byte("later"), 0o644); err != nil {
		t.Fatal(err)
	}
	later, err := Assets().Open("/later.txt")
	if err != nil {
		t.Fatalf("file created after startup should be served: %v", err)
	}
	later.Close()

	if Checksums() != nil {
		t.Error("Checksums() must be nil when serving from disk, or ETags can pin a stale build in the browser")
	}
}

// TestEmbeddedByDefault verifies the normal path is unaffected: with no
// SHELLEY_UI_DIR set, assets come from the binary and checksums are available
// for ETags.
func TestEmbeddedByDefault(t *testing.T) {
	if os.Getenv(UIDirEnv) != "" {
		t.Skipf("%s is set in this environment", UIDirEnv)
	}
	if DevDir() != "" {
		t.Errorf("DevDir() = %q, want empty", DevDir())
	}
	if Checksums() == nil {
		t.Error("Checksums() should be populated for the embedded build")
	}
}
