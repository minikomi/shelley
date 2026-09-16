import { expect, test, type APIRequestContext, type Page, type Route } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

async function installMediaMocks(page: Page, screenCapture = true) {
  await page.addInitScript((screenCaptureAvailable) => {
    const mock = {
      displayRequests: 0,
      microphoneRequests: 0,
      audioSources: 0,
      stoppedTracks: 0,
      displayError: "",
    };

    class MockTrack extends EventTarget {
      kind: string;
      constructor(kind: string) {
        super();
        this.kind = kind;
      }
      stop() {
        mock.stoppedTracks++;
      }
    }

    class MockStream {
      tracks: MockTrack[];
      constructor(tracks: MockTrack[] = []) {
        this.tracks = tracks;
      }
      getTracks() {
        return this.tracks;
      }
      getAudioTracks() {
        return this.tracks.filter((track) => track.kind === "audio");
      }
      getVideoTracks() {
        return this.tracks.filter((track) => track.kind === "video");
      }
    }

    class MockMediaRecorder {
      static isTypeSupported() {
        return true;
      }
      state: RecordingState = "inactive";
      mimeType: string;
      ondataavailable: ((event: BlobEvent) => void) | null = null;
      onstop: (() => void) | null = null;
      onerror: ((event: Event) => void) | null = null;
      constructor(stream: MediaStream, options?: MediaRecorderOptions) {
        const hasVideo = stream.getVideoTracks().length > 0;
        this.mimeType = options?.mimeType ?? (hasVideo ? "video/webm" : "audio/webm");
      }
      start(timeslice?: number) {
        if (timeslice !== 1000) throw new Error(`unexpected timeslice ${timeslice}`);
        this.state = "recording";
        queueMicrotask(() => this.emitChunk("first"));
        queueMicrotask(() => this.emitChunk("second"));
      }
      stop() {
        this.state = "inactive";
        this.emitChunk("last");
        queueMicrotask(() => this.onstop?.());
      }
      emitChunk(value: string) {
        const event = new Event("dataavailable") as BlobEvent;
        Object.defineProperty(event, "data", {
          value: new Blob([value], { type: this.mimeType }),
        });
        this.ondataavailable?.(event);
      }
    }

    class MockAudioContext {
      state: AudioContextState = "running";
      createMediaStreamDestination() {
        return { stream: new MockStream([new MockTrack("audio")]) };
      }
      createMediaStreamSource() {
        mock.audioSources++;
        return { connect() {} };
      }
      async resume() {}
      async close() {}
    }

    const mediaSources = new WeakMap<HTMLMediaElement, unknown>();
    Object.defineProperty(HTMLMediaElement.prototype, "srcObject", {
      configurable: true,
      get() {
        return mediaSources.get(this);
      },
      set(value) {
        mediaSources.set(this, value);
      },
    });
    Object.defineProperty(window, "MediaStream", { value: MockStream, configurable: true });
    Object.defineProperty(window, "MediaRecorder", {
      value: MockMediaRecorder,
      configurable: true,
    });
    Object.defineProperty(window, "AudioContext", {
      value: MockAudioContext,
      configurable: true,
    });
    const mediaDevices: {
      getUserMedia: () => Promise<MockStream>;
      getDisplayMedia?: () => Promise<MockStream>;
    } = {
      async getUserMedia() {
        mock.microphoneRequests++;
        return new MockStream([new MockTrack("audio")]);
      },
    };
    if (screenCaptureAvailable) {
      mediaDevices.getDisplayMedia = async () => {
        mock.displayRequests++;
        if (mock.displayError) throw new DOMException(mock.displayError, "NotAllowedError");
        return new MockStream([new MockTrack("video"), new MockTrack("audio")]);
      };
    }
    Object.defineProperty(navigator, "mediaDevices", {
      configurable: true,
      value: mediaDevices,
    });
    Object.defineProperty(window, "__recordingMock", { value: mock, configurable: true });
  }, screenCapture);
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function fulfillJSON(route: Route, json: Record<string, unknown>, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(json) });
}

