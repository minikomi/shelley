import { expect, test } from "@playwright/test";

const catalog = () => ({
  models: [
    {
      model_id: "managed-deepgram",
      display_name: "Deepgram Nova 3",
      protocol: "deepgram",
      provider: "Deepgram",
      endpoint: "https://api.deepgram.com/v1/listen",
      model_name: "nova-3",
      api_profile: "auto",
      request_encoding: "auto",
      supports_prompted: false,
      supports_timecodes: true,
      managed: true,
      source: "deepgram",
      has_api_key: true,
    },
    {
      model_id: "custom-openai",
      display_name: "Custom Whisper",
      protocol: "openai",
      provider: "My gateway",
      endpoint: "https://speech.example.test/v1",
      model_name: "whisper-1",
      api_profile: "openai-whisper",
      request_encoding: "multipart",
      supports_prompted: true,
      supports_timecodes: false,
      managed: false,
      source: "custom",
      has_api_key: true,
    },
    {
      model_id: "custom-deepgram",
      display_name: "Private Deepgram",
      protocol: "deepgram",
      provider: "Private Deepgram",
      endpoint: "https://deepgram.example.test/v1/listen",
      model_name: "nova-3-general",
      api_profile: "auto",
      request_encoding: "auto",
      supports_prompted: false,
      supports_timecodes: true,
      managed: false,
      source: "custom",
      has_api_key: true,
    },
    {
      model_id: "custom-base64",
      display_name: "Fish Audio",
      protocol: "openai",
      provider: "Fish Audio",
      endpoint: "https://fish.example.test/v1",
      model_name: "fish-audio/transcribe-1",
      api_profile: "auto",
      request_encoding: "base64-json",
      supports_prompted: false,
      supports_timecodes: false,
      managed: false,
      source: "custom",
      has_api_key: true,
    },
  ],
  defaults: {
    transcript: { model_id: "custom-openai", available: true },
  },
});

async function mockCatalogRoutes(page: import("@playwright/test").Page) {
  await page.route("**/api/models", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/custom-models", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/transcription-models", (route) => route.fulfill({ json: catalog() }));
}

async function openTranscriptionForm(page: import("@playwright/test").Page) {
  await page.goto("/new");
  await page.locator(".model-picker").click();
  await page.getByRole("button", { name: /Manage models/ }).click();
  const modelsDialog = page.getByRole("dialog", { name: "Manage Models" });
  await modelsDialog.getByRole("tab", { name: "Transcription Models" }).click();
  await modelsDialog.getByRole("button", { name: "+ Add Model" }).click();
  return page.getByRole("dialog", { name: "Add Transcription Model" });
}

async function fillTestableDeepgramDraft(form: import("@playwright/test").Locator) {
  await form.getByLabel("Name").fill("Draft Deepgram");
  await form.getByLabel("Model ID").fill("nova-3");
  await form.getByLabel("API Key").fill("draft-secret");
}

test("selects every model for Transcript and shows Context prompts capability", async ({
  page,
}) => {
  const selectedModelIds: string[] = [];
  await mockCatalogRoutes(page);
  await page.route("**/api/transcription-model-defaults/transcript", async (route) => {
    const body = route.request().postDataJSON() as { model_id: string };
    selectedModelIds.push(body.model_id);
    await route.fulfill({ json: { model_id: body.model_id, available: true } });
  });

  await page.goto("/new");
  await page.locator(".model-picker").click();
  await page.getByRole("button", { name: /Manage models/ }).click();
  const modelsDialog = page.getByRole("dialog", { name: "Manage Models" });
  await modelsDialog.getByRole("tab", { name: "Transcription Models" }).click();
  const transcription = modelsDialog.getByRole("tabpanel", { name: "Transcription Models" });

  await expect(transcription).toContainText(
    "Choose one model for transcript generation and one for timecoded transcription. Context prompts are optional model capabilities.",
  );
  const deepgram = transcription.getByRole("row", { name: /Deepgram Nova 3 nova-3/ });
  const whisper = transcription.getByRole("row", { name: /Custom Whisper whisper-1/ });
  const base64 = transcription.getByRole("row", { name: /Fish Audio fish-audio\/transcribe-1/ });
  await expect(deepgram).toContainText("Not supported");
  await expect(whisper).toContainText("Supported");
  await expect(base64).toContainText("Not supported");
  await expect(deepgram.getByLabel("Use Deepgram Nova 3 for transcript generation")).toBeEnabled();
  await expect(base64.getByLabel("Use Fish Audio for transcript generation")).toBeEnabled();
  await expect(whisper.getByLabel("Use Custom Whisper for transcript generation")).toBeChecked();
  await expect(whisper.getByLabel("Use Custom Whisper for timecoded transcription")).toHaveCount(0);
  await expect(whisper.locator(".transcription-role-unsupported")).toHaveText("—");

  await deepgram.getByLabel("Use Deepgram Nova 3 for transcript generation").check();
  await base64.getByLabel("Use Fish Audio for transcript generation").check();
  await whisper.getByLabel("Use Custom Whisper for transcript generation").check();
  await expect
    .poll(() => selectedModelIds)
    .toEqual(["managed-deepgram", "custom-base64", "custom-openai"]);
});

