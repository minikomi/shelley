import { strict as assert } from "node:assert";
import { formatFinishedToolDuration, formatRunningToolDuration } from "./toolDuration";

assert.equal(formatRunningToolDuration(-1), "0s");
assert.equal(formatRunningToolDuration(999), "0s");
assert.equal(formatRunningToolDuration(12_345), "12s");
assert.equal(formatRunningToolDuration(62_000), "1m 2s");
assert.equal(formatRunningToolDuration(3_723_000), "1h 2m 3s");

assert.equal(formatFinishedToolDuration(20), "<1s");
assert.equal(formatFinishedToolDuration(20_049), "20s");
assert.equal(formatFinishedToolDuration(20_950), "21s");
assert.equal(formatFinishedToolDuration(62_000), "1m 2s");

console.log("tool duration tests passed");
