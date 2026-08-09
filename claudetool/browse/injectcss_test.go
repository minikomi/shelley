package browse

import (
	"context"
	"strings"
	"testing"
)

// fixtureURL is a data: URL for a page whose #box color comes from a real
// stylesheet. It must be percent-encoded: a bare "#" in a data: URL starts the
// fragment and silently truncates the document.
const fixtureURL = "data:text/html,%3Cstyle%3E%23box%7Bcolor%3Ared%7D%3C%2Fstyle%3E%3Cdiv%20id%3Dbox%3Ehi%3C%2Fdiv%3E"

func TestInjectCSS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() { tools.Close() })
	tool := tools.CombinedTool()

	run := func(input string) string {
		t.Helper()
		out := tool.Run(ctx, []byte(input))
		if out.Error != nil {
			if strings.Contains(out.Error.Error(), "browser automation not available") ||
				strings.Contains(out.Error.Error(), "failed to start browser") {
				t.Skip("Browser automation not available in this environment")
			}
			t.Fatalf("%s: %v", input, out.Error)
		}
		return out.LLMContent[0].Text
	}
	color := func() string {
		t.Helper()
		return run(`{"action":"eval","expression":"getComputedStyle(document.getElementById('box')).color"}`)
	}
	styleNodes := func() string {
		t.Helper()
		return run(`{"action":"eval","expression":"document.querySelectorAll('style#shelley-injected-css').length"}`)
	}
	navigate := func() {
		t.Helper()
		run(`{"action":"navigate","url":"` + fixtureURL + `"}`)
	}

	navigate()
	if got := color(); !strings.Contains(got, "rgb(255, 0, 0)") {
		t.Fatalf("fixture should start red, got %s", got)
	}

	// Injected CSS overrides the page's real stylesheet.
	run(`{"action":"inject_css","css":"#box { color: lime }"}`)
	if got := color(); !strings.Contains(got, "rgb(0, 255, 0)") {
		t.Errorf("injected CSS did not apply: %s", got)
	}

	// A second injection replaces the first rather than stacking, so injected
	// rules never accumulate into layers the agent cannot reason about.
	run(`{"action":"inject_css","css":"#box { color: blue }"}`)
	if got := color(); !strings.Contains(got, "rgb(0, 0, 255)") {
		t.Errorf("second injection did not replace the first: %s", got)
	}
	if got := styleNodes(); !strings.Contains(got, "1") {
		t.Errorf("expected exactly one injected style node, got %s", got)
	}

	// Quotes and newlines in the stylesheet survive the JSON -> JS round trip.
	run(`{"action":"inject_css","css":"#box::after {\n  content: \"it's \\\"quoted\\\"\";\n}\n#box { color: magenta }"}`)
	if got := color(); !strings.Contains(got, "rgb(255, 0, 255)") {
		t.Errorf("multi-line CSS did not apply: %s", got)
	}
	if got := run(`{"action":"eval","expression":"getComputedStyle(document.getElementById('box'),'::after').content"}`); !strings.Contains(got, "quoted") {
		t.Errorf("quoted content did not survive escaping: %s", got)
	}

	// An empty css clears the injection and restores the real stylesheet.
	if got := run(`{"action":"inject_css","css":""}`); !strings.Contains(got, "cleared") {
		t.Errorf("expected clear confirmation, got %s", got)
	}
	if got := color(); !strings.Contains(got, "rgb(255, 0, 0)") {
		t.Errorf("clearing did not restore the real stylesheet: %s", got)
	}
	if got := styleNodes(); !strings.Contains(got, "0") {
		t.Errorf("cleared injection left a style node behind: %s", got)
	}

	// The injection lives in the document, so a navigation drops it. A fresh
	// document is a fresh page.
	run(`{"action":"inject_css","css":"#box { color: lime }"}`)
	navigate()
	if got := color(); !strings.Contains(got, "rgb(255, 0, 0)") {
		t.Errorf("injection survived navigation: %s", got)
	}
	if got := styleNodes(); !strings.Contains(got, "0") {
		t.Errorf("injected style node survived navigation: %s", got)
	}
}

// TestScreenshotReportsInjectedCSS verifies that a screenshot taken while
// inject_css is live says so. A cropped screenshot is the main evidence an
// agent uses during focused UI work; if it silently reflects a live override,
// the agent can conclude source is correct when it is not.
func TestScreenshotReportsInjectedCSS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test in short mode")
	}
	ctx := context.Background()
	tools := NewBrowseTools(ctx, 0)
	t.Cleanup(func() { tools.Close() })
	tool := tools.CombinedTool()

	run := func(input string) llmOut {
		t.Helper()
		out := tool.Run(ctx, []byte(input))
		if out.Error != nil {
			if strings.Contains(out.Error.Error(), "browser automation not available") ||
				strings.Contains(out.Error.Error(), "failed to start browser") {
				t.Skip("Browser automation not available in this environment")
			}
			t.Fatalf("%s: %v", input, out.Error)
		}
		display, _ := out.Display.(map[string]any)
		return llmOut{text: out.LLMContent[0].Text, display: display}
	}

	run(`{"action":"navigate","url":"` + fixtureURL + `"}`)

	clean := run(`{"action":"screenshot"}`)
	if strings.Contains(clean.text, "injected CSS is live") {
		t.Errorf("clean screenshot should not mention injected CSS: %s", clean.text)
	}
	if clean.display["injected_css"] != false {
		t.Errorf("clean screenshot display injected_css = %v, want false", clean.display["injected_css"])
	}

	run(`{"action":"inject_css","css":"#box { color: lime }"}`)
	dirty := run(`{"action":"screenshot"}`)
	if !strings.Contains(dirty.text, "injected CSS is live") {
		t.Errorf("screenshot taken under injected CSS must say so: %s", dirty.text)
	}
	if dirty.display["injected_css"] != true {
		t.Errorf("screenshot display injected_css = %v, want true", dirty.display["injected_css"])
	}

	run(`{"action":"inject_css","css":""}`)
	if got := run(`{"action":"screenshot"}`); strings.Contains(got.text, "injected CSS is live") {
		t.Errorf("screenshot after clearing should not mention injected CSS: %s", got.text)
	}
}

type llmOut struct {
	text    string
	display map[string]any
}
