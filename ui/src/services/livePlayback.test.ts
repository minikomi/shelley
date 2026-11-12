import assert from "node:assert/strict";
import { LivePlaybackActivity } from "./livePlayback";

const playback = new LivePlaybackActivity();
const silence = new Float32Array(32);
const speech = new Float32Array(32).fill(0.02);
assert.equal(playback.sample(silence, 0), false);
assert.equal(playback.active, false);
assert.equal(playback.sample(speech, 50), true);
assert.equal(playback.active, true);
assert.equal(playback.sample(silence, 800), false, "hold a pause within speech");
assert.equal(playback.sample(speech, 900), false);
assert.equal(playback.sample(silence, 2099), false);
assert.equal(playback.sample(silence, 2100), true, "release only after sustained quiet");
assert.equal(playback.active, false);
assert.equal(playback.sample(silence, 2200), false, "report transitions only");
assert.equal(playback.sample(speech, 3000), true, "detect the next spoken response");
