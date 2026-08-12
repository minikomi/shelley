import type { Conversation } from "../types";
import { compareTagGroupKeys, foldTag, tagGroupKey, tagGroupLabel } from "./tagFilter";

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}

function assertEqual(got: string[], want: string[], msg: string): void {
  const g = JSON.stringify(got);
  const w = JSON.stringify(want);
  if (g !== w) throw new Error(`Assertion failed: ${msg} (got ${g}, want ${w})`);
}

function run(name: string, fn: () => void): void {
  try {
    fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}

function conv(id: string, tags: string[] | string | null): Conversation {
  return {
    conversation_id: id,
    slug: id,
    user_initiated: true,
    created_at: "2026-05-10T12:00:00Z",
    updated_at: "2026-05-10T12:00:00Z",
    cwd: null,
    archived: false,
    parent_conversation_id: null,
    tags: tags === null ? "" : typeof tags === "string" ? tags : JSON.stringify(tags),
  } as unknown as Conversation;
}

run("foldTag trims and lowercases", () => {
  assert(foldTag("  Infra ") === "infra", "fold");
  assert(foldTag("   ") === "", "blank folds to empty");
});

run("tagGroupKey is order- and case-insensitive, and null when untagged", () => {
  assert(
    tagGroupKey(conv("a", ["infra", "urgent"])) === tagGroupKey(conv("b", ["urgent", "infra"])),
    "order",
  );
  assert(tagGroupKey(conv("c", ["Infra"])) === tagGroupKey(conv("d", ["infra"])), "case");
  assert(tagGroupKey(conv("e", [])) === null, "untagged has no group");
  assert(
    tagGroupKey(conv("d", ["infra"])) !== tagGroupKey(conv("a", ["infra", "urgent"])),
    "distinct sets differ",
  );
});

run("tagGroupLabel sorts and keeps original casing", () => {
  assert(
    tagGroupLabel(conv("f", ["urgent", "Infra"])) === "#Infra #urgent",
    "sorted, cased, hashed",
  );
  assert(tagGroupLabel(conv("g", ["a", "A"])) === "#a", "folded duplicates collapse");
});

run("tag groups order by first tag, then second", () => {
  const key = (tags: string[]) => tagGroupKey(conv("x", tags))!;
  const got = [key(["b", "d"]), key(["b"]), key(["a", "b"]), key(["b", "c"])]
    .sort(compareTagGroupKeys)
    .map((k) =>
      k
        .split("\u0000")
        .map((t) => `#${t}`)
        .join(" "),
    );
  // A set that is a prefix of another comes first, so the broad #b group sits
  // directly above the narrower groups extending it.
  assertEqual(got, ["#a #b", "#b", "#b #c", "#b #d"], "ordered by tag sequence");
});

run("tag group ordering is total and prefix-aware", () => {
  const key = (tags: string[]) => tagGroupKey(conv("x", tags))!;
  assert(compareTagGroupKeys(key(["b"]), key(["b", "c"])) < 0, "prefix sorts first");
  assert(compareTagGroupKeys(key(["b", "c"]), key(["b"])) > 0, "and is antisymmetric");
  assert(compareTagGroupKeys(key(["a", "z"]), key(["b"])) < 0, "first tag dominates");
  assert(compareTagGroupKeys(key(["a"]), key(["a"])) === 0, "equal keys tie");
});

console.log("\ntagFilter tests passed");
