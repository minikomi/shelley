import {
  activeStructuredQueryEdit,
  bareStructuredModifierAtCaret,
  isBareStructuredQueryEdit,
  omitStructuredQueryEdit,
  removeConversationQueryToken,
  removeConversationQueryTerm,
  replaceStructuredQueryEdit,
  serializeConversationQuery,
  tokenizeConversationQuery,
} from "./conversationQuery";

function assert(condition: boolean, message: string): asserts condition {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

function assertEqual(actual: unknown, expected: unknown, message: string): void {
  const got = JSON.stringify(actual);
  const want = JSON.stringify(expected);
  if (got !== want) throw new Error(`Assertion failed: ${message} (got ${got}, want ${want})`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (error) {
    console.error(`\u2717 ${name}`);
    throw error;
  }
}

run("tokenizes interleaved text and committed terms in lexical order", () => {
  const raw = "test user:aaa@x.com tag:aaa big";
  const tokens = tokenizeConversationQuery(raw);
  assertEqual(
    tokens.map((token) => [token.kind, token.raw]),
    [
      ["text", "test "],
      ["user", "user:aaa@x.com"],
      ["text", " "],
      ["tag", "tag:aaa"],
      ["text", " big"],
    ],
    "ordered tokens",
  );
  assert(serializeConversationQuery(tokens) === raw, "serialization is exact");
});

run("preserves quoted tag syntax while displaying a literal tag label", () => {
  const raw = 'before  tag:"in progress"  after';
  const tokens = tokenizeConversationQuery(raw);
  const tag = tokens.find((token) => token.kind === "tag");
  assert(tag?.kind === "tag", "tag token found");
  assert(tag?.raw === 'tag:"in progress"', "raw quoted term is retained");
  assert(tag?.label === "tag:in progress", "pill label omits query quotes");
  assert(serializeConversationQuery(tokens) === raw, "spacing and quotes round-trip");
});

run("keeps a trailing tag or user prefix editable", () => {
  assertEqual(
    tokenizeConversationQuery("tag:").map((token) => token.kind),
    ["text"],
    "bare tag prefix",
  );
  assertEqual(
    tokenizeConversationQuery("user:aaa@x.com").map((token) => token.kind),
    ["text"],
    "trailing user prefix",
  );
  assertEqual(
    tokenizeConversationQuery("tag:aaa ").map((token) => token.kind),
    ["text", "tag", "text"],
    "space commits a tag",
  );
});

run("state terms are atomic even at the end of a query", () => {
  assertEqual(
    tokenizeConversationQuery("is:untagged").map((token) => token.kind),
    ["text", "untagged", "text"],
    "untagged state",
  );
  assertEqual(
    tokenizeConversationQuery("is:unattributed").map((token) => token.kind),
    ["text", "unattributed", "text"],
    "unattributed state",
  );
});

run("atomic removal removes one pill and an adjoining separator", () => {
  const raw = "test user:aaa@x.com tag:aaa big";
  const tokens = tokenizeConversationQuery(raw);
  const user = tokens.find((token) => token.kind === "user");
  const tag = tokens.find((token) => token.kind === "tag");
  assert(user?.kind === "user", "user token found");
  assert(tag?.kind === "tag", "tag token found");
  assert(
    removeConversationQueryToken(raw, user.start).query === "test tag:aaa big",
    "middle user removed whole",
  );
  assert(
    removeConversationQueryToken(raw, tag.start).query === "test user:aaa@x.com big",
    "middle tag removed whole",
  );
});

run("atomic removal handles edge pills", () => {
  const first = "tag:aaa text";
  assert(
    removeConversationQueryToken(first, 0).query === "text",
    "leading pill and following separator",
  );
  const last = "text user:aaa@x.com ";
  const user = tokenizeConversationQuery(last).find((token) => token.kind === "user");
  assert(user?.kind === "user", "trailing user found");
  assert(
    removeConversationQueryToken(last, user.start).query === "text ",
    "trailing pill and separator",
  );
});

run("tracks and completes an unwrapped structured term at its lexical range", () => {
  const raw = "left tag:alpha right user:kept@example.com ";
  const edit = { kind: "tag" as const, start: 5, end: 14 };
  assertEqual(
    activeStructuredQueryEdit(raw, edit),
    { ...edit, prefix: "alpha" },
    "active tag prefix",
  );
  assert(
    omitStructuredQueryEdit(raw, edit) === "left  right user:kept@example.com ",
    "parsing can omit only the active term",
  );
  assertEqual(
    replaceStructuredQueryEdit(raw, edit, "tag:alpine"),
    {
      query: "left tag:alpine right user:kept@example.com ",
      caret: 15,
    },
    "completion replaces the exact middle range",
  );
  const userEdit = { kind: "user" as const, start: 21, end: 42 };
  assertEqual(
    activeStructuredQueryEdit(raw, userEdit),
    { ...userEdit, prefix: "kept@example.com" },
    "active user prefix",
  );
});

run("recognizes and removes a bare modifier atomically", () => {
  const raw = "left tag: right";
  const edit = { kind: "tag" as const, start: 5, end: 9 };
  assert(isBareStructuredQueryEdit(raw, edit), "bare active modifier");
  assertEqual(
    bareStructuredModifierAtCaret(raw, edit.end, "backward"),
    { raw: "tag:", start: 5, end: 9 },
    "backward caret finds bare modifier",
  );
  assertEqual(
    bareStructuredModifierAtCaret(raw, edit.start, "forward"),
    { raw: "tag:", start: 5, end: 9 },
    "forward caret finds bare modifier",
  );
  assertEqual(
    removeConversationQueryTerm(raw, edit.start, edit.end),
    { query: "left right", caret: 5 },
    "bare modifier and one separator are removed",
  );
  assert(
    isBareStructuredQueryEdit("user:", { kind: "user", start: 0, end: 5 }),
    "bare user modifier",
  );
});

console.log("\nconversationQuery tests passed");
