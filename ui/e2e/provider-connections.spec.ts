import { expect, test, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

// Saved provider connection form and management dialog, against stubbed
// /api/model-providers routes only (no backend state or real keys involved).

const provider = (overrides: Record<string, unknown> = {}) => ({
  provider_id: "prov-1",
  provider_type: "openai",
  endpoint: "https://api.openai.com/v1",
  display_name: "Work",
  has_api_key: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  state: "ready",
  model_count: 0,
  last_success: null,
  error: null,
  ...overrides,
});

async function openAddModels(page: Page) {
  await page.goto("/new");
  await page.locator(".model-picker").click();
  await page.getByRole("button", { name: "Manage Models" }).click();
  await page.getByRole("button", { name: "+ Add models" }).click();
  return page.getByRole("dialog").last();
}

async function stubLists(page: Page, providers: () => unknown[]) {
  await page.route("**/api/models", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/custom-models", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/model-providers", async (route) => {
    if (route.request().method() === "GET") await route.fulfill({ json: providers() });
    else await route.fallback();
  });
}

test.describe("provider connections", () => {
  test("endpoint is a real input: typed text is the value, presets follow the protocol", async ({
    page,
  }) => {
    let createBody: Record<string, unknown> | undefined;
    await stubLists(page, () => []);
    await page.route("**/api/model-providers", async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      createBody = route.request().postDataJSON();
      await route.fulfill({
        status: 409,
        json: { error: "a provider with this endpoint and API key is already saved" },
      });
    });

    const form = await openAddModels(page);
    await form.getByRole("button", { name: "OpenAI Chat" }).click();
    const endpoint = form.getByRole("textbox", { name: "Endpoint", exact: true });
    const dropdown = form.getByRole("button", { name: "Endpoint", exact: true });
    const presets = page.locator("#model-endpoint-presets");
    await expect(endpoint).toHaveValue("https://api.openai.com/v1");
    await dropdown.click();
    await expect(presets).toBeVisible();
    await expect(endpoint).not.toBeFocused();
    await expect(presets.locator("input")).toHaveCount(0);
    await expect(presets).toContainText("https://openrouter.ai/api/v1");
    await expect(presets).toContainText("https://api.z.ai/api/paas/v4");
    await presets.getByText("Groq", { exact: true }).click();
    await expect(endpoint).toHaveValue("https://api.groq.com/openai/v1");
    await expect(presets).toBeHidden();

    // Typing is immediate: no Enter, no option pick, no blur needed.
    await endpoint.fill("https://my-proxy.example/v1");
    await expect(endpoint).toHaveValue("https://my-proxy.example/v1");

    // Presets are scoped to the protocol; a custom URL survives a protocol switch.
    await form.getByRole("button", { name: "OpenAI Responses" }).click();
    await endpoint.press("ArrowDown");
    await expect(presets).toBeVisible();
    await expect(presets).toContainText("https://api.openai.com/v1");
    await expect(presets).not.toContainText("https://api.z.ai/api/paas/v4");
    await page.keyboard.press("Escape");
    await expect(presets).toBeHidden();
    await expect(endpoint).toHaveValue("https://my-proxy.example/v1");

    await form.getByLabel("API Key").fill("synthetic-key");
    await form.getByRole("button", { name: "Connect" }).click();
    await expect.poll(() => createBody?.endpoint).toBe("https://my-proxy.example/v1");
    expect(createBody?.provider_type).toBe("openai-responses");
    // The API's JSON error body is unwrapped, not shown raw.
    await expect(form.locator(".models-error")).toContainText(
      "Failed to add provider connection: a provider with this endpoint and API key is already saved",
    );
    await expect(form.locator(".models-error")).not.toContainText('{"error"');
  });

  test("API key stays masked when switching between provider and single-model modes", async ({
    page,
  }) => {
    await stubLists(page, () => []);
    const form = await openAddModels(page);
    await form.getByLabel("API Key").fill("synthetic-key");
    await expect(form.getByLabel("API Key")).toHaveAttribute("type", "password");

    await form.getByRole("button", { name: "Add single model" }).click();
    const key = form.locator(".form-group").filter({ hasText: "API Key" }).locator("input");
    await expect(key).toHaveValue("synthetic-key");
    await expect(key).toHaveAttribute("type", "password");
  });

  test("remove: a missing connection reconciles; other failures stay visible in the dialog", async ({
    page,
  }) => {
    let listed = [provider()];
    let deleteStatus = 500;
    await stubLists(page, () => listed);
    await page.route("**/api/model-providers/prov-1", async (route) => {
      if (route.request().method() !== "DELETE") return route.fallback();
      if (deleteStatus === 404) listed = [];
      await route.fulfill({ status: deleteStatus, json: { error: "storage unavailable" } });
    });

    await page.goto("/new");
    await page.locator(".model-picker").click();
    await page.getByRole("button", { name: "Manage Models" }).click();
    await page.getByRole("button", { name: "Remove connection" }).first().click();
    const confirm = page.getByRole("dialog").last();
    await confirm.getByRole("button", { name: "Remove connection" }).click();
    await expect(confirm.getByRole("alert")).toContainText("storage unavailable");
    await expect(confirm).toBeVisible();

    deleteStatus = 404;
    await confirm.getByRole("button", { name: "Remove connection" }).click();
    await expect(page.getByText("Remove provider connection?")).toBeHidden();
    await expect(page.getByRole("button", { name: "Remove connection" })).toHaveCount(0);
  });
});

