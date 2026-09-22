import assert from "node:assert/strict";

const storage = new Map<string, string>([["shelley-live-preview-path", "//untrusted.test"]]);
Object.defineProperty(globalThis, "localStorage", {
  configurable: true,
  value: {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
  },
});
Object.defineProperty(globalThis, "window", {
  configurable: true,
  value: {
    location: { origin: "https://shelley.test" },
  },
});

const {
  closeLivePreview,
  isPreviewCommand,
  livePreviewOpen,
  livePreviewPath,
  livePreviewPort,
  livePreviewRevision,
  mostRecentPreviewEndpoint,
  openLivePreview,
  parsePreviewCommand,
  previewEndpointFromText,
  previewPortError,
  previewURL,
  refreshLivePreview,
  syncLivePreviewPath,
} = await import("./livePreview");

assert.equal(livePreviewPath.value, "/");
assert.throws(() => parsePreviewCommand("hello"));
assert.equal(isPreviewCommand("/previewing"), false);
assert.equal(isPreviewCommand("/preview 8001"), true);
assert.deepEqual(parsePreviewCommand("/preview"), { action: "open" });
assert.deepEqual(parsePreviewCommand("/preview off"), { action: "close" });
assert.deepEqual(parsePreviewCommand("/preview 8001"), { action: "open", port: 8001, path: "/" });
assert.deepEqual(parsePreviewCommand("/preview 8001 docs"), {
  action: "open",
  port: 8001,
  path: "/docs",
});
assert.deepEqual(parsePreviewCommand("/preview 8001 /docs?tab=one#top"), {
  action: "open",
  port: 8001,
  path: "/docs?tab=one#top",
});
for (const command of [
  "/preview on",
  "/preview 8001 extra args",
  "/preview off /docs",
  "/preview 8001 //other.test",
  "/preview 8001 https://other.test",
  "/preview 8001 /bad\\path",
  "/preview 8001 /bad path",
  "/preview 80",
]) {
  assert.throws(() => parsePreviewCommand(command));
}

for (const separator of [" ", "\t", "\n", "\r\n", " \t "]) {
  assert.equal(isPreviewCommand(`/preview${separator}8001`), true);
  assert.deepEqual(parsePreviewCommand(` /preview${separator}8001 docs `), {
    action: "open",
    port: 8001,
    path: "/docs",
  });
}

assert.equal(previewURL(8000), "/__preview/8000/");
assert.equal(previewURL(8000, "/docs"), "/__preview/8000/docs");
assert.equal(previewURL(8000, "/docs?q=1#top"), "/__preview/8000/docs?q=1#top");
assert.equal(previewPortError(2999), "Preview ports must be between 3000 and 9999");
assert.equal(previewPortError(8000), null);

assert.deepEqual(previewEndpointFromText("running at http://localhost:8001/docs?q=1#top"), {
  port: 8001,
  path: "/docs?q=1#top",
});
assert.deepEqual(previewEndpointFromText("visit https://demo.test:8002/nested/page"), {
  port: 8002,
  path: "/nested/page",
});
assert.deepEqual(previewEndpointFromText("localhost:8003/docs"), { port: 8003, path: "/docs" });
assert.deepEqual(previewEndpointFromText("Use `/preview 8004 docs`"), {
  port: 8004,
  path: "/docs",
});
assert.deepEqual(previewEndpointFromText("/preview\t8005 /docs?x=1#section"), {
  port: 8005,
  path: "/docs?x=1#section",
});
assert.deepEqual(previewEndpointFromText("serve -port 8006"), { port: 8006, path: "/" });
assert.equal(previewEndpointFromText("issue 8003"), null);
assert.equal(previewEndpointFromText("/previewing 8001"), null);
assert.equal(previewEndpointFromText("/preview 80010"), null);
assert.equal(previewEndpointFromText("/preview 2999"), null);
assert.equal(previewEndpointFromText("/preview off"), null);
assert.equal(previewEndpointFromText("/preview 8001 //untrusted.test"), null);
assert.deepEqual(previewEndpointFromText("port 8001 then `/preview 8004 docs`"), {
  port: 8004,
  path: "/docs",
});
assert.deepEqual(previewEndpointFromText("`/preview 8004 docs` then port 8001"), {
  port: 8001,
  path: "/",
});
assert.deepEqual(previewEndpointFromText("https://one.test:8001/one then localhost:8002/two"), {
  port: 8002,
  path: "/two",
});
assert.deepEqual(
  previewEndpointFromText(
    "/preview 8001 /one then https://two.test:8002/two then localhost:8003/three",
  ),
  { port: 8003, path: "/three" },
);
assert.deepEqual(mostRecentPreviewEndpoint(["port 8001", "Use `/preview 8004 docs`"]), {
  port: 8004,
  path: "/docs",
});
assert.deepEqual(
  mostRecentPreviewEndpoint(["localhost:8001/first", "no endpoint here", "port 8005"]),
  {
    port: 8005,
    path: "/",
  },
);

openLivePreview(8006, "docs");
assert.equal(livePreviewOpen.value, true);
assert.equal(livePreviewPort.value, 8006);
assert.equal(livePreviewPath.value, "/docs");
assert.equal(storage.get("shelley-live-preview-port"), "8006");
assert.equal(storage.get("shelley-live-preview-path"), "/docs");
syncLivePreviewPath("https://shelley.test/__preview/8006/snacks/?q=1");
assert.equal(livePreviewPath.value, "/snacks/?q=1");
syncLivePreviewPath("https://shelley.test/__preview/8007/other");
assert.equal(livePreviewPath.value, "/snacks/?q=1");
syncLivePreviewPath("https://shelley.test/");
assert.equal(livePreviewPath.value, "/snacks/?q=1");
const revision = livePreviewRevision.value;
refreshLivePreview();
assert.equal(livePreviewRevision.value, revision + 1);
closeLivePreview();
assert.equal(livePreviewOpen.value, false);
assert.equal(livePreviewRevision.value, revision + 1);
