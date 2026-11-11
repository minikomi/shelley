import { expect, test, type Page } from "@playwright/test";

// Exercise the real AudioContext and AudioWorklet with Chromium's microphone
// fixture. Only the provider transport is simulated; no timers or sleeps.
test.use({
  launchOptions: { args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream"] },
  permissions: ["microphone"],
});

async function enableStreaming(page: Page) {
  await page.addInitScript(() => {
    let init: Record<string, unknown>;
    Object.defineProperty(window, "__SHELLEY_INIT__", {
      configurable: true,
      get: () => init,
      set: (value) => { init = { ...value, streaming_transcription: true, transcription_available: false }; },
    });
  });
}

async function microphone(page: Page) {
  await page.goto("/new");
  await expect(page.getByTestId("message-input")).toBeVisible();
  await page.getByTestId("voice-button").click();
}

test("streams audio and partial text before Stop, then sends the final transcript", async ({ page }, testInfo) => {
  await enableStreaming(page);
  let bytes = 0;
  let commits = 0;
  let uploads = 0;
  await page.route("**/api/upload/raw?*", (route) => { uploads++; return route.abort(); });
  await page.routeWebSocket("**/transcription/stream", (socket) => {
    socket.onMessage((message) => {
      if (typeof message !== "string") {
        bytes += message.length;
        if (bytes === message.length) socket.send(JSON.stringify({ type: "delta", text: "echo: streaming" }));
      } else {
        expect(JSON.parse(message)).toEqual({ type: "commit" });
        commits++;
        socket.send(JSON.stringify({ type: "completed", text: "echo: streaming microphone works" }));
      }
    });
    socket.send(JSON.stringify({ type: "ready" }));
  });
  await microphone(page);
  await expect(page.getByTestId("recording-transcript")).toHaveText("echo: streaming");
  expect(bytes).toBeGreaterThanOrEqual(4800);
  expect(commits).toBe(0);
  await page.screenshot({ path: testInfo.outputPath("streaming.png") });
  const chat = page.waitForRequest((r) => r.url().endsWith("/chat") && r.method() === "POST");
  await page.getByTestId("recording-stop-button").click();
  expect((await chat).postDataJSON().message).toBe("echo: streaming microphone works");
  await expect(page.getByTestId("recording-panel")).toHaveCount(0);
  expect(commits).toBe(1);
  expect(uploads).toBe(0);
});

test("cancels streaming without committing or sending a message", async ({ page }) => {
  await enableStreaming(page);
  let closed = false;
  let commits = 0;
  let chats = 0;
  page.on("request", (r) => { if (r.url().endsWith("/chat")) chats++; });
  await page.routeWebSocket("**/transcription/stream", (socket) => {
    socket.onMessage((message) => {
      if (typeof message === "string") commits++;
      else socket.send(JSON.stringify({ type: "delta", text: "Unsent speech" }));
    });
    socket.onClose(() => { closed = true; });
    socket.send(JSON.stringify({ type: "ready" }));
  });
  await microphone(page);
  await expect(page.getByTestId("recording-transcript")).toContainText("Unsent speech");
  await page.getByTestId("recording-cancel-button").click();
  await expect(page.getByTestId("recording-panel")).toHaveCount(0);
  await expect.poll(() => closed).toBe(true);
  expect(commits).toBe(0);
  expect(chats).toBe(0);
});

test("shows provider failure and does not upload a recording", async ({ page }) => {
  await enableStreaming(page);
  let uploads = 0;
  await page.route("**/api/upload/raw?*", (route) => { uploads++; return route.abort(); });
  await page.routeWebSocket("**/transcription/stream", (socket) => {
    socket.send(JSON.stringify({ type: "error", message: "API key expired" }));
  });
  await microphone(page);
  await expect(page.getByTestId("recording-error")).toHaveText("API key expired");
  await expect(page.getByTestId("recording-retry-button")).toBeVisible();
  expect(uploads).toBe(0);
  await page.getByTestId("recording-cancel-button").click();
  await expect(page.getByTestId("recording-panel")).toHaveCount(0);
});

test("retains the final transcript for retry when sending fails", async ({ page }) => {
  await enableStreaming(page);
  let connections = 0;
  let sends = 0;
  await page.routeWebSocket("**/transcription/stream", (socket) => {
    connections++;
    socket.onMessage((message) => {
      socket.send(JSON.stringify(typeof message === "string"
        ? { type: "completed", text: "echo: preserved speech" }
        : { type: "delta", text: "speech" }));
    });
    socket.send(JSON.stringify({ type: "ready" }));
  });
  await page.route("**/api/conversation/*/chat", async (route) => {
    sends++;
    expect(route.request().postDataJSON().message).toBe("Keep my note\n\necho: preserved speech");
    if (sends === 1) await route.fulfill({ status: 503, body: "Sending unavailable" });
    else await route.fulfill({ status: 202, contentType: "application/json", body: '{"status":"queued"}' });
  });
  await page.goto("/new");
  await page.getByTestId("message-input").fill("Keep my note");
  await page.getByTestId("voice-button").click();
  await expect(page.getByTestId("recording-transcript")).toContainText("speech");
  await page.getByTestId("recording-stop-button").click();
  await expect(page.getByTestId("recording-error")).toContainText("Sending unavailable");
  await expect(page.getByTestId("recording-transcript")).toHaveText("echo: preserved speech");
  await page.getByTestId("recording-retry-button").click();
  await expect(page.getByTestId("recording-panel")).toHaveCount(0);
  expect(connections).toBe(1);
  expect(sends).toBe(2);
});

test("retries a failed destination before starting the microphone stream", async ({ page }) => {
  await enableStreaming(page);
  let drafts = 0;
  await page.route("**/api/conversations/draft", async (route) => {
    drafts++;
    if (drafts === 1) await route.fulfill({ status: 503, body: "Draft unavailable" });
    else await route.continue();
  });
  await page.routeWebSocket("**/transcription/stream", (socket) => {
    socket.onMessage(() => socket.send(JSON.stringify({ type: "delta", text: "Recovered" })));
    socket.send(JSON.stringify({ type: "ready" }));
  });
  await microphone(page);
  await expect(page.getByTestId("recording-error")).toContainText("Draft unavailable");
  await page.getByTestId("recording-retry-button").click();
  await expect(page.getByTestId("recording-transcript")).toContainText("Recovered");
  expect(drafts).toBe(2);
  await page.getByTestId("recording-cancel-button").click();
  await expect(page.getByTestId("recording-panel")).toHaveCount(0);
});