interface QueuedMessageFixture {
  id: string;
  llm: { Role: number; Content: Array<{ ID: string; Type: number; Text: string }> };
  created_at: string;
  model: string;
  kind?: "transcription";
  state?: "working" | "ready" | "failed";
  transcription?: {
    media_path: string;
    child_conversation_id: string;
    context?: string;
  };
  error?: string;
}

function queuedMessage(
  id: string,
  text: string,
  extra: Partial<QueuedMessageFixture> = {},
): QueuedMessageFixture {
  return {
    id,
    llm: { Role: 0, Content: [{ ID: "", Type: 2, Text: text }] },
    created_at: "2026-09-12T00:00:00Z",
    model: "predictable",
    ...extra,
  };
}

async function openQueuedConversation(
  page: Page,
  request: APIRequestContext,
  queued: QueuedMessageFixture[],
  distilling = false,
): Promise<string> {
  const { conversationId, slug } = await createConversationViaAPIWithDetails(
    request,
    "echo: recording queue seed",
  );
  await page.route("**/api/stream2*", (route) => route.abort());
  await page.route("**/api/conversations/snapshot", async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as {
      conversations?: Array<{ conversation_id: string; queued_messages?: string }>;
    };
    const conversation = body.conversations?.find(
      (candidate) => candidate.conversation_id === conversationId,
    );
    if (conversation) conversation.queued_messages = JSON.stringify(queued);
    await route.fulfill({ response, json: body });
  });
  await page.route("**/api/conversation/*", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() !== "GET" || !/^\/api\/conversation\/[^/]+$/.test(url.pathname)) {
      await route.fallback();
      return;
    }
    const response = await route.fetch();
    const body = (await response.json()) as {
      conversation?: { queued_messages?: string };
      messages?: Array<Record<string, unknown>>;
    };
    if (body.conversation) body.conversation.queued_messages = JSON.stringify(queued);
    if (distilling) {
      body.messages = [
        ...(body.messages ?? []),
        {
          message_id: "distill-in-progress",
          conversation_id: conversationId,
          sequence_id: 999,
          type: "agent",
          user_data: JSON.stringify({
            distill_status: "in_progress",
            distill_method: "compact",
          }),
          created_at: "2026-09-12T00:00:00Z",
          generation: 1,
        },
      ];
    }
    await route.fulfill({ response, json: body });
  });
  await page.goto(`/c/${slug}`);
  await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30_000 });
  return slug;
}

