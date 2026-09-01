import assert from "node:assert/strict";
import { createDraftAutosave } from "./draftAutosave";

interface Deferred {
  resolve: () => void;
  promise: Promise<void>;
}

function deferred(): Deferred {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { resolve, promise };
}

const requests: Array<{ value: string; done: Deferred }> = [];
const autosave = createDraftAutosave((value) => {
  const done = deferred();
  requests.push({ value, done });
  return done.promise;
});

autosave.schedule("repeat");
autosave.flush();
assert.equal(requests.length, 1);

autosave.reset();
autosave.schedule("repeat");
autosave.flush();
assert.equal(requests.length, 2, "a fresh generation saves identical text during the old request");

requests[1].done.resolve();
await Promise.resolve();
await Promise.resolve();
requests[0].done.resolve();
await Promise.resolve();
await Promise.resolve();

autosave.schedule("repeat");
autosave.flush();
assert.equal(requests.length, 2, "the stale completion does not replace fresh save bookkeeping");

autosave.reset();
autosave.schedule("stale");
autosave.flush();
autosave.reset();
autosave.schedule("fresh");
autosave.flush();
assert.equal(requests.length, 4);

requests[3].done.resolve();
await Promise.resolve();
await Promise.resolve();
requests[2].done.resolve();
await Promise.resolve();
await Promise.resolve();

autosave.schedule("fresh");
autosave.flush();
assert.equal(requests.length, 4, "an old generation cannot overwrite the fresh last-saved value");

console.log("draftAutosave: all tests passed");
