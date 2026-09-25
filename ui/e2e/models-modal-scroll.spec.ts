import { expect, test, type Page } from "@playwright/test";

const customModels = Array.from({ length: 18 }, (_, index) => ({
  model_id: `custom-${index}`,
  display_name: `Custom model ${index}`,
  provider_type: "openai",
  endpoint: "https://models.example.test/v1",
  api_key: "test-key",
  model_name: `model-${index}`,
  max_tokens: 200000,
  tags: "",
  reasoning_effort: "",
  reasoning_support: "auto",
  reasoning_map: "",
  supports_reasoning: false,
  image_support: "auto",
  supports_images: true,
}));

const transcriptionCatalog = {
  models: Array.from({ length: 18 }, (_, index) => ({
    model_id: `transcription-${index}`,
    display_name: `Transcription model ${index}`,
    protocol: "openai",
    provider: "Example",
    endpoint: "https://speech.example.test/v1",
    model_name: `speech-${index}`,
    supports_prompted: true,
    supports_timecodes: true,
    managed: false,
    source: "custom",
    has_api_key: true,
  })),
  defaults: {},
};

async function openManageModels(page: Page) {
  await page.route("**/api/custom-models", (route) => route.fulfill({ json: customModels }));
  await page.route("**/api/transcription-models", (route) =>
    route.fulfill({ json: transcriptionCatalog }),
  );
  await page.goto("/new");
  await page.locator(".model-picker").click();
  await page.getByRole("button", { name: /Manage models/ }).click();
  return page.getByRole("dialog", { name: "Manage Models" });
}

test.describe("Manage Models tabs and scrolling", () => {
  test.use({ viewport: { width: 390, height: 720 } });

  test("switches catalogs with generous internally scrolling tables", async ({ page }) => {
    const manageModels = await openManageModels(page);
    const modelsTab = manageModels.getByRole("tab", { name: "Models", exact: true });
    const transcriptionTab = manageModels.getByRole("tab", {
      name: "Transcription Models",
      exact: true,
    });
    const modelsPanel = manageModels.getByRole("tabpanel", { name: "Models", exact: true });
    const transcriptionPanel = manageModels.getByRole("tabpanel", {
      name: "Transcription Models",
      exact: true,
    });

    await expect(modelsTab).toHaveAttribute("aria-selected", "true");
    await expect(modelsPanel).toBeVisible();
    await expect(transcriptionPanel).toBeHidden();
    await expect(
      manageModels.locator(".models-tabs-row").getByRole("button", { name: "+ Add Model" }),
    ).toBeVisible();

    const modelsViewport = modelsPanel.locator(".p-datatable-table-container");
    await expect(modelsPanel.locator(".models-datatable.p-datatable-scrollable")).toHaveCount(1);
    const dimensions = await modelsViewport.evaluate((element) => ({
      scrollHeight: element.scrollHeight,
      clientHeight: element.clientHeight,
    }));
    expect(dimensions.clientHeight).toBeGreaterThan(250);
    expect(dimensions.scrollHeight).toBeGreaterThan(dimensions.clientHeight);
    await modelsViewport.evaluate((element) => {
      element.scrollTop = 120;
    });
    await expect
      .poll(() => modelsViewport.evaluate((element) => element.scrollTop))
      .toBeGreaterThan(0);
    await expect(modelsPanel.locator(".p-datatable-thead")).toBeVisible();

    await modelsTab.focus();
    await page.keyboard.press("ArrowRight");
    await expect(transcriptionTab).toHaveAttribute("aria-selected", "true");
    await expect(transcriptionPanel).toBeVisible();
    await expect(modelsPanel).toBeHidden();
    await expect(
      transcriptionPanel.locator(".models-datatable.p-datatable-scrollable"),
    ).toHaveCount(1);
    await expect(transcriptionPanel.getByRole("heading", { name: "Transcription Models" })).toHaveCount(
      0,
    );

    const alignedLeftEdges = await transcriptionPanel.evaluate((panel) => {
      const leftContentEdge = (element: Element) => {
        const styles = getComputedStyle(element);
        return element.getBoundingClientRect().left + Number.parseFloat(styles.paddingLeft);
      };
      const description = panel.querySelector(".transcription-models-description");
      const header = panel.querySelector(".p-datatable-thead > tr > th:first-child");
      const group = panel.querySelector(
        ".p-datatable-tbody > .p-datatable-row-group-header > td:first-child",
      );
      const model = panel.querySelector(
        ".p-datatable-tbody > tr:not(.p-datatable-row-group-header) > td:first-child",
      );
      if (!description || !header || !group || !model) throw new Error("Missing alignment target");
      return [
        description.getBoundingClientRect().left,
        leftContentEdge(header),
        leftContentEdge(group),
        leftContentEdge(model),
      ];
    });
    expect(Math.max(...alignedLeftEdges) - Math.min(...alignedLeftEdges)).toBeLessThanOrEqual(1);

    const transcriptionViewport = transcriptionPanel.locator(".p-datatable-table-container");
    await expect
      .poll(() => transcriptionViewport.evaluate((element) => element.clientHeight))
      .toBeGreaterThan(250);
    await transcriptionViewport.evaluate((element) => {
      element.scrollTop = 120;
    });
    await expect
      .poll(() => transcriptionViewport.evaluate((element) => element.scrollTop))
      .toBeGreaterThan(0);

    await manageModels.getByRole("button", { name: "Close modal" }).click();
    await page.locator(".model-picker").click();
    await page.getByRole("button", { name: /Manage models/ }).click();
    await expect(modelsTab).toHaveAttribute("aria-selected", "true");
  });
});