test.describe("media recording composer", () => {
  test.beforeEach(async ({ page }) => {
    await installMediaMocks(page);
  });

  test("starts microphone recording inline and submits only the transcription command", async ({
    page,
  }) => {
    let uploadedFilename = "";
    let uploadedBody = "";
    const chatBodies: Record<string, unknown>[] = [];
    const transcriptionRequested = deferred();
    const releaseAcceptance = deferred();

    await page.route("**/api/upload/raw?filename=*", async (route) => {
      expect(route.request().method()).toBe("POST");
      uploadedFilename = new URL(route.request().url()).searchParams.get("filename") ?? "";
      uploadedBody = route.request().postDataBuffer()?.toString() ?? "";
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/recording.webm" });
    });
    await page.route("**/api/conversation/*/chat", async (route) => {
      chatBodies.push(route.request().postDataJSON() as Record<string, unknown>);
      transcriptionRequested.resolve();
      await releaseAcceptance.promise;
      await fulfillJSON(route, { status: "queued" }, 202);
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/new");
    const inputBeforeRecording = page.getByTestId("message-input");
    await inputBeforeRecording.fill("Keep this note with the recording.");
    await inputBeforeRecording.evaluate((input) => {
      const file = new File([new Blob(["image"], { type: "image/png" })], "context.png", {
        type: "image/png",
      });
      const transfer = new DataTransfer();
      transfer.items.add(file);
      input.dispatchEvent(
        new ClipboardEvent("paste", { clipboardData: transfer, bubbles: true, cancelable: true }),
      );
    });
    await expect(page.locator(".message-attachment-ready")).toHaveCount(1);
    await expect(page.getByTestId("voice-video-icon")).toBeVisible();
    const composerHeight = (await inputBeforeRecording.boundingBox())?.height;
    await page.getByTestId("voice-button").click();

    await expect(page.getByTestId("recording-panel")).toBeVisible();
    const recordingHeight = (await page.getByTestId("recording-panel").boundingBox())?.height;
    expect(recordingHeight).toBeLessThanOrEqual(46);
    expect(recordingHeight).toBeLessThan(composerHeight ?? Number.POSITIVE_INFINITY);
    await expect(page.getByRole("dialog", { name: "Record media" })).toHaveCount(0);
    await expect(page.getByTestId("message-input")).toHaveCount(0);
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await expect(page.getByTestId("recording-preserved-text")).toHaveText(
      "Keep this note with the recording.",
    );
    await expect(page.getByTestId("recording-waveform")).toBeVisible();
    await expect.poll(() => page.evaluate(() => window.__recordingMock.microphoneRequests)).toBe(1);

    await page.getByTestId("recording-stop-button").click();
    await transcriptionRequested.promise;
    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    const input = page.getByTestId("message-input");
    await expect(input).toBeVisible();

    releaseAcceptance.resolve();
    await expect(input).toBeEnabled();
    await expect(input).toBeFocused();
    await input.fill("I can keep typing while the server transcribes.");

    expect(chatBodies).toHaveLength(1);
    expect(chatBodies[0]?.message).toMatch(
      /^\/transcription \/tmp\/shelley-uploads\/recording\.webm\nKeep this note with the recording\. \[\/tmp\/shelley-uploads\/[^\]]+\.png\]$/,
    );
    expect(chatBodies[0]).toMatchObject({ model: "predictable" });
    expect(chatBodies[0]).not.toHaveProperty("transcription_context");
    await expect(page.getByTestId("message-attachments")).toHaveCount(0);
    await expect(page.getByTestId("transcription-task")).toHaveCount(0);
    await expect(input).toHaveValue("I can keep typing while the server transcribes.");
    expect(uploadedFilename).toMatch(/^rec-\d{8}-\d{6}\.webm$/);
    expect(uploadedBody).toBe("firstsecondlast");
    expect(await page.evaluate(() => window.__recordingMock.stoppedTracks)).toBeGreaterThan(0);
  });

  test("discards microphone capture and submits screen recording for transcription", async ({
    page,
  }) => {
    const uploadedFilenames: string[] = [];
    let transcriptionBody: Record<string, unknown> | null = null;

    await page.route("**/api/upload/raw?filename=*", async (route) => {
      const filename = new URL(route.request().url()).searchParams.get("filename") ?? "";
      uploadedFilenames.push(filename);
      const path = filename.endsWith(".json")
        ? "/tmp/shelley-uploads/screen.webm.json"
        : "/tmp/shelley-uploads/screen.webm";
      await fulfillJSON(route, { path });
    });
    await page.route("**/api/conversation/*/chat", async (route) => {
      transcriptionBody = route.request().postDataJSON() as Record<string, unknown>;
      await fulfillJSON(route, { status: "queued" }, 202);
    });

    await page.goto("/new");
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await page.getByTestId("recording-screen-button").click();

    await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", "screen");
    await expect(page.getByTestId("recording-preview")).toBeVisible();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording screen + microphone…");
    expect(uploadedFilenames).toHaveLength(0);
    expect(
      await page.evaluate(() => ({
        display: window.__recordingMock.displayRequests,
        microphone: window.__recordingMock.microphoneRequests,
        sources: window.__recordingMock.audioSources,
      })),
    ).toEqual({ display: 1, microphone: 2, sources: 2 });

    await page.getByTestId("recording-stop-button").dispatchEvent("pointerdown", {
      pointerType: "touch",
      button: 0,
    });
    await expect.poll(() => transcriptionBody).not.toBeNull();
    await expect(page.getByTestId("message-input")).toHaveValue("");
    expect(transcriptionBody).toMatchObject({
      message: "/transcription /tmp/shelley-uploads/screen.webm",
      model: "predictable",
    });
    expect(uploadedFilenames).toHaveLength(2);
    expect(uploadedFilenames[0]).toMatch(/^rec-\d{8}-\d{6}\.webm$/);
    expect(uploadedFilenames[1]).toBe("screen.webm.json");
  });

  test("renders durable transcription queue states in exact order after reload", async ({
    page,
    request,
  }) => {
    const queued = [
      queuedMessage("q-first", "echo: first queued message"),
      queuedMessage("q-working", "", {
        kind: "transcription",
        state: "working",
        transcription: {
          media_path: "/tmp/shelley-uploads/working.webm",
          child_conversation_id: "cWORKING",
          context: "Keep this image [/tmp/shelley-uploads/context.png]",
        },
      }),
      queuedMessage("q-ready", "Finished spoken words.\n\n(transcribed by subagent cREADY)", {
        kind: "transcription",
        state: "ready",
        transcription: {
          media_path: "/tmp/shelley-uploads/ready.webm",
          child_conversation_id: "cREADY",
        },
      }),
      queuedMessage("q-failed", "", {
        kind: "transcription",
        state: "failed",
        transcription: {
          media_path: "/tmp/shelley-uploads/failed.webm",
          child_conversation_id: "cFAILED",
        },
        error: "transcription unavailable",
      }),
    ];
    await page.route("**/api/conversation/*/send-queued?queued_id=*", (route) =>
      fulfillJSON(route, {}, 202),
    );
    const slug = await openQueuedConversation(page, request, queued);

    const items = page.locator('[data-testid="queued-ghost"], [data-testid="transcription-task"]');
    await expect(items).toHaveCount(4);
    await expect(items.nth(0)).toContainText("first queued message");
    await expect(items.nth(1)).toContainText("Transcribing…");
    await expect(items.nth(1)).toContainText("working.webm");
    await expect(items.nth(1)).toContainText("Keep this image");
    await expect(items.nth(2)).toContainText("Finished spoken words.");
    await expect(items.nth(2)).toContainText("Queued");
    await expect(items.nth(3)).toContainText("Recording failed");
    await expect(items.nth(3)).toContainText("transcription unavailable");
    await expect(page.getByTestId("queued-ghost")).toHaveCount(2);
    await expect(page.getByTestId("transcription-task")).toHaveCount(2);
    await expect(page.getByTestId("send-queued-now")).toHaveCount(1);
    const sendNowRequest = page.waitForRequest((req) =>
      req.url().includes("/send-queued?queued_id=q-first"),
    );
    await page.getByTestId("send-queued-now").click();
    expect((await sendNowRequest).method()).toBe("POST");

    await page.reload();
    await expect(page).toHaveURL(new RegExp(`/c/${slug}$`));
    await expect(items).toHaveCount(4);
    await expect(items.nth(0)).toContainText("first queued message");
    await expect(items.nth(1)).toContainText("working.webm");
    await expect(items.nth(2)).toContainText("Finished spoken words.");
    await expect(items.nth(3)).toContainText("failed.webm");
  });

  test("hides Send now on queued messages while compaction is in progress", async ({
    page,
    request,
  }) => {
    await openQueuedConversation(
      page,
      request,
      [queuedMessage("q-compacting", "queued during compaction")],
      true,
    );

    await expect(page.getByTestId("distill-in-progress")).toContainText("Compacting");
    await expect(page.getByTestId("queued-badge")).toBeVisible();
    await expect(page.getByTestId("send-queued-now")).toHaveCount(0);
    await expect(page.getByTestId("cancel-queued")).toBeVisible();
  });

  test("uses queued-message cancel and retry RPCs for transcription cards", async ({
    page,
    request,
  }) => {
    const queued = [
      queuedMessage("q-working", "", {
        kind: "transcription",
        state: "working",
        transcription: {
          media_path: "/tmp/shelley-uploads/working.webm",
          child_conversation_id: "cWORKING",
          context: "Keep working draft",
        },
      }),
      queuedMessage("q-failed", "", {
        kind: "transcription",
        state: "failed",
        transcription: {
          media_path: "/tmp/shelley-uploads/failed.webm",
          child_conversation_id: "cFAILED",
          context: "Keep failed draft",
        },
        error: "transcription unavailable",
      }),
    ];
    await page.route("**/api/conversation/*/cancel-queued?queued_id=*", (route) =>
      fulfillJSON(route, {}),
    );
    await page.route("**/api/conversation/*/retry-queued?queued_id=*", (route) =>
      fulfillJSON(route, {}),
    );
    await openQueuedConversation(page, request, queued);

    const stopRequest = page.waitForRequest((req) =>
      req.url().includes("/cancel-queued?queued_id=q-working"),
    );
    await page.getByTestId("transcription-stop-button").click();
    expect((await stopRequest).method()).toBe("POST");
    const input = page.getByTestId("message-input");
    await expect(input).toHaveValue("Keep working draft");

    await input.fill("");
    const retryRequest = page.waitForRequest((req) =>
      req.url().includes("/retry-queued?queued_id=q-failed"),
    );
    await page.getByTestId("transcription-retry-button").click();
    expect((await retryRequest).method()).toBe("POST");
    await expect(input).toHaveValue("");

    const cancelRequest = page.waitForRequest((req) =>
      req.url().includes("/cancel-queued?queued_id=q-failed"),
    );
    await page.getByTestId("transcription-cancel-button").click();
    expect((await cancelRequest).method()).toBe("POST");
    await expect(input).toHaveValue("Keep failed draft");
  });

  test("keeps the original draft when durable acceptance fails", async ({ page }) => {
    await page.route("**/api/upload/raw?filename=*", (route) =>
      fulfillJSON(route, { path: "/tmp/shelley-uploads/preserved.webm" }),
    );
    await page.route("**/api/conversation/*/chat", (route) =>
      route.fulfill({ status: 500, body: "transcription unavailable" }),
    );

    await page.goto("/new");
    await page.getByTestId("message-input").fill("Keep this draft");
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await page.getByTestId("recording-stop-button").click();

    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    await expect(page.getByTestId("message-input")).toHaveValue("Keep this draft");
    await expect(page.getByTestId("transcription-task")).toHaveCount(0);
  });

  test("keeps microphone recording when screen selection fails", async ({ page }) => {
    let uploadCount = 0;
    let transcriptionBody: Record<string, unknown> | null = null;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/kept.webm" });
    });
    await page.route("**/api/conversation/*/chat", async (route) => {
      transcriptionBody = route.request().postDataJSON() as Record<string, unknown>;
      await fulfillJSON(route, { status: "queued" }, 202);
    });

    await page.goto("/new");
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await page.evaluate(() => {
      window.__recordingMock.displayError = "Screen sharing was cancelled";
    });
    await page.getByTestId("recording-screen-button").click();

    await expect(page.getByTestId("recording-error")).toHaveText("Screen sharing was cancelled");
    await expect(page.getByTestId("recording-panel")).toHaveAttribute("data-mode", "microphone");
    await expect(page.getByTestId("recording-stop-button")).toBeVisible();
    expect(uploadCount).toBe(0);

    await page.getByTestId("recording-stop-button").click();
    await expect.poll(() => transcriptionBody).not.toBeNull();
    await expect(page.getByTestId("message-input")).toHaveValue("");
    expect(uploadCount).toBe(1);
    expect(transcriptionBody).toMatchObject({
      message: "/transcription /tmp/shelley-uploads/kept.webm",
      model: "predictable",
    });
  });

  test("cancels an active inline recording without uploading and restores the composer", async ({
    page,
  }) => {
    let uploadCount = 0;
    await page.route("**/api/upload/raw?filename=*", async (route) => {
      uploadCount++;
      await fulfillJSON(route, { path: "/tmp/shelley-uploads/unexpected.webm" });
    });

    await page.goto("/new");
    await page.getByTestId("voice-button").click();
    await expect(page.getByTestId("recording-status")).toHaveText("Recording…");
    await page.getByTestId("recording-cancel-button").click();

    await expect(page.getByTestId("recording-panel")).toHaveCount(0);
    expect(uploadCount).toBe(0);
    await expect(page.getByTestId("message-input")).toHaveValue("");
    expect(await page.evaluate(() => window.__recordingMock.stoppedTracks)).toBeGreaterThan(0);
  });
});

test("uses a microphone icon when screen capture is unavailable", async ({ page }) => {
  await installMediaMocks(page, false);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/new");
  await expect(page.getByTestId("voice-microphone-icon")).toBeVisible();
  await expect(page.getByTestId("voice-video-icon")).toHaveCount(0);
});

declare global {
  interface Window {
    __recordingMock: {
      displayRequests: number;
      microphoneRequests: number;
      audioSources: number;
      stoppedTracks: number;
      displayError: string;
    };
  }
}