test("resets unsupported Context prompts and submits a both-false base64 draft", async ({
  page,
}) => {
  let submitted: Record<string, unknown> | undefined;
  await mockCatalogRoutes(page);
  await page.route("**/api/transcription-models", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    submitted = route.request().postDataJSON();
    await route.fulfill({ json: { ...submitted, model_id: "custom-base64", managed: false } });
  });
  const form = await openTranscriptionForm(page);

  await form.getByLabel("Model ID").fill("gpt-transcribe");
  await expect(form).toContainText(
    "Detected: standard transcription with optional Context prompts.",
  );
  await expect(form.getByLabel("Context prompts")).toBeChecked();
  await expect(form.getByLabel("Context prompts")).toBeEnabled();
  await expect(form.getByLabel("Timecodes")).not.toBeChecked();
  await expect(form.getByLabel("Timecodes")).toBeDisabled();

  await form.getByLabel("Model ID").fill("fish-audio/transcribe-1");
  await expect(form).toContainText("Detected: Base64 JSON for Fish Audio.");
  await expect(form).toContainText("Base64 JSON supports Transcript, but not Context prompts.");
  await expect(form.getByLabel("Context prompts")).not.toBeChecked();
  await expect(form.getByLabel("Context prompts")).toBeDisabled();
  await expect(form.getByLabel("Timecodes")).not.toBeChecked();
  await expect(form.getByLabel("Timecodes")).toBeEnabled();

  await form.getByLabel("Name").fill("Fish Audio draft");
  await form.getByLabel("API Key").fill("draft-secret");
  await form.getByRole("button", { name: "Add Model" }).click();
  await expect
    .poll(() => submitted)
    .toMatchObject({
      supports_prompted: false,
      supports_timecodes: false,
      model_name: "fish-audio/transcribe-1",
    });
});

test("shows Deepgram transcript and context prompt behavior in the form", async ({ page }) => {
  await mockCatalogRoutes(page);
  const form = await openTranscriptionForm(page);

  await form.getByRole("radio", { name: "Deepgram" }).check();
  await expect(form.getByLabel("Provider")).toHaveValue("Deepgram");
  await expect(form.getByLabel("Endpoint")).toHaveValue("https://api.deepgram.com/v1/listen");
  await expect(form.getByLabel("Context prompts")).toBeDisabled();
  await expect(form.getByLabel("Context prompts")).not.toBeChecked();
  await expect(form.getByLabel("Timecodes")).toBeChecked();
  await expect(form).toContainText(
    "Native Deepgram supports Transcript and optional timecodes, but not Context prompts.",
  );
});

test("resets protocol defaults for new models and preserves saved models on edit", async ({
  page,
}) => {
  await mockCatalogRoutes(page);
  const form = await openTranscriptionForm(page);

  await expect(form.getByLabel("Model ID")).toHaveAttribute("placeholder", "e.g., whisper-1");
  await form.getByLabel("Provider").fill("Custom OpenAI");
  await form.getByLabel("Endpoint").fill("https://custom.example.test/transcribe");
  await form.getByLabel("Model ID").fill("custom-model");
  await form.getByLabel("Name").fill("Custom transcription");
  await form.getByLabel("Timecodes").check();
  await form.getByRole("radio", { name: "Deepgram" }).check();

  await expect(form.getByLabel("Provider")).toHaveValue("Deepgram");
  await expect(form.getByLabel("Endpoint")).toHaveValue("https://api.deepgram.com/v1/listen");
  await expect(form.getByLabel("Model ID")).toHaveValue("");
  await expect(form.getByLabel("Model ID")).toHaveAttribute("placeholder", "e.g., nova-3");
  await expect(form.getByLabel("Name")).toHaveValue("");
  await expect(form.getByLabel("Context prompts")).not.toBeChecked();
  await expect(form.getByLabel("Timecodes")).toBeChecked();

  await form.getByLabel("Model ID").fill("nova-3");
  await form.getByLabel("Name").fill("Custom Deepgram");
  await form.getByRole("radio", { name: "OpenAI-compatible" }).check();

  await expect(form.getByLabel("Provider")).toHaveValue("OpenAI");
  await expect(form.getByLabel("Endpoint")).toHaveValue(
    "https://api.openai.com/v1/audio/transcriptions",
  );
  await expect(form.getByLabel("Model ID")).toHaveValue("");
  await expect(form.getByLabel("Model ID")).toHaveAttribute("placeholder", "e.g., whisper-1");
  await expect(form.getByLabel("Name")).toHaveValue("");
  await expect(form.getByLabel("Context prompts")).toBeChecked();
  await expect(form.getByLabel("Timecodes")).not.toBeChecked();

  await form.getByRole("button", { name: "Close modal" }).click();
  const modelsDialog = page.getByRole("dialog", { name: "Manage Models" });
  await modelsDialog.getByRole("tab", { name: "Transcription Models" }).click();
  const transcription = modelsDialog.getByRole("tabpanel", { name: "Transcription Models" });
  await transcription
    .getByRole("row", { name: /Private Deepgram/ })
    .getByRole("button", { name: "Edit Model" })
    .click();
  const editForm = page.getByRole("dialog", { name: "Edit Transcription Model" });

  await expect(editForm.getByRole("radio", { name: "Deepgram" })).toBeChecked();
  await expect(editForm.getByLabel("Provider")).toHaveValue("Private Deepgram");
  await expect(editForm.getByLabel("Endpoint")).toHaveValue(
    "https://deepgram.example.test/v1/listen",
  );
  await expect(editForm.getByLabel("Model ID")).toHaveValue("nova-3-general");
  await expect(editForm.getByLabel("Name")).toHaveValue("Private Deepgram");
  await expect(editForm.getByLabel("Context prompts")).not.toBeChecked();
  await expect(editForm.getByLabel("Timecodes")).toBeChecked();
});

