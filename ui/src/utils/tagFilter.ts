// Pure helpers backing the conversation drawer's tag handling: the `tag:`
// search-query syntax, the tag filter it drives, and tag-based grouping.
//
// Filter semantics (the UI lives in ConversationDrawer.vue):
//   * AND — a conversation matches only if it carries EVERY selected tag.
//   * Case-insensitive comparison, first-seen original casing for display.
//     The server's normalizeTags is deliberately case-sensitive, so `Infra`
//     and `infra` can both exist in storage; we fold them together here.
//   * Order-insensitive — tag order in storage is insertion order and carries
//     no meaning.
//   * Progressive narrowing — the offered tags are the tags carried by the
//     conversations that currently match, minus the selection. Selecting an
//     offered tag can therefore never produce an empty result set.
//
// The selection is not stored anywhere: it lives in the search query the user
// typed, which is the only copy. Nothing can be silently filtering.
//
// No Vue imports: everything here is a plain function over conversation rows.
import type { Conversation } from "../types";
import { parseTags } from "../vue/components/conversationDrawerShared";

// The comparison key for a tag. Empty string means "not a tag".
export function foldTag(tag: string): string {
  return tag.trim().toLowerCase();
}

// The folded tags of one conversation, deduped (storage may hold both `Infra`
// and `infra` on the same row).
function foldedTagSet(conversation: Conversation): Set<string> {
  const out = new Set<string>();
  for (const tag of parseTags(conversation)) {
    const folded = foldTag(tag);
    if (folded) out.add(folded);
  }
  return out;
}

function foldedSelection(selected: readonly string[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const tag of selected) {
    const folded = foldTag(tag);
    if (!folded || seen.has(folded)) continue;
    seen.add(folded);
    out.push(folded);
  }
  return out;
}

export function conversationMatchesTags(
  conversation: Conversation,
  selected: readonly string[],
): boolean {
  const wanted = foldedSelection(selected);
  if (wanted.length === 0) return true;
  const has = foldedTagSet(conversation);
  return wanted.every((tag) => has.has(tag));
}

// Keeps every conversation carrying all of `selected`. An empty selection
// returns the input array unchanged (same reference) so the no-filter case
// costs nothing and does not churn downstream computeds.
export function filterConversationsByTags<T extends Conversation>(
  conversations: T[],
  selected: readonly string[],
): T[] {
  const wanted = foldedSelection(selected);
  if (wanted.length === 0) return conversations;
  return conversations.filter((conversation) => {
    const has = foldedTagSet(conversation);
    return wanted.every((tag) => has.has(tag));
  });
}

// The tag half of a parsed query: the AND over `tag:` terms, plus
// `is:untagged`. One function so the drawer's three lists cannot drift.
export function filterConversationsByQuery<T extends Conversation>(
  conversations: T[],
  query: TagQuery,
): T[] {
  const byTag = filterConversationsByTags(conversations, query.tags);
  if (!query.untaggedOnly) return byTag;
  return byTag.filter((conversation) => foldedTagSet(conversation).size === 0);
}

// Whether the query narrows by tag at all. Drives "is the tag filter to blame
// for this empty list".
export function queryHasTagFilter(query: TagQuery): boolean {
  return query.tags.length > 0 || query.untaggedOnly;
}

// Removes `is:untagged` from a query, leaving everything else alone.
export function removeUntaggedFromQuery(raw: string): string {
  const kept = raw
    .split(/\s+/)
    .filter((term) => term !== "" && term.toLowerCase() !== UNTAGGED_TERM);
  return kept.length === 0 ? "" : kept.join(" ") + " ";
}

export type TagQuery = Pick<ParsedQuery, "tags" | "untaggedOnly">;

export interface OfferedTag {
  // First-seen original casing, for display.
  tag: string;
  // How many conversations would remain if this tag were added to the
  // selection.
  count: number;
}

