import assert from "node:assert/strict";
import {
  LiveTranscript,
  projectLiveQueuedMessages,
  projectLiveTranscripts,
} from "./liveTranscript";
import { queuedMessageText, type LLMMessage, type Message, type QueuedMessage } from "../types";

const transcript = new LiveTranscript();
transcript.append(true, "Hi.", 0, 300);
transcript.append(false, "Hello", 400, 800);
transcript.append(false, " there.", 800, 1200);
assert.deepEqual(transcript.turns, [
  { user: true, text: "Hi." },
  { user: false, text: "Hello there." },
]);

// The receipt and the saved row can arrive in either order.
transcript.handoff(3, "saved exchange");
transcript.reconcile([]);
assert.equal(transcript.turns.length, 2);
transcript.append(false, " Still here.", 1300, 1700);
transcript.reconcile(["saved exchange"]);
assert.deepEqual(transcript.turns, [{ user: false, text: " Still here." }]);

// Replaying a receipt must not consume speech that followed its snapshot.
transcript.handoff(3, "saved exchange");
transcript.reconcile(["saved exchange"]);
assert.deepEqual(transcript.turns, [{ user: false, text: " Still here." }]);

transcript.append(true, "Another request.", 2000, 2500);
transcript.reconcile(["saved exchange", "queued exchange"]);
assert.equal(transcript.turns.length, 2);
transcript.handoff(5, "queued exchange");
transcript.reconcile(["saved exchange", "queued exchange"]);
assert.deepEqual(transcript.turns, []);

// Deltas still count when they carry no visible text.
transcript.append(false, "", 2600, 2600);
transcript.append(false, "Next reply.", 2700, 3000);
transcript.handoff(6, "empty fragment");
transcript.reconcile(["empty fragment"]);
assert.deepEqual(transcript.turns, [{ user: false, text: "Next reply." }]);

// A new call starts with a fresh counter and no pending exchange.
assert.deepEqual(new LiveTranscript().turns, []);

const overlapping = new LiveTranscript();
overlapping.append(true, "The transcript keeps", 0, 1200);
overlapping.append(false, "Mm-hmm.", 900, 1300);
overlapping.append(true, " splitting up.", 1300, 2500);
assert.deepEqual(overlapping.turns, [
  { user: true, text: "The transcript keeps splitting up." },
  { user: false, text: "Mm-hmm." },
]);
overlapping.append(false, "I see.", 3000, 4000);
overlapping.append(true, "A new thought.", 5000, 6000);
assert.deepEqual(overlapping.turns.slice(2), [
  { user: false, text: "I see." },
  { user: true, text: "A new thought." },
]);

// Provider delivery order does not determine the visible speaker ordering.
const delayed = new LiveTranscript();
delayed.append(false, "Hey!", 500, 800);
delayed.append(true, "Hey Shelley, ", 0, 1000);
delayed.append(true, "how's it going?", 1000, 2000);
assert.deepEqual(delayed.turns, [
  { user: true, text: "Hey Shelley, how's it going?" },
  { user: false, text: "Hey!" },
]);
const sparse = new LiveTranscript();
sparse.append(true, "A new request.", 4000, 5000);
sparse.append(false, "The old reply's tail.", 2000, 3000);
sparse.consume([2]);
assert.deepEqual(sparse.turns, [{ user: true, text: "A new request." }]);
sparse.append(true, "Late old instruction.", 0, 1000);
sparse.handoff(3, "continued request", [3]);
sparse.reconcile([]);
assert.equal(sparse.turns.length, 2);
sparse.reconcile(["continued request"]);
assert.deepEqual(sparse.turns, [{ user: true, text: "A new request." }]);
sparse.consume([2, 3]);
assert.deepEqual(sparse.turns, [{ user: true, text: "A new request." }]);

const savedText =
  "Spoken request:\nCheck the panel\n\nVoice exchange (for context only):\nUSER: Check the panel\nLIVE: Make sure the live chat";
const savedLLM: LLMMessage = { Role: 0, Content: [{ ID: "", Type: 2, Text: savedText }] };
const original = {
  message_id: "request",
  type: "user",
  llm_data: JSON.stringify(savedLLM),
} as Message;
const extension = {
  message_id: "extension",
  type: "livetranscript",
  user_data: JSON.stringify({
    session_id: "call",
    message: savedText,
    fragments: [
      { n: 4, user: false, text: " panel doesn't", start_ms: 2000, end_ms: 3000 },
      { n: 5, user: false, text: " get too tall.", start_ms: 3000, end_ms: 4000 },
    ],
  }),
} as Message;
const expectedText = savedText + " panel doesn't get too tall.";
const projected = projectLiveTranscripts([original, extension, extension]);
assert.equal(projected.length, 1);
assert.equal(JSON.parse(projected[0].llm_data!).Content[0].Text, expectedText);
assert.equal(JSON.parse(original.llm_data!).Content[0].Text, savedText);
assert.deepEqual(projectLiveTranscripts([extension]), []);
assert.equal(projectLiveTranscripts([original])[0], original);

const queued = [{ id: "queued", llm: savedLLM }] as QueuedMessage[];
const projectedQueue = projectLiveQueuedMessages(queued, [extension]);
assert.equal(queuedMessageText(projectedQueue[0]), expectedText);
assert.equal(queuedMessageText(queued[0]), savedText);
assert.equal(projectLiveQueuedMessages(queued, []), queued);
console.log("liveTranscript tests passed");
