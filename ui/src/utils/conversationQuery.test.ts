import {
  removeConversationQueryToken,
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

console.log("\nconversationQuery tests passed");
