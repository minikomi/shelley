import assert from "node:assert/strict";
import { LiveTranscript } from "./liveTranscript";

const transcript = new LiveTranscript();
transcript.append(true, "Hi.");
transcript.append(false, "Hello");
transcript.append(false, " there.");
assert.deepEqual(transcript.turns, [
  { user: true, text: "Hi." },
  { user: false, text: "Hello there." },
]);

// The receipt and the saved row can arrive in either order.
transcript.handoff(3, "saved exchange");
transcript.reconcile([]);
assert.equal(transcript.turns.length, 2);
transcript.append(false, " Still here.");
transcript.reconcile(["saved exchange"]);
assert.deepEqual(transcript.turns, [{ user: false, text: " Still here." }]);

// Replaying a receipt must not consume speech that followed its snapshot.
transcript.handoff(3, "saved exchange");
transcript.reconcile(["saved exchange"]);
assert.deepEqual(transcript.turns, [{ user: false, text: " Still here." }]);

transcript.append(true, "Another request.");
transcript.reconcile(["saved exchange", "queued exchange"]);
assert.equal(transcript.turns.length, 2);
transcript.handoff(5, "queued exchange");
transcript.reconcile(["saved exchange", "queued exchange"]);
assert.deepEqual(transcript.turns, []);

// Deltas still count when they carry no visible text.
transcript.append(false, "");
transcript.append(false, "Next reply.");
transcript.handoff(6, "empty fragment");
transcript.reconcile(["empty fragment"]);
assert.deepEqual(transcript.turns, [{ user: false, text: "Next reply." }]);

// A new call starts with a fresh counter and no pending exchange.
assert.deepEqual(new LiveTranscript().turns, []);
console.log("liveTranscript tests passed");
