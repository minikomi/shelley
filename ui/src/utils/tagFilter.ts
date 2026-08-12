// Pure helpers backing the conversation drawer's tag-based grouping.
//
// No Vue imports: everything here is a plain function over conversation rows.
import type { Conversation } from "../types";
import { parseTags } from "../vue/components/conversationDrawerShared";

// The comparison key for a tag. Empty string means "not a tag".
export function foldTag(tag: string): string {
  return tag.trim().toLowerCase();
}

// The folded tags of one conversation, deduped (the server's normalizeTags is
// case-sensitive, so storage may hold both `Infra` and `infra`).
function foldedTagSet(conversation: Conversation): Set<string> {
  const out = new Set<string>();
  for (const tag of parseTags(conversation)) {
    const folded = foldTag(tag);
    if (folded) out.add(folded);
  }
  return out;
}

// --- Grouping ---

// The group key for "group by tag": the conversation's whole tag set, sorted
// and deduped, so `#infra #urgent` and `#urgent #infra` land in one group and
// every conversation appears exactly once (a tag-per-group layout would
// duplicate rows). NUL-joined so the parts stay recoverable: no tag can
// contain one.
const TAG_KEY_SEP = "\u0000";

export function tagGroupKey(conversation: Conversation): string | null {
  const folded = [...foldedTagSet(conversation)].sort();
  return folded.length === 0 ? null : folded.join(TAG_KEY_SEP);
}

// Orders tag groups by first tag, then second, and so on — so `#a #b`
// precedes `#b`, which precedes `#b #c`. Compares tag tuples rather than the
// joined keys: collation may ignore the NUL separator.
export function compareTagGroupKeys(a: string, b: string): number {
  const left = a.split(TAG_KEY_SEP);
  const right = b.split(TAG_KEY_SEP);
  for (let i = 0; i < Math.min(left.length, right.length); i++) {
    const cmp = left[i].localeCompare(right[i]);
    if (cmp !== 0) return cmp;
  }
  return left.length - right.length;
}

// The display label for a tagGroupKey: original casing, sorted, `#`-prefixed.
export function tagGroupLabel(conversation: Conversation): string {
  const byFold = new Map<string, string>();
  for (const tag of parseTags(conversation)) {
    const folded = foldTag(tag);
    if (folded && !byFold.has(folded)) byFold.set(folded, tag.trim());
  }
  return [...byFold.keys()]
    .sort()
    .map((folded) => `#${byFold.get(folded)}`)
    .join(" ");
}