test("duplicates and deletes custom transcription models", async ({ page }) => {
  let models = catalog().models;
  const duplicate = {
    ...models[2],
    model_id: "custom-deepgram-copy",
    display_name: "Private Deepgram copy",
  };

  await page.route("**/api/models", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/custom-models", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/transcription-models", (route) =>
    route.fulfill({ json: { ...catalog(), models } }),
  );
  await page.route("**/api/transcription-models/custom-deepgram/duplicate", (route) => {
    models = [...models, duplicate];
    return route.fulfill({ json: duplicate });
  });
  await page.route("**/api/transcription-models/custom-deepgram", (route) => {
    models = models.filter((model) => model.model_id !== "custom-deepgram");
    return route.fulfill({ status: 204 });
  });

  await page.goto("/new");
  await page.locator(".model-picker").click();
  await page.getByRole("button", { name: /Manage models/ }).click();
  const modelsDialog = page.getByRole("dialog", { name: "Manage Models" });
  await modelsDialog.getByRole("tab", { name: "Transcription Models" }).click();
  const transcription = modelsDialog.getByRole("tabpanel", { name: "Transcription Models" });
  const privateDeepgram = transcription.getByRole("row", {
    name: /^Private Deepgram nova-3-general/,
  });

  await privateDeepgram.getByRole("button", { name: "Duplicate" }).click();
  await expect(transcription.getByRole("row", { name: /Private Deepgram copy/ })).toBeVisible();

  await privateDeepgram.getByRole("button", { name: "Delete" }).click();
  await expect(
    transcription.getByRole("row", { name: /^Private Deepgram nova-3-general/ }),
  ).toHaveCount(0);
});

test("shows transcript and timecode test results", async ({ page }) => {
  let testedModel = "";
  await mockCatalogRoutes(page);
  await page.route("**/api/transcription-models/test", async (route) => {
    testedModel = route.request().postData() || "";
    await route.fulfill({
      json: {
        model: catalog().models[1],
        results: {
          transcript: {
            success: true,
            message: "Test successful",
            transcript: "Sample transcript.",
          },
          timecoded: { success: true, message: "Test successful", has_timecodes: true },
        },
      },
    });
  });

  const form = await openTranscriptionForm(page);
  await form.getByLabel("Provider").fill("OpenAI");
  await form.getByLabel("Model ID").fill("legacy-openai");
  await form.getByLabel("Name").fill("Dual-capability transcription");
  await form.getByLabel("API Key").fill("draft-secret");
  await form.getByRole("combobox", { name: "API behavior" }).click();
  await page.getByRole("option", { name: "Whisper timestamps" }).click();
  await form.getByLabel("Timecodes").check();

  await form.getByRole("button", { name: "Test model" }).click();
  await expect(form).toContainText("Transcript");
  await expect(form).toContainText("Sample transcript.");
  await expect(form).toContainText("Timecoded transcription");
  await expect(form).toContainText("Timecodes returned.");
  expect(testedModel).toContain('"supports_prompted":true');
  expect(testedModel).toContain('"supports_timecodes":true');
  expect(testedModel).toContain('"api_profile":"openai-whisper"');
});
