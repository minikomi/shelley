// Run with: tsx src/utils/unavailableModelLabel.test.ts
import { unavailableModelLabel } from "./modelNames";

function eq(actual: string, expected: string) {
  if (actual !== expected) throw new Error(`expected ${expected}, got ${actual}`);
}
const b64 = (s: string) => Buffer.from(s).toString("base64url");

eq(
  unavailableModelLabel(`provider:${b64("abc")}:${b64("org/model:tag")}`),
  "org/model:tag (not listed)",
);
eq(
  unavailableModelLabel(`provider:${b64("p")}:${b64("claude-sonnet-4-5")}`),
  "claude-sonnet-4-5 (not listed)",
);
eq(
  unavailableModelLabel(`provider:${b64("p")}:${b64("glm-4.5")}`, new Map([["p", "api.z.ai"]])),
  "glm-4.5 · api.z.ai (not listed)",
);
eq(
  unavailableModelLabel(`provider:${b64("gone")}:${b64("glm-4.5")}`, new Map([["p", "api.z.ai"]])),
  "glm-4.5 (not listed)",
);
eq(unavailableModelLabel("claude-opus-4.8"), "claude-opus-4.8");
eq(unavailableModelLabel("provider:@@:@@"), "provider:@@:@@");
eq(unavailableModelLabel("provider:abc"), "provider:abc");
console.log("✓ unavailableModelLabel");