test("deleting the preferred provider clears the new-chat selection without switching accounts", async ({ page }) => {
  const savedID = "provider:cHJvdi0x:Z3B0LTYtbHVuYQ";
  let listed = [provider({ model_count: 1 })];
  let catalog = [
    { id: "predictable", display_name: "predictable", ready: true, supports_reasoning: false },
    { id: savedID, display_name: "GPT-6 Luna", ready: true, source_id: "prov-1", source: "Work", supports_reasoning: false },
  ];
  await stubLists(page, () => listed);
  await page.route("**/api/models", (route) => route.fulfill({ json: catalog }));
  await page.route("**/api/model-providers/prov-1", async (route) => {
    if (route.request().method() !== "DELETE") return route.fallback();
    listed = [];
    catalog = catalog.filter((model) => model.id !== savedID);
    await route.fulfill({ status: 204 });
  });
  await page.addInitScript((id) => {
    localStorage.setItem("shelley_selected_model", id);
    localStorage.setItem("shelley.profileApplied", "Default");
  }, savedID);
  await page.goto("/new");
  const picker = page.locator(".model-picker.p-select");
  await expect(picker.locator(".model-picker-value-name")).toContainText("GPT-6 Luna");
  await picker.click();
  await page.getByRole("button", { name: "Manage Models" }).click();
  await page.getByRole("button", { name: "Remove connection" }).click();
  await page.getByRole("dialog").last().getByRole("button", { name: "Remove connection" }).click();
  await expect.poll(() => page.evaluate(() => localStorage.getItem("shelley_selected_model"))).toBe("");
  await page.keyboard.press("Escape");
  await expect(page.getByText("The selected provider connection was removed. Choose another model.")).toBeVisible();
  await page.locator(".status-bar-content .status-button").click();
  await expect(picker.locator(".model-picker-value-name")).toHaveText("Choose a model");
  const input = page.getByTestId("message-input");
  await input.evaluate((element) => {
    const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value")!.set!;
    setter.call(element, "echo: keep this message");
    element.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(page.locator(".status-error")).toHaveText("Choose a model");
  await expect(input).toHaveValue("echo: keep this message");
  await page.locator(".status-bar-content .status-button").click();
  await picker.click();
  await page.locator(".model-picker-panel .p-select-option").filter({ hasText: "predictable" }).click();
  await expect.poll(() => page.evaluate(() => localStorage.getItem("shelley_selected_model"))).toBe("predictable");
});

test("an account-label lookup failure is handled without an unhandled rejection", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await stubLists(page, () => []);
  await page.route("**/api/models", (route) => route.fulfill({ json: [
    { id: "predictable", display_name: "predictable", ready: true, supports_reasoning: false },
    { id: "fresh-catalog-model", display_name: "Fresh catalog model", ready: true, supports_reasoning: false },
  ] }));
  let providerRequests = 0;
  await page.route("**/api/model-providers", (route) => {
    providerRequests++;
    return route.fulfill({ status: 503, json: { error: "temporarily unavailable" } });
  });
  await page.addInitScript(() => {
    localStorage.setItem("shelley_selected_model", "provider:cHJvdi0x:Z3B0LTYtbHVuYQ");
    localStorage.setItem("shelley.profileApplied", "Default");
  });
  const handled = page.waitForEvent("console", {
    predicate: (message) => message.text().includes("Failed to load provider account labels:"),
  });
  await page.goto("/new");
  await handled;
  const picker = page.locator(".model-picker.p-select");
  await expect(picker.locator(".model-picker-value-name")).toContainText("gpt-6-luna (not listed)");
  await picker.click();
  await expect(page.locator(".model-picker-panel .p-select-option").filter({ hasText: "Fresh catalog model" })).toBeVisible();
  expect(providerRequests).toBe(2);
  expect(errors).toEqual([]);
});


test("returning from a conversation with a removed saved preference prompts a new choice", async ({ page, request }) => {
  const savedID = "provider:cHJvdi0x:Z3B0LTYtbHVuYQ";
  const conversation = await createConversationViaAPIWithDetails(request, "echo: saved preference recovery");
  let listed = [provider({ model_count: 1 })];
  let catalog = [
    { id: "predictable", display_name: "predictable", ready: true, supports_reasoning: false },
    { id: savedID, display_name: "GPT-6 Luna", ready: true, source_id: "prov-1", supports_reasoning: false },
  ];
  await stubLists(page, () => listed);
  await page.route("**/api/models", (route) => route.fulfill({ json: catalog }));
  await page.route("**/api/model-providers/prov-1", async (route) => {
    if (route.request().method() !== "DELETE") return route.fallback();
    listed = [];
    catalog = [{ id: "predictable", display_name: "Refreshed predictable", ready: true, supports_reasoning: false }];
    await route.fulfill({ status: 204 });
  });
  await page.addInitScript((id) => {
    localStorage.setItem("shelley_selected_model", id);
    localStorage.setItem("shelley.profileApplied", "Default");
  }, savedID);
  await page.goto(`/c/${conversation.slug}`);
  await page.locator(".model-picker.p-select").click();
  await page.getByRole("button", { name: "Manage Models" }).click();
  await page.getByRole("button", { name: "Remove connection" }).click();
  await page.getByRole("dialog").last().getByRole("button", { name: "Remove connection" }).click();
  await expect.poll(() => page.evaluate(() => window.__SHELLEY_INIT__?.models?.[0]?.display_name)).toBe("Refreshed predictable");
  await page.keyboard.press("Escape");
  expect(await page.evaluate(() => localStorage.getItem("shelley_selected_model"))).toBe(savedID);
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();
  await page.getByRole("button", { name: "New Conversation", exact: true }).click();
  await expect(page).toHaveURL(/\/new$/);
  await expect(page.getByText("The selected provider connection was removed. Choose another model.")).toBeVisible();
  await expect.poll(() => page.evaluate(() => localStorage.getItem("shelley_selected_model"))).toBe("");
  await page.locator(".status-bar-content .status-button").click();
  await expect(page.locator(".model-picker-value-name")).toHaveText("Choose a model");
});
