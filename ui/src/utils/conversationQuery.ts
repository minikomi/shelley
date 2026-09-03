const TAG_PREFIX = "tag:";
const USER_PREFIX = "user:";
export const UNTAGGED_TERM = "is:untagged";
export const UNATTRIBUTED_TERM = "is:unattributed";

export interface QueryTermSpan {
  raw: string;
  start: number;
  end: number;
}

export interface ScannedConversationQuery {
  terms: QueryTermSpan[];
  endsMidTerm: boolean;
}

export type StructuredQueryKind = "tag" | "user" | "untagged" | "unattributed";

export interface QueryTextToken {
  kind: "text";
  raw: string;
  start: number;
  end: number;
}

export interface StructuredQueryToken {
  kind: StructuredQueryKind;
  raw: string;
  start: number;
  end: number;
  label: string;
  value: string;
}

export type ConversationQueryToken = QueryTextToken | StructuredQueryToken;

// Finds whitespace-delimited query terms while treating a quoted span as one
// term. Ranges refer to the original query so editor operations never have to
// rebuild unrelated text or quoted tag values.
export function scanConversationQuery(raw: string): ScannedConversationQuery {
  const terms: QueryTermSpan[] = [];
  let start = -1;
  let inQuote = false;
  let escaped = false;

  for (let i = 0; i < raw.length; i += 1) {
    const ch = raw[i];
    if (start < 0) {
      if (/\s/.test(ch)) continue;
      start = i;
    }
    if (escaped) {
      escaped = false;
      continue;
    }
    if (inQuote && ch === "\\") {
      escaped = true;
      continue;
    }
    if (ch === '"') {
      inQuote = !inQuote;
      continue;
    }
    if (!inQuote && /\s/.test(ch)) {
      terms.push({ raw: raw.slice(start, i), start, end: i });
      start = -1;
    }
  }

  if (start >= 0) terms.push({ raw: raw.slice(start), start, end: raw.length });
  return { terms, endsMidTerm: start >= 0 };
}

// Decodes the value portion of a tag term. An unterminated quoted value is
// valid while it is still being typed.
export function decodeTagQueryValue(value: string): string {
  if (!value.startsWith('"')) return value;
  let out = "";
  let escaped = false;
  for (const ch of value.slice(1)) {
    if (escaped) {
      out += ch;
      escaped = false;
      continue;
    }
    if (ch === "\\") {
      escaped = true;
      continue;
    }
    if (ch === '"') break;
    out += ch;
  }
  return out;
}

function structuredToken(
  term: QueryTermSpan,
  trailingPartial: boolean,
): StructuredQueryToken | null {
  const lower = term.raw.toLowerCase();
  if (lower === UNTAGGED_TERM) {
    return { ...term, kind: "untagged", label: UNTAGGED_TERM, value: "" };
  }
  if (lower === UNATTRIBUTED_TERM) {
    return { ...term, kind: "unattributed", label: UNATTRIBUTED_TERM, value: "" };
  }
  if (lower.startsWith(TAG_PREFIX)) {
    if (trailingPartial) return null;
    const value = decodeTagQueryValue(term.raw.slice(TAG_PREFIX.length)).trim();
    if (!value) return null;
    return { ...term, kind: "tag", label: `${TAG_PREFIX}${value}`, value };
  }
  if (lower.startsWith(USER_PREFIX)) {
    if (trailingPartial) return null;
    const value = term.raw.slice(USER_PREFIX.length).trim();
    if (!value) return null;
    return { ...term, kind: "user", label: `${USER_PREFIX}${value}`, value };
  }
  return null;
}

// Produces alternating editable text and atomic structured terms. Empty text
// tokens at the edges are deliberate insertion points before/after a pill.
export function tokenizeConversationQuery(raw: string): ConversationQueryToken[] {
  const scanned = scanConversationQuery(raw);
  const structured: StructuredQueryToken[] = [];
  scanned.terms.forEach((term, index) => {
    const token = structuredToken(term, scanned.endsMidTerm && index === scanned.terms.length - 1);
    if (token) structured.push(token);
  });

  const tokens: ConversationQueryToken[] = [];
  let textStart = 0;
  for (const token of structured) {
    tokens.push({
      kind: "text",
      raw: raw.slice(textStart, token.start),
      start: textStart,
      end: token.start,
    });
    tokens.push(token);
    textStart = token.end;
  }
  tokens.push({
    kind: "text",
    raw: raw.slice(textStart),
    start: textStart,
    end: raw.length,
  });
  return tokens;
}

export function serializeConversationQuery(tokens: readonly ConversationQueryToken[]): string {
  return tokens.map((token) => token.raw).join("");
}

export interface RemovedConversationQueryToken {
  query: string;
  caret: number;
}

// Removes one exact atomic term and one adjoining separator. Prefer the
// following separator so text before and after the removed pill keep a single
// natural boundary.
export function removeConversationQueryToken(
  raw: string,
  tokenStart: number,
): RemovedConversationQueryToken {
  const token = tokenizeConversationQuery(raw).find(
    (candidate) => candidate.kind !== "text" && candidate.start === tokenStart,
  );
  if (!token) throw new Error(`No structured query token starts at ${tokenStart}`);

  let start = token.start;
  let end = token.end;
  if (end < raw.length && /\s/.test(raw[end])) {
    while (end < raw.length && /\s/.test(raw[end])) end += 1;
  } else {
    while (start > 0 && /\s/.test(raw[start - 1])) start -= 1;
  }
  return { query: raw.slice(0, start) + raw.slice(end), caret: start };
}

// Escape clears editable text/partials while retaining committed pills in
// their lexical order.
export function clearConversationQueryText(raw: string): string {
  const structured = tokenizeConversationQuery(raw).filter((token) => token.kind !== "text");
  return structured.length === 0 ? "" : structured.map((token) => token.raw).join(" ") + " ";
}
