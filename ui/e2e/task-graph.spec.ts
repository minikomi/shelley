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
  await expect(graph.locator(".task-graph-row")).toHaveCount(0);
  await expect(graph).toContainText("3 subagents running");

  await graph.locator(".task-graph-header").click();
  await expect(graph).toContainText("Build task graph demo");
  await expect(graph.locator(".task-graph-row")).toHaveCount(6);
  await expect(graph.locator(".task-state-running")).toHaveCount(3);
  await expect(graph.locator(".task-graph-flow")).toBeVisible();
  await expect(graph.locator(".task-flow-layer")).toHaveCount(3);
  await expect(graph.locator(".task-flow-layer.is-group")).toHaveCount(2);
  await expect(graph.locator(".task-flow-junction")).toHaveCount(2);
  await expect(graph.locator(".task-tree-connector")).toHaveCount(0);
  await expect(
    graph.locator(".task-graph-flow .task-graph-row strong").filter({ hasText: /^Implement backend$/ }),
  ).toBeVisible();
  await expect(
    graph.locator(".task-graph-flow .task-graph-row strong").filter({ hasText: /^Implement frontend$/ }),
  ).toBeVisible();
  expect(await graph.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);

  await graph.locator(".task-graph-header").click();
  await expect(graph.locator(".task-graph-row")).toHaveCount(0);
  await expect(graph).toContainText("3 subagents running");

  await graph.locator(".task-graph-header").click();
  await expect(graph.locator(".task-graph-row")).toHaveCount(6);
});
