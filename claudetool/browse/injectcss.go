package browse

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/chromedp"
	"shelley.exe.dev/llm"
)

// jsString renders s as a JavaScript string literal. JSON string syntax is a
// subset of JavaScript's, so this is safe for arbitrary stylesheet text.
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err) // json.Marshal of a string cannot fail
	}
	return string(b)
}

// injectedStyleID is the id of the single <style> element that inject_css
// owns. One node, overwritten on every call, so injected CSS never
// accumulates into invisible layers the agent can't reason about.
const injectedStyleID = "shelley-injected-css"

// injectCSSRun installs (or clears) a stylesheet in the live page without
// touching source files. Intended for fast visual experimentation during
// focused UI work: try a rule, screenshot the element, and only write the
// rule into the real stylesheet once it looks right.
//
// The style node lives in the document, so it does NOT survive a navigation
// or a full page reload — a fresh document is a fresh page. Framework hot
// module replacement leaves it in place.
func (b *BrowseTools) injectCSSRun(ctx context.Context, css, timeout string) llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}
	timeoutCtx, cancel := context.WithTimeout(browserCtx, parseTimeout(timeout))
	defer cancel()

	// Passing the CSS as a function argument rather than interpolating it into
	// the expression keeps quoting and newlines in the stylesheet from
	// breaking the script.
	const script = `(css => {
		const id = %q;
		let node = document.getElementById(id);
		if (!css) {
			if (node) node.remove();
			return false;
		}
		if (!node) {
			node = document.createElement("style");
			node.id = id;
			document.head.appendChild(node);
		}
		node.textContent = css;
		return true;
	})(%s)`

	var active bool
	expr := fmt.Sprintf(script, injectedStyleID, jsString(css))
	if err := chromedp.Run(timeoutCtx, chromedp.Evaluate(expr, &active)); err != nil {
		return llm.ErrorToolOut(err)
	}

	if !active {
		return llm.ToolOut{LLMContent: llm.TextContent("Injected CSS cleared. The page now shows only its real stylesheets.")}
	}
	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf(
		"Injected CSS is live in <style id=%q> (%d bytes). It overrides the real stylesheets until you clear it "+
			"(inject_css with an empty css) or navigate away. Write the rules you keep into source — the page is not a file.",
		injectedStyleID, len(css),
	))}
}

// injectedCSSActive reports whether the inject_css style node is currently in
// the document. Screenshots consult this so a cropped image can never be read
// as evidence about source files when it is really evidence about a live
// override.
func (b *BrowseTools) injectedCSSActive(ctx context.Context) bool {
	var active bool
	expr := fmt.Sprintf(`!!document.getElementById(%q)`, injectedStyleID)
	if err := chromedp.Run(ctx, chromedp.Evaluate(expr, &active)); err != nil {
		return false
	}
	return active
}