// The tags that can still be added, each with the size of the result set it
// would produce. Sorted by count descending, then alphabetically.
export function offeredTags(
  conversations: Conversation[],
  selected: readonly string[],
): OfferedTag[] {
  const chosen = new Set(foldedSelection(selected));
  const counts = new Map<string, OfferedTag>();
  for (const conversation of filterConversationsByTags(conversations, selected)) {
    const seen = new Set<string>();
    for (const tag of parseTags(conversation)) {
      const folded = foldTag(tag);
      if (!folded || chosen.has(folded) || seen.has(folded)) continue;
      seen.add(folded);
      const existing = counts.get(folded);
      if (existing) existing.count += 1;
      else counts.set(folded, { tag: tag.trim(), count: 1 });
    }
  }
  return [...counts.values()].sort(
    (a, b) => b.count - a.count || foldTag(a.tag).localeCompare(foldTag(b.tag)),
  );
}

// Every folded tag present anywhere in the list. Drives "does any conversation
// have a tag at all" — the filter button hides entirely when this is empty.
export function presentTags(conversations: Conversation[]): Set<string> {
  const out = new Set<string>();
  for (const conversation of conversations) {
    for (const tag of foldedTagSet(conversation)) out.add(tag);
  }
  return out;
}

export function isTagSelected(selected: readonly string[], tag: string): boolean {
  const folded = foldTag(tag);
  return folded !== "" && selected.some((t) => foldTag(t) === folded);
}

// Substring match used by the picker's search box.
export function tagMatchesQuery(tag: string, query: string): boolean {
  const needle = foldTag(query);
  if (!needle) return true;
  return foldTag(tag).includes(needle);
}

// --- The `tag:` search-query syntax ---
//
// Tags are typed into the ordinary search box: `tag:infra auth bug` means
// "carries #infra, and matches the text 'auth bug'". This keeps one input for
// "narrow my list" instead of a search box plus a separate filter control, and
// it makes the active filter visible and editable as text.
//
// A bare `tag:` (no value) is the cue to open the tag dropdown, so typing the
// prefix offers the vocabulary rather than requiring the tag be known upfront.
//
// "Show me the untagged ones" is `is:untagged`, in a separate namespace on
// purpose. Spelling it `tag:untagged` would collide the moment somebody
// creates a tag actually named "untagged" — and somebody will. Because `is:`
// is its own prefix, that tag stays perfectly addressable as `tag:untagged`,
// with no escaping rule to learn.

const TAG_PREFIX = "tag:";
export const UNTAGGED_TERM = "is:untagged";

export interface ParsedQuery {
  // COMMITTED tags, in the order typed, original casing preserved. A tag is
  // committed once it is followed by a space or another term.
  tags: string[];
  // Everything that was not a `tag:` term, for the full-text search.
  text: string;
  // True when `is:untagged` is present: keep only conversations with no tags.
  // Combining it with a `tag:` term asks for something nothing can satisfy, so
  // the result is empty — which is exactly what the query says.
  untaggedOnly: boolean;
  // The half-typed trailing `tag:` term, if the caret is still inside one —
  // a bare `tag:`, or a partial `tag:inf`.
  //
  // Deliberately NOT part of `tags`: a half-typed tag matches no conversation,
  // so filtering on it would empty the list and leave the dropdown — which is
  // computed from what survives — with nothing to suggest. Mid-typing is a
  // query against the tag vocabulary; only a committed tag filters.
  activeTagPrefix: string | null;
}

