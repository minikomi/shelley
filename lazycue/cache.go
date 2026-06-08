package lazycue

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CachedTest is the JSON wrapper stored in git blobs.
type CachedTest struct {
	Steps    json.RawMessage `json:"steps"`
	Metadata *CacheMetadata  `json:"metadata,omitempty"`
}

// CacheMetadata holds provenance information about a cached test.
type CacheMetadata struct {
	CreatedAt        time.Time `json:"created_at"`
	Hostname         string    `json:"hostname"`
	Model            string    `json:"model"`
	InputTokens      int       `json:"input_tokens"`
	OutputTokens     int       `json:"output_tokens"`
	EstimatedCostUSD float64   `json:"estimated_cost_usd"`
	CIRun            string    `json:"ci_run,omitempty"`
	GitSHA           string    `json:"git_sha,omitempty"`
	Mode             string    `json:"mode"`
}

// randomHex returns n random hex characters.
func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// detectCIRun returns the CI build URL from common CI env vars, or empty string.
func detectCIRun() string {
	if u := os.Getenv("BUILDKITE_BUILD_URL"); u != "" {
		return u
	}
	server := os.Getenv("GITHUB_SERVER_URL")
	repo := os.Getenv("GITHUB_REPOSITORY")
	runID := os.Getenv("GITHUB_RUN_ID")
	if server != "" && repo != "" && runID != "" {
		return server + "/" + repo + "/actions/runs/" + runID
	}
	if u := os.Getenv("CI_JOB_URL"); u != "" {
		return u
	}
	return ""
}

