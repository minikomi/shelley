import type { ConversationWithState } from "../types";
import {
  hasMultiParticipantConversation,
  hasOtherParticipant,
} from "./conversationParticipantFilter";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

function participant(email: string, message_count = 1) {
  return { email, message_count };
}

function conversation(
  id: string,
  participants?: { email: string; message_count: number }[] | null,
): ConversationWithState {
  return { conversation_id: id, participants } as ConversationWithState;
}

const current = conversation("current", [participant("me@example.com", 3)]);
const shared = conversation("shared", [
  participant("other@example.com", 2),
  participant("ME@example.com"),
]);
const other = conversation("other", [participant("other@example.com", 4)]);
const unattributed = conversation("unattributed");
const empty = conversation("empty", []);
const subagent = conversation("subagent", [participant("third@example.com")]);
subagent.parent_conversation_id = "current";
const draft = conversation("draft", []);
draft.is_draft = true;
const corpus = [current, shared, other, unattributed, empty, subagent, draft];

assert(hasOtherParticipant(corpus, "me@example.com"), "detects another known participant");
assert(
  !hasOtherParticipant([current, unattributed, empty], "me@example.com"),
  "single-user and unattributed lists stay single-user",
);
assert(!hasOtherParticipant(corpus, ""), "an unauthenticated request cannot enable the filter");

assert(hasMultiParticipantConversation(corpus), "detects a multi-participant conversation");
assert(
  !hasMultiParticipantConversation([current, other, unattributed, empty]),
  "disjoint single-user conversations are not multi-participant",
);

console.log("✓ conversation participant detection");
