import assert from "node:assert/strict";
import { test } from "node:test";
import type { ConversationWithState } from "../types";
import { runningSubagents } from "./runningSubagents";

function conversation(
  id: string,
  parent: string | null,
  working = false,
  jobs = 0,
): ConversationWithState {
  return {
    conversation_id: id,
    parent_conversation_id: parent,
    working,
    running_background_jobs: jobs,
  } as ConversationWithState;
}

test("includes busy descendants through idle ancestors, but not unrelated work", () => {
  const conversations = [
    conversation("grandchild", "idle-child", false, 1),
    conversation("unrelated-child", "unrelated", true, 2),
    conversation("root", null, true, 1),
    conversation("idle-child", "root"),
    conversation("working-child", "root", true),
    conversation("finished-child", "root"),
  ];
  assert.deepEqual(
    runningSubagents(conversations, "root").map((c) => c.conversation_id),
    ["working-child", "grandchild"],
  );
  assert.deepEqual(
    runningSubagents(conversations, "idle-child").map((c) => c.conversation_id),
    ["grandchild"],
  );
  assert.deepEqual(runningSubagents(conversations, "grandchild"), []);
});
