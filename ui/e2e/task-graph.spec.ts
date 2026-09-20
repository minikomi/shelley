import { test, expect } from "@playwright/test";
import { createConversationViaAPI } from "./helpers";

test("task graph docks above the composer and collapses", async ({ page, request }) => {
  test.setTimeout(120000);
  const slug = await createConversationViaAPI(request, "task graph demo", {
    agentTimeout: 30000,
  });

  await page.goto(`/c/${slug}`);
  const graph = page.getByTestId("task-graph").last();
  await expect(graph).toBeVisible();
  await expect(graph).toContainText("Build task graph demo");
  await expect(graph.locator(".task-graph-row")).toHaveCount(4);
  await expect(graph.locator(".task-state-running")).toHaveCount(3);
  await expect(graph.locator(".task-tree-connector")).toHaveCount(1);
  await expect(graph.locator(".task-state-pending")).toContainText(
    "Waiting for Inspect repository, Verify UI states, Scaffold application",
  );
  expect(await graph.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);

  await graph.locator(".task-graph-header").click();
  await expect(graph.locator(".task-graph-row")).toHaveCount(0);
  await expect(graph).toContainText("3 running");

  await graph.locator(".task-graph-header").click();
  await expect(graph.locator(".task-graph-row")).toHaveCount(4);
});
