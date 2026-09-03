import type { ConversationWithState } from "../types";
import {
  filterConversationsByParticipantQuery,
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
const corpus = [current, shared, other, unattributed, empty];

assert(hasOtherParticipant(corpus, "me@example.com"), "detects another known participant");
assert(
  !hasOtherParticipant([current, unattributed, empty], "me@example.com"),
  "single-user and unattributed lists stay single-user",
);
assert(!hasOtherParticipant(corpus, ""), "an unauthenticated request cannot enable the filter");

const filtered = filterConversationsByParticipantQuery(corpus, ["me@example.com"], false);
assert(
  filtered.map((item) => item.conversation_id).join(",") === "current,shared",
  "matches a selected user",
);
assert(
  filterConversationsByParticipantQuery(corpus, ["OTHER@example.com"], false)
    .map((item) => item.conversation_id)
    .join(",") === "shared,other",
  "matches selected users case-insensitively",
);
assert(
  filterConversationsByParticipantQuery(corpus, ["me@example.com", "other@example.com"], false)
    .map((item) => item.conversation_id)
    .join(",") === "shared",
  "multiple users use AND semantics",
);
assert(
  filterConversationsByParticipantQuery(corpus, [], true)
    .map((item) => item.conversation_id)
    .join(",") === "unattributed,empty",
  "unattributed can stand alone",
);
assert(
  filterConversationsByParticipantQuery(corpus, ["me@example.com"], true).length === 0,
  "unattributed is mutually exclusive with user constraints",
);
assert(
  filterConversationsByParticipantQuery(corpus, [], false) === corpus,
  "an empty participant facet is a no-op",
);

console.log("✓ conversation participant detection and filtering");
