// Tool metadata helpers mirroring the iOS client (see
// iOS/exe.dev/Support/ToolEmoji.swift, ToolHeadline.swift, and
// ToolPillsRow.swift). Used by the conversation UI to render
// consecutive tool calls as a wrapped row of tightly packed,
// color-coded "pills" instead of one full-width card per call.

// Extract the action string from a `browser` tool's input. We use this
// to subspecialize emoji/headline for the umbrella "browser" tool name,
// matching how BrowserTool.tsx picks a specialized child component.
function browserAction(input: unknown): string {
  if (typeof input !== "object" || input === null) return "";
  const v = (input as Record<string, unknown>).action;
  return typeof v === "string" ? v : "";
}

/** Emoji for a tool pill. Pass `input` so the umbrella "browser" tool can
 *  pick a per-action emoji matching BrowserTool's per-action component. */
export function toolEmoji(name: string | undefined | null, input?: unknown): string {
  if (!name) return "⚙️";
  if (name === "browser") {
    switch (browserAction(input)) {
      case "navigate":
        return "🌐";
      case "eval":
        return "⚡";
      case "resize":
        return "📐";
      case "screenshot":
        return "📷";
      case "console_logs":
      case "clear_console_logs":
        return "📋";
      case "screencast_start":
      case "screencast_stop":
      case "screencast_status":
        return "🎬";
    }
  }
  switch (name) {
    case "bash":
    case "shell":
      return "🛠️";
    case "patch":
      return "🖋️";
    case "screenshot":
    case "browser_take_screenshot":
      return "📷";
    case "read_image":
      return "🖼️";
    case "browser_navigate":
      return "🌐";
    case "browser_eval":
      return "⚡";
    case "subagent":
      return "⚡";
    case "keyword_search":
      return "🔍";
    case "browser_recent_console_logs":
    case "browser_clear_console_logs":
    case "read_context_file":
      return "📋";
    case "browser_emulate":
      return "📱";
    case "browser_resize":
      return "📐";
    case "browser_accessibility":
      return "♿";
    case "browser_network":
      return "📡";
    case "browser_profile":
      return "📊";
    case "browser_screencast":
      return "🎬";
    case "change_dir":
      return "📂";
    case "llm_one_shot":
      return "🤖";
    case "output_iframe":
      return "✨";
    case "web_search":
      return "🔎";
    default:
      return "⚙️";
  }
}

// Hand-picked utilities where the second token carries the meaning
// (so a row of identical "ls" pills becomes "ls /tmp", "ls src", …).
const TWO_ARG_UTILS = new Set([
  "ls",
  "cat",
  "cd",
  "cp",
  "mv",
  "rm",
  "echo",
  "head",
  "tail",
  "grep",
  "less",
  "wc",
  "mkdir",
  "rmdir",
  "touch",
  "vim",
  "nano",
  "diff",
  "file",
  "which",
  "git",
  "go",
  "npm",
  "yarn",
  "pnpm",
  "cargo",
  "pip",
  "python",
  "python3",
  "swift",
  "make",
  "docker",
  "kubectl",
  "curl",
  "wget",
]);

function basename(p: string): string {
  const i = p.lastIndexOf("/");
  return i >= 0 ? p.slice(i + 1) : p;
}

/** Compact, tool-specific headline shown next to the emoji. */
export function toolHeadline(name: string | undefined | null, input: unknown): string {
  const n = name || "tool";
  const summary = inputSummary(name, input).trim();

  switch (n) {
    case "bash":
    case "shell": {
      const firstLine =
        summary
          .split(/\r?\n/)
          .find((l) => l.trim().length > 0)
          ?.trim() || "";
      if (!firstLine) return n;
      const tokens = firstLine.split(/\s+/).filter(Boolean);
      if (tokens.length === 0) return firstLine;
      const first = tokens[0];
      const head = first.includes("/") ? basename(first) : first;
      if (TWO_ARG_UTILS.has(head) && tokens.length >= 2) {
        const combined = `${head} ${tokens[1]}`;
        if (combined.length <= 24) return combined;
      }
      return head;
    }
    case "patch":
      return summary ? basename(summary) : n;
    case "change_dir":
      return summary || n;
    case "screenshot":
    case "browser_take_screenshot":
    case "read_image":
    case "browser_navigate":
    case "keyword_search":
      return summary || n;
    default: {
      if (!summary) return n;
      const firstLine = summary.split(/\r?\n/)[0] || summary;
      return `${n}: ${firstLine}`;
    }
  }
}

// Pull the most-relevant single string out of a tool input payload.
function inputSummary(name: string | undefined | null, input: unknown): string {
  if (input == null) return "";
  if (typeof input === "string") return input;
  if (typeof input !== "object") return String(input);
  const o = input as Record<string, unknown>;
  const pick = (...keys: string[]): string => {
    for (const k of keys) {
      const v = o[k];
      if (typeof v === "string" && v.length > 0) return v;
    }
    return "";
  };
  switch (name) {
    case "bash":
    case "shell":
      return pick("command");
    case "patch":
    case "change_dir":
    case "read_context_file":
      return pick("path");
    case "screenshot":
    case "browser_take_screenshot":
      return pick("selector", "url");
    case "read_image":
      return pick("path", "url");
    case "browser_navigate":
      return pick("url");
    case "keyword_search":
    case "web_search":
      return pick("query");
    case "subagent":
      return pick("slug", "prompt");
    case "llm_one_shot":
      return pick("prompt_file", "prompt");
    case "output_iframe":
      return pick("title", "path");
    case "browser_eval":
      return pick("expression");
    case "browser_emulate":
      return pick("device", "media");
    case "browser_resize": {
      const w = o.width,
        h = o.height;
      if (typeof w === "number" && typeof h === "number") return `${w}x${h}`;
      return "";
    }
    case "browser_accessibility":
    case "browser_network":
    case "browser_profile":
      return pick("action");
    default: {
      const v = pick("command", "path", "url", "query", "prompt", "action");
      if (v) return v;
      try {
        return JSON.stringify(input);
      } catch {
        return "";
      }
    }
  }
}

/** Tools whose entire value is the inline rendering (diffs, images,
 *  iframes). These are NOT collapsed into pills; they keep the
 *  current full-bleed card so the user sees the diff / image
 *  without an extra tap.
 */
export function isAutoExpandTool(name: string | undefined | null): boolean {
  switch (name) {
    case "patch":
    case "screenshot":
    case "browser_take_screenshot":
    case "read_image":
    case "output_iframe":
      return true;
    default:
      return false;
  }
}
