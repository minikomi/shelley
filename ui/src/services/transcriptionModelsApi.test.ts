import assert from "node:assert/strict";
import { transcriptionModelsApi, type TranscriptionModelDraft } from "./api";

const originalFetch = globalThis.fetch;
const draft: TranscriptionModelDraft = {
  display_name: "Deepgram Nova",
  protocol: "deepgram",
  provider: "Deepgram",
  endpoint: "https://api.deepgram.com",
  model_name: "nova-3",
  api_profile: "auto",
  request_encoding: "auto",
  supports_prompted: true,
  supports_timecodes: true,
};

async function run() {
  try {
    let url = "";
    let init: RequestInit | undefined;
    globalThis.fetch = (async (input, request) => {
      url = String(input);
      init = request;
      return new Response(JSON.stringify({ model_id: "custom-1", available: true }), {
        status: 200,
      });
    }) as typeof fetch;

    await transcriptionModelsApi.setDefault("transcript", "custom-1");
    assert.equal(url, "/api/transcription-model-defaults/transcript");
    assert.equal(init?.method, "PUT");
    assert.deepEqual(JSON.parse(String(init?.body)), { model_id: "custom-1" });

    globalThis.fetch = (async (input, request) => {
      url = String(input);
      init = request;
      return new Response(JSON.stringify({ ...draft, model_id: "custom-1" }), { status: 200 });
    }) as typeof fetch;
    await transcriptionModelsApi.create(draft);
    assert.equal(url, "/api/transcription-models");
    assert.equal(init?.method, "POST");
    assert.equal(JSON.parse(String(init?.body)).api_profile, "auto");
    assert.equal(JSON.parse(String(init?.body)).request_encoding, "auto");

    await transcriptionModelsApi.update("custom-1", { ...draft, api_profile: "openai-whisper" });
    assert.equal(url, "/api/transcription-models/custom-1");
    assert.equal(init?.method, "PUT");
    assert.equal(JSON.parse(String(init?.body)).api_profile, "openai-whisper");

    let submitted: unknown = null;
    globalThis.fetch = (async (input, request) => {
      url = String(input);
      submitted = request?.body as FormData;
      return new Response(
        JSON.stringify({
          model: {
            ...draft,
            model_id: "draft",
            managed: false,
            source: "custom",
            has_api_key: true,
          },
          results: {},
        }),
        { status: 200 },
      );
    }) as typeof fetch;
    await transcriptionModelsApi.test(
      { ...draft, api_key: "never-rendered" },
      new Blob(["sample"]),
    );
    assert.equal(url, "/api/transcription-models/test");
    assert.ok(submitted instanceof FormData);
    const multipart = submitted as FormData;
    assert.equal((multipart.get("model") as string).includes("never-rendered"), true);
    assert.equal((multipart.get("model") as string).includes('"api_profile":"auto"'), true);
    assert.equal((multipart.get("model") as string).includes('"request_encoding":"auto"'), true);
    assert.ok(multipart.get("file") instanceof Blob);
  } finally {
    globalThis.fetch = originalFetch;
  }
}

await run();
console.log("transcriptionModelsApi: passed");
