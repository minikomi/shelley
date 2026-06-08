// Package lazycue implements self-healing browser tests written as plain English.
//
// A test is a description string. The system hashes the description to check
// git refs for a cached DSL test script. If cached, it executes the DSL.
// If the test passes, it's done. If it fails (or isn't cached), an LLM agent
// generates or fixes the DSL, and the new version is saved.
package lazycue

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

// Options configures a lazycue test run.
type Options struct {
	BaseURL          string // Base URL of the application under test (required)
	Remote           string // Git remote for cache (default: "origin")
	Model            string // LLM model (default: "claude-sonnet-4-6")
	AnthropicBaseURL string // Anthropic API base URL (default: ANTHROPIC_BASE_URL or https://api.anthropic.com)
	AnthropicAPIKey  string // Anthropic API key (default: ANTHROPIC_API_KEY)
	RepoRoot         string // Git repo root (default: auto-detect)
	Verbose          bool   // Verbose output
	NoPush           bool   // If true, don't push cache refs to remote after saving
	NoFetch          bool   // If true, use local cache only — don't fetch refs from the remote
	Commit           string // Commit SHA to tag cache with; also used for ancestry filtering (default: HEAD)
}

func (o *Options) defaults() {
	if o.Remote == "" {
		o.Remote = "origin"
	}
	if o.Model == "" {
		o.Model = "claude-sonnet-4-6"
	}
	if o.AnthropicBaseURL == "" {
		if v := os.Getenv("ANTHROPIC_BASE_URL"); v != "" {
			o.AnthropicBaseURL = v
		} else {
			o.AnthropicBaseURL = "https://api.anthropic.com"
		}
	}
	if o.AnthropicAPIKey == "" {
		o.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	}
}

// RunMode describes how the test was resolved.
type RunMode string

const (
	RunModeCached    RunMode = "cached"    // test ran from cache
	RunModeGenerated RunMode = "generated" // agent generated fresh
	RunModeHealed    RunMode = "healed"    // agent fixed a cached test
)

// TestResult is the result of running a lazy test.
type TestResult struct {
	Pass           bool
	Error          string
	ScreenshotPath string
	Steps          []StepResult
	CacheVersion   int
	Description    string
	Mode           RunMode
	TotalDuration  time.Duration // wall-clock time for the entire test
	AgentDuration  time.Duration // time spent in the LLM agent (0 if cached)
	InputTokens    int           // total input tokens used by agent (0 if cached)
	OutputTokens   int           // total output tokens used by agent (0 if cached)
	EstimatedCost  float64       // estimated USD cost
}

