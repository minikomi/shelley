// Shared thinking-level constants/types. "default" leaves the request unset so
// the selected model's configured/provider default applies.
export type ThinkingLevel = "default" | "off" | "minimal" | "low" | "medium" | "high" | "xhigh";

export const DEFAULT_THINKING_LEVEL: ThinkingLevel = "default";

export const THINKING_LEVELS: { value: ThinkingLevel; label: string }[] = [
  { value: "default", label: "default" },
  { value: "off", label: "off" },
  { value: "minimal", label: "minimal" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
];

export const THINKING_LEVEL_KEY = "shelley.thinkingLevel.v2";

// storedThinkingLevel is the user's last composer effort pick, or the
// "default" sentinel when nothing valid is stored.
export function storedThinkingLevel(): ThinkingLevel {
  const stored = localStorage.getItem(THINKING_LEVEL_KEY);
  return THINKING_LEVELS.some((l) => l.value === stored)
    ? (stored as ThinkingLevel)
    : DEFAULT_THINKING_LEVEL;
}