// Splits a raw search query into tag terms and free text.
//
// Deliberately simple: whitespace-separated terms, no quoting. Tags cannot
// contain whitespace in practice (they are chips), and a quoting mini-language
// would be more syntax than this earns.
export function parseSearchQuery(raw: string): ParsedQuery {
  const tags: string[] = [];
  const seen = new Set<string>();
  const words: string[] = [];
  // A trailing `tag:` term is "active" only if the user has not typed a space
  // after it; the split below cannot see that, so check the raw string.
  const endsMidTerm = raw.length > 0 && !/\s$/.test(raw);
  const terms = raw.split(/\s+/).filter((term) => term !== "");
  let untaggedOnly = false;
  terms.forEach((term, i) => {
    if (term.toLowerCase() === UNTAGGED_TERM) {
      untaggedOnly = true;
      return;
    }
    if (!term.toLowerCase().startsWith(TAG_PREFIX)) {
      words.push(term);
      return;
    }
    const value = term.slice(TAG_PREFIX.length);
    const folded = foldTag(value);
    // The final term keeps its own line below; everything else is committed.
    if (i === terms.length - 1 && endsMidTerm) return;
    if (!folded || seen.has(folded)) return;
    seen.add(folded);
    tags.push(value.trim());
  });

  const last = terms[terms.length - 1];
  let activeTagPrefix: string | null = null;
  if (endsMidTerm && last && last.toLowerCase().startsWith(TAG_PREFIX)) {
    activeTagPrefix = last.slice(TAG_PREFIX.length);
  }

  return { tags, text: words.join(" "), untaggedOnly, activeTagPrefix };
}

// Replaces the `tag:` term the caret sits in with `term`, followed by a space
// so the next keystroke starts a fresh term. Used when a dropdown entry is
// chosen — `term` is a full token (`tag:infra`, or `is:untagged`).
export function completeTermInQuery(raw: string, term: string): string {
  const trimmedEnd = raw.replace(/\s+$/, "");
  const terms = trimmedEnd.split(/\s+/).filter((term) => term !== "");
  const last = terms[terms.length - 1];
  const replacing =
    last !== undefined && last.toLowerCase().startsWith(TAG_PREFIX) && !/\s$/.test(raw);
  const kept = replacing ? terms.slice(0, -1) : terms;
  return [...kept, term].join(" ") + " ";
}

// Removes every `tag:` term matching `tag`, leaving the rest of the query
// alone. Used by the chips and by clicking an already-selected tag.
export function removeTagFromQuery(raw: string, tag: string): string {
  const target = foldTag(tag);
  const kept = raw
    .split(/\s+/)
    .filter((term) => term !== "")
    .filter((term) => {
      if (!term.toLowerCase().startsWith(TAG_PREFIX)) return true;
      return foldTag(term.slice(TAG_PREFIX.length)) !== target;
    });
  return kept.length === 0 ? "" : kept.join(" ") + " ";
}

// Adds `tag:<tag>` to a query, or removes it if already present. Used by the
// row chips, which toggle.
export function toggleTagInQuery(raw: string, tag: string): string {
  if (isTagSelected(parseSearchQuery(raw).tags, tag)) return removeTagFromQuery(raw, tag);
  const base = raw.replace(/\s+$/, "");
  return (base ? base + " " : "") + `${TAG_PREFIX}${tag.trim()} `;
}

// --- Grouping ---

// The group key for "group by tag": the conversation's whole tag set, sorted
// and deduped, so `#infra #urgent` and `#urgent #infra` land in one group.
//
// Grouping by a MULTI-VALUED field has no single obviously-right answer. Using
// the combination as the key keeps the invariant every other group mode has —
// each conversation appears exactly once — which a tag-per-group layout would
// break by duplicating rows.
//
// NUL-joined so the parts stay recoverable: no tag can contain one.
const TAG_KEY_SEP = "\u0000";

export function tagGroupKey(conversation: Conversation): string | null {
  const folded = [...foldedTagSet(conversation)].sort();
  return folded.length === 0 ? null : folded.join(TAG_KEY_SEP);
}

// Orders tag groups by first tag, then second, and so on — so `#a #b` precedes
// `#b`, which precedes `#b #c` and then `#b #d`. A set that is a prefix of
// another sorts first, which puts the broad `#b` group directly above the
// narrower `#b #…` groups that extend it.
//
// This is a plain tuple comparison rather than a comparison of the joined
// keys: localeCompare gives the right answer per tag, but on the joined string
// collation may ignore the NUL separator, which would break the prefix rule.
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