// StepResult is the result of executing a single DSL step.
type StepResult struct {
	Action   string        `json:"action"`
	Summary  string        `json:"summary"` // e.g. "click #login-button"
	Pass     bool          `json:"pass"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

// Run executes a single lazy test described by the given plain-English description.
func Run(ctx context.Context, opts Options, description string) (*TestResult, error) {
	totalStart := time.Now()
	opts.defaults()

	if opts.BaseURL == "" {
		return nil, fmt.Errorf("BaseURL is required")
	}

	if opts.RepoRoot == "" {
		root, err := DetectRepoRoot()
		if err != nil {
			return nil, fmt.Errorf("cannot detect repo root: %w", err)
		}
		opts.RepoRoot = root
	}

	// Resolve commit for cache ancestry.
	commit := opts.Commit
	if commit == "" {
		commit = detectGitSHA(opts.RepoRoot)
	}

	logf := func(format string, args ...any) {
		if opts.Verbose {
			log.Printf(format, args...)
		}
	}

	// Step 1: Check cache — local first, then remote.
	logf("[lazycue] checking local cache")
	cachedTest, cacheHit, err := GetCachedTest(opts.RepoRoot, opts.Remote, description, commit)
	if err != nil {
		logf("[lazycue] warning: get cached test: %v", err)
	}

	if cachedTest == nil && !opts.NoFetch {
		remoteURL := RemoteURL(opts.RepoRoot, opts.Remote)
		logf("[lazycue] no local cache hit, fetching from %s (%s)", opts.Remote, remoteURL)
		if err := FetchCachedRefs(opts.RepoRoot, opts.Remote); err != nil {
			logf("[lazycue] warning: fetch cache refs from %s: %v", remoteURL, err)
		}

		cachedTest, cacheHit, err = GetCachedTest(opts.RepoRoot, opts.Remote, description, commit)
		if err != nil {
			logf("[lazycue] warning: get cached test: %v", err)
		}
	} else if cachedTest != nil {
		logf("[lazycue] local cache hit: v%d from %s", cacheHit.Version, cacheHit.Ref)
	}

	var version int
	if cacheHit != nil {
		version = cacheHit.Version
	}

	// Step 2: Launch browser
	logf("[lazycue] launching browser for %s", opts.BaseURL)
	browser, err := NewBrowser(ctx)
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	defer browser.Close()

	push := !opts.NoPush

	// Step 3: If cached, try executing
	if cachedTest != nil {
		steps, parseErr := ParseSteps(cachedTest.Steps)
		if parseErr == nil {
			logf("[lazycue] found cached v%d (%d steps) from %s", version, len(steps), cacheHit.Ref)
			results, execErr := browser.ExecuteSteps(ctx, opts.BaseURL, steps)
			allPassed := execErr == nil
			for _, r := range results {
				if !r.Pass {
					allPassed = false
					break
				}
			}
			if allPassed {
				logf("[lazycue] cached test passed")
				return &TestResult{
					Pass:          true,
					Steps:         results,
					CacheVersion:  version,
					Description:   description,
					Mode:          RunModeCached,
					TotalDuration: time.Since(totalStart),
				}, nil
			}

			// Cached test failed — need to fix it
			logf("[lazycue] cached test failed, spawning agent to fix")
			failureDesc := summarizeFailure(results)

			// Reset browser for agent
			browser.Close()
			browser, err = NewBrowser(ctx)
			if err != nil {
				return nil, fmt.Errorf("relaunch browser: %w", err)
			}

			agentStart := time.Now()
			agentResult, agentErr := RunAgent(ctx, &AgentConfig{
				Mode:             AgentModeFix,
				Description:      description,
				PreviousSteps:    cachedTest.Steps,
				PreviousError:    failureDesc,
				Browser:          browser,
				BaseURL:          opts.BaseURL,
				Model:            opts.Model,
				AnthropicBaseURL: opts.AnthropicBaseURL,
				AnthropicAPIKey:  opts.AnthropicAPIKey,
				RepoRoot:         opts.RepoRoot,
				Verbose:          opts.Verbose,
			})
			agentDur := time.Since(agentStart)
			if agentErr != nil {
				return nil, fmt.Errorf("agent fix: %w", agentErr)
			}

			newVersion := version + 1
			cost := float64(agentResult.InputTokens)*3.0/1_000_000 + float64(agentResult.OutputTokens)*15.0/1_000_000
			if agentResult.Success {
				meta := buildCacheMetadata(opts, agentResult, "healed")
				if saveErr := SaveCachedTest(opts.RepoRoot, opts.Remote, description, agentResult.StepsJSON, newVersion, meta, commit, push); saveErr != nil {
					logf("[lazycue] warning: save cached test: %v", saveErr)
				}
			}

			return &TestResult{
				Pass:           agentResult.Success,
				Error:          agentResult.Error,
				ScreenshotPath: agentResult.ScreenshotPath,
				Steps:          agentResult.StepResults,
				CacheVersion:   newVersion,
				Description:    description,
				Mode:           RunModeHealed,
				TotalDuration:  time.Since(totalStart),
				AgentDuration:  agentDur,
				InputTokens:    agentResult.InputTokens,
				OutputTokens:   agentResult.OutputTokens,
				EstimatedCost:  cost,
			}, nil
		}
		// Parse error — treat as uncached
		logf("[lazycue] cached test parse error: %v, regenerating", parseErr)
	}

	// Step 4: No cache — generate from scratch
	logf("[lazycue] no cached test, spawning agent to generate")
	agentStart := time.Now()
	agentResult, agentErr := RunAgent(ctx, &AgentConfig{
		Mode:             AgentModeGenerate,
		Description:      description,
		Browser:          browser,
		BaseURL:          opts.BaseURL,
		Model:            opts.Model,
		AnthropicBaseURL: opts.AnthropicBaseURL,
		AnthropicAPIKey:  opts.AnthropicAPIKey,
		RepoRoot:         opts.RepoRoot,
		Verbose:          opts.Verbose,
	})
	agentDur := time.Since(agentStart)
	if agentErr != nil {
		return nil, fmt.Errorf("agent generate: %w", agentErr)
	}

	cost := float64(agentResult.InputTokens)*3.0/1_000_000 + float64(agentResult.OutputTokens)*15.0/1_000_000
	if agentResult.Success {
		meta := buildCacheMetadata(opts, agentResult, "generated")
		if saveErr := SaveCachedTest(opts.RepoRoot, opts.Remote, description, agentResult.StepsJSON, 1, meta, commit, push); saveErr != nil {
			logf("[lazycue] warning: save cached test: %v", saveErr)
		}
	}

	return &TestResult{
		Pass:           agentResult.Success,
		Error:          agentResult.Error,
		ScreenshotPath: agentResult.ScreenshotPath,
		Steps:          agentResult.StepResults,
		CacheVersion:   1,
		Description:    description,
		Mode:           RunModeGenerated,
		TotalDuration:  time.Since(totalStart),
		AgentDuration:  agentDur,
		InputTokens:    agentResult.InputTokens,
		OutputTokens:   agentResult.OutputTokens,
		EstimatedCost:  cost,
	}, nil
}

func summarizeFailure(results []StepResult) string {
	for i, r := range results {
		if !r.Pass {
			return fmt.Sprintf("step %d (%s) failed: %s", i, r.Action, r.Error)
		}
	}
	return "unknown failure"
}

// DetectRepoRoot returns the root of the git repository.
func DetectRepoRoot() (string, error) {
	out, err := gitExec(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return out, nil
}

// Harness holds options for running lazycue tests. Create one as a
// package-level var and call Test from each test function:
//
//	var browser = lazycue.New(lazycue.Options{BaseURL: "http://localhost:3000"})
//
//	func TestLogin(t *testing.T) {
//	    browser.Test(t, "Navigate to /login and verify the login form is visible")
//	}
type Harness struct {
	opts Options
}

// New creates a Harness with the given options.
func New(opts Options) *Harness {
	return &Harness{opts: opts}
}

// Test runs a self-healing browser test described in plain English.
// It calls t.Fatal if the test fails or encounters an error.
func (h *Harness) Test(t testing.TB, description string) {
	t.Helper()
	result, err := Run(t.Context(), h.opts, description)
	if err != nil {
		t.Fatalf("lazycue: %v", err)
	}

	// Log step results.
	var sb strings.Builder
	for _, s := range result.Steps {
		mark := "✓"
		if !s.Pass {
			mark = "✗"
		}
		fmt.Fprintf(&sb, "  %s %s  %s", mark, s.Summary, s.Duration.Round(time.Millisecond))
		if s.Error != "" {
			fmt.Fprintf(&sb, "  %s", s.Error)
		}
		sb.WriteByte('\n')
	}
	if result.InputTokens > 0 {
		cost := float64(result.InputTokens)*3.0/1_000_000 + float64(result.OutputTokens)*15.0/1_000_000
		fmt.Fprintf(&sb, "  ⚡ %d in / %d out tokens  ~$%.3f\n", result.InputTokens, result.OutputTokens, cost)
	}
	t.Logf("lazycue [%s]: %s\n%s", result.Mode, description, sb.String())

	if !result.Pass {
		t.Fatalf("lazycue: test failed: %s", result.Error)
	}
}

func buildCacheMetadata(opts Options, agentResult *AgentResult, mode string) *CacheMetadata {
	cost := float64(agentResult.InputTokens)*3.0/1_000_000 + float64(agentResult.OutputTokens)*15.0/1_000_000
	hostname, _ := os.Hostname()
	return &CacheMetadata{
		CreatedAt:        time.Now().UTC(),
		Hostname:         hostname,
		Model:            opts.Model,
		InputTokens:      agentResult.InputTokens,
		OutputTokens:     agentResult.OutputTokens,
		EstimatedCostUSD: cost,
		CIRun:            detectCIRun(),
		GitSHA:           detectGitSHA(opts.RepoRoot),
		Mode:             mode,
	}
}
