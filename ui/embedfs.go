package ui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Dist contains the contents of the built UI under dist/.
//
//go:embed dist/*
var Dist embed.FS

var assets http.FileSystem

// devDir is the directory assets are served from when SHELLEY_UI_DIR is set,
// or "" when assets come from the embedded filesystem.
var devDir string

// DevDir reports the directory UI assets are being served from live, or ""
// when they come from the binary. Callers use this to skip caching that would
// otherwise pin a stale build in the browser.
func DevDir() string { return devDir }

// UIDirEnv names the environment variable that points at a UI build directory
// on disk. Setting it makes the server read every asset from that directory
// per request instead of from the binary, so an esbuild watch can rebuild
// ui/dist and a browser reload picks the change up with no Go build and no
// server restart. Development only: an unset variable is the normal embedded
// path.
const UIDirEnv = "SHELLEY_UI_DIR"

func init() {
	if dir := os.Getenv(UIDirEnv); dir != "" {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			fmt.Fprintf(os.Stderr, "\nError: %s=%s is not a directory.\n\n", UIDirEnv, dir)
			os.Exit(1)
		}
		devDir = dir
		assets = http.Dir(dir)
		// No staleness check: serving from disk is what makes a rebuilt
		// directory take effect, so "sources newer than the build" is the
		// expected steady state rather than an error.
		return
	}

	sub, err := fs.Sub(Dist, "dist")
	if err != nil {
		// If the build is misconfigured and dist/ is missing, fail fast.
		panic(err)
	}
	assets = http.FS(sub)

	// Check if UI sources are stale compared to the embedded build
	checkStaleness()
}

// checkStaleness verifies that the embedded UI build is not stale.
// If ui/src exists and has files modified after the build, we exit with an error.
func checkStaleness() {
	// Read build-info.json from embedded filesystem
	buildInfoData, err := fs.ReadFile(Dist, "dist/build-info.json")
	if err != nil {
		// If build-info.json doesn't exist, the build is old or incomplete.
		fmt.Fprintf(os.Stderr, "\nError: UI build is stale!\n")
		fmt.Fprintf(os.Stderr, "\nPlease run 'make serve' instead of 'go run ./cmd/shelley serve'\n")
		fmt.Fprintf(os.Stderr, "Or rebuild the UI first: cd ui && pnpm run build\n\n")
		os.Exit(1)
		return
	}

	var buildInfo struct {
		Timestamp int64  `json:"timestamp"`
		Date      string `json:"date"`
		SrcDir    string `json:"srcDir"`
	}
	if err := json.Unmarshal(buildInfoData, &buildInfo); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to parse build-info.json: %v\n", err)
		return
	}

	// Allow a small grace window. Filesystem mtime granularity is at most
	// 1s on NFS/older extN, and CI ordering between git checkout and the
	// in-process Date.now() captured by the UI build script can land a
	// freshly-built source a sub-second after that timestamp even though
	// nothing actually edited it. 5s comfortably covers both without
	// hiding genuinely stale builds.
	const staleSlack = 5 * time.Second
	buildTime := time.UnixMilli(buildInfo.Timestamp).Add(staleSlack)

	// Check if source directory exists (we might be in a deployed binary without source)
	srcDir := buildInfo.SrcDir
	if srcDir == "" {
		// Build info doesn't have srcDir, can't check staleness
		return
	}
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		// Source directory doesn't exist, assume we're in production/deployed
		return
	}

	// Walk through ui/src and check if any files are newer than the build
	var newerFiles []string
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.ModTime().After(buildTime) {
			newerFiles = append(newerFiles, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to check source file timestamps: %v\n", err)
		return
	}

	if len(newerFiles) > 0 {
		fmt.Fprintf(os.Stderr, "\nError: UI build is stale!\n")
		fmt.Fprintf(os.Stderr, "Build timestamp: %s\n", buildInfo.Date)
		fmt.Fprintf(os.Stderr, "\nThe following source files are newer than the build:\n")
		for _, f := range newerFiles {
			fmt.Fprintf(os.Stderr, "  - %s\n", f)
		}
		fmt.Fprintf(os.Stderr, "\nPlease run 'make serve' instead of 'go run ./cmd/shelley serve'\n")
		fmt.Fprintf(os.Stderr, "Or rebuild the UI first: cd ui && pnpm run build\n\n")
		os.Exit(1)
	}
}

// Assets returns an http.FileSystem backed by the embedded UI assets.
func Assets() http.FileSystem {
	return assets
}

// Checksums returns the content checksums for static assets.
// These are computed during build and used for ETag generation.
func Checksums() map[string]string {
	// Serving from disk means the bytes change under the server as the watch
	// rebuilds. Returning no checksums drops ETags, so the browser cannot 304
	// its way into showing the previous build.
	if devDir != "" {
		return nil
	}
	data, err := fs.ReadFile(Dist, "dist/checksums.json")
	if err != nil {
		return nil
	}
	var checksums map[string]string
	if err := json.Unmarshal(data, &checksums); err != nil {
		return nil
	}
	return checksums
}
