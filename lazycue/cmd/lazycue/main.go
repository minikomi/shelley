// Command lazycue runs self-healing browser tests described in plain English.
//
// Usage:
//
//	lazycue [options] "test description" ["test description" ...]
//	lazycue promote [options]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	lazycue "github.com/boldsoftware/shelley/lazycue"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "promote" {
		runPromote(os.Args[2:])
		return
	}
	runTests()
}

func runTests() {
	baseURL := flag.String("base-url", "", "Base URL of the app under test (required)")
	remote := flag.String("remote", "origin", "Git remote for cache")
	model := flag.String("model", "", "LLM model (default: claude-sonnet-4-6)")
	apiURL := flag.String("api-url", "", "Anthropic API base URL (env: ANTHROPIC_BASE_URL)")
	apiKey := flag.String("api-key", "", "Anthropic API key (env: ANTHROPIC_API_KEY)")
	verbose := flag.Bool("verbose", false, "Verbose output")
	noPush := flag.Bool("no-push", false, "Save cache locally only; use 'promote' subcommand to push to remote after CI passes")
	noFetch := flag.Bool("no-fetch", false, "Use local cache only — don't fetch refs from the remote")
	commit := flag.String("commit", "", "Pin cache to this commit SHA instead of HEAD (controls which cached tests are visible via git ancestry)")

	flag.Parse()

	if *baseURL == "" {
		fmt.Fprintln(os.Stderr, "error: --base-url is required")
		os.Exit(2)
	}

	descriptions := flag.Args()
	if len(descriptions) == 0 {
		fmt.Fprintln(os.Stderr, `usage: lazycue [options] "test description" ["test description" ...]`)
		fmt.Fprintln(os.Stderr, "       lazycue promote [options]")
		os.Exit(2)
	}

	opts := lazycue.Options{
		BaseURL:          *baseURL,
		Remote:           *remote,
		Model:            *model,
		AnthropicBaseURL: *apiURL,
		AnthropicAPIKey:  *apiKey,
		Verbose:          *verbose,
		NoPush:           *noPush,
		NoFetch:          *noFetch,
		Commit:           *commit,
	}

	ctx := context.Background()
	var anyFailed bool
	suiteStart := time.Now()

	for i, desc := range descriptions {
		if i > 0 {
			fmt.Println()
		}
		result, err := lazycue.Run(ctx, opts, desc)
		if err != nil {
			printError(i+1, len(descriptions), desc, err)
			anyFailed = true
			continue
		}

		printResult(i+1, len(descriptions), result)

		if !result.Pass {
			anyFailed = true
		}
	}

	// Suite summary.
	if len(descriptions) > 1 {
		fmt.Println()
		elapsed := time.Since(suiteStart).Round(time.Millisecond)
		if anyFailed {
			fmt.Printf("\033[31m✗ some tests failed\033[0m  (%s)\n", elapsed)
		} else {
			fmt.Printf("\033[32m✓ %d tests passed\033[0m  (%s)\n", len(descriptions), elapsed)
		}
	}

	if anyFailed {
		os.Exit(1)
	}
}

func runPromote(args []string) {
	fs := flag.NewFlagSet("promote", flag.ExitOnError)
	remote := fs.String("remote", "origin", "Git remote for cache")
	commit := fs.String("commit", "", "Re-tag refs under this commit SHA before pushing (default: keep existing)")
	fs.Parse(args)

	root, err := lazycue.DetectRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: not in a git repository: %v\n", err)
		os.Exit(2)
	}

	pushed, err := lazycue.PromoteRefs(root, *remote, *commit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\033[31merror: %v\033[0m\n", err)
		os.Exit(1)
	}

	if pushed == 0 {
		fmt.Println("no local cache refs to promote")
	} else {
		fmt.Printf("\033[32m✓ promoted %d cache ref(s) to %s\033[0m\n", pushed, *remote)
	}
}

func printResult(idx, total int, r *lazycue.TestResult) {
	// Status emoji + colour.
	var status, colour, reset string
	if r.Pass {
		status = "PASS"
		colour = "\033[32m" // green
	} else {
		status = "FAIL"
		colour = "\033[31m" // red
	}
	reset = "\033[0m"

	// Mode badge.
	var badge string
	switch r.Mode {
	case lazycue.RunModeCached:
		badge = fmt.Sprintf("cached v%d", r.CacheVersion)
	case lazycue.RunModeGenerated:
		badge = fmt.Sprintf("generated → v%d", r.CacheVersion)
	case lazycue.RunModeHealed:
		badge = fmt.Sprintf("healed → v%d", r.CacheVersion)
	}

	// Timing.
	totalMs := r.TotalDuration.Round(time.Millisecond)
	var timing string
	if r.AgentDuration > 0 {
		timing = fmt.Sprintf("%s total, %s agent", totalMs, r.AgentDuration.Round(time.Millisecond))
	} else {
		timing = totalMs.String()
	}

	// Header line.
	if total > 1 {
		fmt.Printf("%s%s%s  %d/%d  [%s]  %s\n", colour, status, reset, idx, total, badge, timing)
	} else {
		fmt.Printf("%s%s%s  [%s]  %s\n", colour, status, reset, badge, timing)
	}

	// Description (dimmed).
	fmt.Printf("\033[2m  %s\033[0m\n", truncateDesc(r.Description, 120))

	// Steps.
	if len(r.Steps) > 0 {
		for _, s := range r.Steps {
			mark := "\033[32m✓\033[0m"
			if !s.Pass {
				mark = "\033[31m✗\033[0m"
			}
			line := fmt.Sprintf("  %s %-50s %6s", mark, s.Summary, s.Duration.Round(time.Millisecond))
			if s.Error != "" {
				line += fmt.Sprintf("  \033[31m%s\033[0m", truncateDesc(s.Error, 80))
			}
			fmt.Println(line)
		}
	}

	// Token usage.
	if r.InputTokens > 0 {
		fmt.Printf("\033[2m  ⚡ %s in / %s out tokens  ~$%.3f\033[0m\n", formatTokens(r.InputTokens), formatTokens(r.OutputTokens), r.EstimatedCost)
	}

	// Error detail for failures.
	if !r.Pass && r.Error != "" {
		errLines := strings.Split(r.Error, "\n")
		if len(errLines) <= 3 {
			fmt.Printf("\033[31m  %s\033[0m\n", r.Error)
		} else {
			for _, l := range errLines[:3] {
				fmt.Printf("\033[31m  %s\033[0m\n", l)
			}
			fmt.Printf("\033[2m  ... (%d more lines)\033[0m\n", len(errLines)-3)
		}
	}
}

func printError(idx, total int, desc string, err error) {
	if total > 1 {
		fmt.Printf("\033[31mERROR\033[0m  %d/%d\n", idx, total)
	} else {
		fmt.Printf("\033[31mERROR\033[0m\n")
	}
	fmt.Printf("\033[2m  %s\033[0m\n", truncateDesc(desc, 120))
	fmt.Printf("\033[31m  %s\033[0m\n", err)
}

func truncateDesc(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// formatTokens formats an integer with comma separators: 14832 → "14,832".
func formatTokens(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}