// detectGitSHA returns the current HEAD commit SHA, or empty string on error.
func detectGitSHA(repoRoot string) string {
	sha, err := gitExec(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return sha
}

// DescriptionHash returns the first 16 hex chars of the SHA-256 hash of a description.
func DescriptionHash(description string) string {
	h := sha256.Sum256([]byte(description))
	return fmt.Sprintf("%x", h[:8])
}

func refPrefix(description string) string {
	return "refs/lazycue/" + DescriptionHash(description)
}

// gitExec runs a git command in the given directory and returns trimmed stdout.
func gitExec(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, string(ee.Stderr))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitExecStdin runs a git command with stdin input.
func gitExecStdin(dir, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, string(ee.Stderr))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RemoteURL resolves a git remote name to its URL.
func RemoteURL(repoRoot, remote string) string {
	url, err := gitExec(repoRoot, "remote", "get-url", remote)
	if err != nil {
		return remote
	}
	return url
}

// FetchCachedRefs fetches all lazycue refs from the remote.
func FetchCachedRefs(repoRoot, remote string) error {
	_, err := gitExec(repoRoot, "fetch", remote, "+refs/lazycue/*:refs/lazycue/*")
	return err
}

// isAncestor returns true if ancestor is an ancestor of (or equal to) descendant.
func isAncestor(repoRoot, ancestor, descendant string) bool {
	_, err := gitExec(repoRoot, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

// parsedRef holds a parsed cache ref with its components.
type parsedRef struct {
	ref     string
	commit  string // commit SHA from ref path, or "" for legacy refs
	version int
}

// versionRefRe matches both legacy and new ref formats:
//
//	legacy: .../v<N>-<hex>  (no commit component)
//	new:    .../<commit>/v<N>-<hex>
var versionRefRe = regexp.MustCompile(`/v(\d+)(?:-[0-9a-f]+)?$`)

// commitRefRe extracts commit SHA and version from the new format:
//
//	refs/lazycue/<desc_hash>/<commit>/v<N>-<hex>
var commitRefRe = regexp.MustCompile(`/([0-9a-f]{40})/v(\d+)(?:-[0-9a-f]+)?$`)

// parseRefs extracts version info from a newline-separated list of refs.
// Returns parsed refs sorted by version descending.
func parseRefs(refsOutput string) []parsedRef {
	var parsed []parsedRef
	for _, ref := range strings.Split(refsOutput, "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}

		// Try new format first: .../<commit>/v<N>-<hex>
		if m := commitRefRe.FindStringSubmatch(ref); m != nil {
			v, _ := strconv.Atoi(m[2])
			if v > 0 {
				parsed = append(parsed, parsedRef{ref: ref, commit: m[1], version: v})
				continue
			}
		}

		// Legacy format: .../v<N>-<hex> (no commit component)
		if m := versionRefRe.FindStringSubmatch(ref); m != nil {
			v, _ := strconv.Atoi(m[1])
			if v > 0 {
				parsed = append(parsed, parsedRef{ref: ref, commit: "", version: v})
			}
		}
	}

	// Sort by version descending.
	sort.Slice(parsed, func(i, j int) bool {
		return parsed[i].version > parsed[j].version
	})
	return parsed
}

// CacheHit describes which ref a cache hit came from.
type CacheHit struct {
	Ref     string // full ref name, e.g. refs/lazycue/abc123/def.../v2-f3a9b2
	Version int
	Commit  string // commit SHA from the ref, or "" for legacy refs
}

// GetCachedTest retrieves the best cached DSL test for a description.
// If targetCommit is non-empty, only cache entries whose commit is an ancestor
// of targetCommit (or that have no commit, i.e. legacy) are considered.
// Returns nil, nil, nil if no cached version exists.
func GetCachedTest(repoRoot, remote, description, targetCommit string) (*CachedTest, *CacheHit, error) {
	prefix := refPrefix(description)

	refs, err := gitExec(repoRoot, "for-each-ref", "--format=%(refname)", prefix+"/")
	if err != nil || refs == "" {
		return nil, nil, nil
	}

	parsed := parseRefs(refs)
	if len(parsed) == 0 {
		return nil, nil, nil
	}

	// Find best matching ref: highest version among ancestor commits.
	var best *parsedRef
	for i := range parsed {
		p := &parsed[i]
		if p.commit == "" {
			// Legacy ref (no commit) — always eligible as fallback.
			if best == nil {
				best = p
			}
			continue
		}
		if targetCommit == "" {
			// No target specified — any commit is fine.
			if best == nil || p.version > best.version {
				best = p
			}
			continue
		}
		// Check ancestry: cache commit must be ancestor of target.
		if isAncestor(repoRoot, p.commit, targetCommit) {
			if best == nil || p.version > best.version {
				best = p
			}
		}
	}

	if best == nil {
		return nil, nil, nil
	}

	// Resolve ref to blob hash.
	blobHash, err := gitExec(repoRoot, "rev-parse", best.ref)
	if err != nil {
		return nil, nil, fmt.Errorf("rev-parse %s: %w", best.ref, err)
	}

	// Read blob content.
	content, err := gitExec(repoRoot, "cat-file", "blob", blobHash)
	if err != nil {
		return nil, nil, fmt.Errorf("cat-file %s: %w", blobHash, err)
	}

	raw := []byte(content)

	// Detect format: legacy (raw JSON array starting with '[') vs new wrapped format.
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "[") {
		// Legacy format: raw steps array.
		return &CachedTest{Steps: json.RawMessage(raw)}, &CacheHit{Ref: best.ref, Version: best.version, Commit: best.commit}, nil
	}

	var cached CachedTest
	if err := json.Unmarshal(raw, &cached); err != nil {
		return nil, nil, fmt.Errorf("unmarshal cached test: %w", err)
	}
	return &cached, &CacheHit{Ref: best.ref, Version: best.version, Commit: best.commit}, nil
}

// SaveCachedTest saves a DSL JSON blob as a new version.
// If commit is non-empty, the ref includes the commit SHA for ancestry filtering.
// If push is true, the ref is pushed to the remote.
func SaveCachedTest(repoRoot, remote, description string, code []byte, version int, meta *CacheMetadata, commit string, push bool) error {
	suffix := randomHex(6)
	var ref string
	if commit != "" {
		ref = fmt.Sprintf("%s/%s/v%d-%s", refPrefix(description), commit, version, suffix)
	} else {
		ref = fmt.Sprintf("%s/v%d-%s", refPrefix(description), version, suffix)
	}

	// Build the wrapped format.
	wrapped := CachedTest{
		Steps:    json.RawMessage(code),
		Metadata: meta,
	}
	blob, err := json.MarshalIndent(wrapped, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cached test: %w", err)
	}

	// Write blob.
	blobHash, err := gitExecStdin(repoRoot, string(blob), "hash-object", "-w", "--stdin")
	if err != nil {
		return fmt.Errorf("hash-object: %w", err)
	}

	// Create local ref.
	if _, err := gitExec(repoRoot, "update-ref", ref, blobHash); err != nil {
		return fmt.Errorf("update-ref: %w", err)
	}

	if push {
		if _, err := gitExec(repoRoot, "push", remote, ref+":"+ref); err != nil {
			// Non-fatal: log but don't fail.
			fmt.Printf("[lazycue] warning: push %s to %s failed: %v\n", ref, remote, err)
		}
	}

	return nil
}

// ListLocalRefs returns all local lazycue refs.
func ListLocalRefs(repoRoot string) ([]string, error) {
	out, err := gitExec(repoRoot, "for-each-ref", "--format=%(refname)", "refs/lazycue/")
	if err != nil || out == "" {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			refs = append(refs, line)
		}
	}
	return refs, nil
}

// PromoteRefs pushes local lazycue refs to the remote.
// If commit is non-empty and a ref has no commit component (legacy) or has a
// different commit, it is re-written under the new commit before pushing.
func PromoteRefs(repoRoot, remote, commit string) (pushed int, err error) {
	refs, err := ListLocalRefs(repoRoot)
	if err != nil {
		return 0, fmt.Errorf("list local refs: %w", err)
	}

	for _, ref := range refs {
		targetRef := ref

		// If caller specified a commit, ensure the ref is filed under that commit.
		if commit != "" {
			pr := parseOneRef(ref)
			if pr != nil && pr.commit != commit {
				// Re-file under the specified commit.
				// Read the blob, create a new ref under the commit path.
				descHash := extractDescHash(ref)
				if descHash == "" {
					continue
				}
				suffix := randomHex(6)
				targetRef = fmt.Sprintf("refs/lazycue/%s/%s/v%d-%s", descHash, commit, pr.version, suffix)

				// Copy blob to new ref.
				blobHash, bErr := gitExec(repoRoot, "rev-parse", ref)
				if bErr != nil {
					continue
				}
				if _, bErr := gitExec(repoRoot, "update-ref", targetRef, blobHash); bErr != nil {
					continue
				}
			}
		}

		if _, pErr := gitExec(repoRoot, "push", remote, targetRef+":"+targetRef); pErr != nil {
			return pushed, fmt.Errorf("push %s: %w", targetRef, pErr)
		}
		pushed++
	}
	return pushed, nil
}

// parseOneRef parses a single ref string into its components.
func parseOneRef(ref string) *parsedRef {
	if m := commitRefRe.FindStringSubmatch(ref); m != nil {
		v, _ := strconv.Atoi(m[2])
		if v > 0 {
			return &parsedRef{ref: ref, commit: m[1], version: v}
		}
	}
	if m := versionRefRe.FindStringSubmatch(ref); m != nil {
		v, _ := strconv.Atoi(m[1])
		if v > 0 {
			return &parsedRef{ref: ref, commit: "", version: v}
		}
	}
	return nil
}

// extractDescHash extracts the description hash from a ref path.
// e.g. "refs/lazycue/abc123def456/..." -> "abc123def456"
func extractDescHash(ref string) string {
	const prefix = "refs/lazycue/"
	if !strings.HasPrefix(ref, prefix) {
		return ""
	}
	rest := ref[len(prefix):]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return ""
	}
	return rest[:slash]
}
