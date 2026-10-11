import { test, expect } from "@playwright/test";
import type { ConversationWithState } from "../src/types";
import { createConversationViaAPIWithDetails } from "./helpers";

test.use({ timezoneId: "America/New_York" });

test("browse last-touched days with search, tags, grouping, archives, and reset", async ({
  page,
  request,
}) => {
  await page.clock.setFixedTime(new Date("2026-10-08T16:00:00Z"));
  const today = await createConversationViaAPIWithDetails(request, "day paging today");
  const yesterday = await createConversationViaAPIWithDetails(request, "day paging yesterday");
  const earlier = await createConversationViaAPIWithDetails(request, "day paging earlier");
  const response = await request.get("/api/conversations");
  expect(response.ok()).toBeTruthy();
  const corpus = (await response.json()) as ConversationWithState[];
  const fixtures = [
    [today.conversationId, "2026-10-08T04:00:00Z", "day-today"],
    [yesterday.conversationId, "2026-10-08T03:59:59Z", "day-yesterday"],
    [earlier.conversationId, "2026-10-06T16:00:00Z", "day-earlier"],
  ].map(([id, updated_at, tag]) => {
    const conversation = corpus.find((c) => c.conversation_id === id);
    expect(conversation).toBeTruthy();
    return {
      ...conversation!,
      created_at: "2025-01-01T00:00:00Z",
      updated_at,
      tags: JSON.stringify([tag]),
    };
  });
  await page.route("**/api/conversations", (route) => route.fulfill({ json: fixtures }));
  await page.route("**/api/conversations/snapshot", (route) =>
    route.fulfill({ json: { conversations: fixtures, hash: "day-filter-fixture" } }),
  );
  await page.route("**/api/conversations/search?*", (route) => route.fulfill({ json: fixtures }));
  await page.route("**/api/conversations/archived", (route) =>
    route.fulfill({ json: fixtures.map((c) => ({ ...c, archived: true })) }),
  );
  await page.route("**/api/stream2*", (route) => route.abort());
  await page.goto("/");
  await page.getByRole("button", { name: "Open conversations", exact: true }).click();
  await page.getByRole("button", { name: "Search conversations..." }).click();

  const picker = page.getByTestId("conversation-day-filter");
  const row = (id: string) => page.locator(`.conversation-item[data-conversation-id="${id}"]`);
  const previous = picker.getByRole("button", { name: "Previous day" });
  const next = picker.getByRole("button", { name: "Next day" });
  await expect(picker.locator(".drawer-day-label")).toHaveText("All days");
  await expect(row(today.conversationId)).toBeVisible();
  await expect(row(yesterday.conversationId)).toBeVisible();
  await expect(next).toBeDisabled();

  await picker.getByRole("button", { name: "Today", exact: true }).click();
  await expect(picker.locator(".drawer-day-label")).toHaveText("Today");
  await expect(row(today.conversationId)).toBeVisible();
  await expect(row(yesterday.conversationId)).toBeHidden();
  await expect(next).toBeDisabled();
  await previous.click();
  await expect(picker.locator(".drawer-day-label")).toHaveText("Yesterday");
  await expect(row(today.conversationId)).toBeHidden();
  await expect(row(yesterday.conversationId)).toBeVisible();
  await expect(next).toBeEnabled();

  await page.getByRole("button", { name: "Add tag filter" }).click();
  await expect(
    page.locator('[data-testid="tag-filter-option"][data-tag="day-yesterday"]'),
  ).toBeVisible();
  await expect(page.locator('[data-testid="tag-filter-option"][data-tag="day-today"]')).toHaveCount(
    0,
  );
  await page.locator('[data-testid="tag-filter-option"][data-tag="day-yesterday"]').click();
  await next.click();
  await expect(row(yesterday.conversationId)).toBeHidden();
  await page.getByRole("button", { name: "Clear search", exact: true }).click();
  await expect(row(today.conversationId)).toBeVisible();

  await page.getByRole("button", { name: "Group conversations" }).click();
  await page
    .locator(".group-by-menu-item")
    .filter({ hasText: /^Directory$/ })
    .click();
  await expect(row(today.conversationId)).toBeVisible();
  await expect(row(yesterday.conversationId)).toBeHidden();
  await page.getByRole("button", { name: "Group conversations" }).click();
  await page
    .locator(".group-by-menu-item")
    .filter({ hasText: /^No grouping$/ })
    .click();

  const search = page.locator(".drawer-search-input");
  await search.fill("day paging");
  await expect(row(today.conversationId)).toBeVisible();
  await expect(row(yesterday.conversationId)).toBeHidden();
  await previous.click();
  await expect(row(yesterday.conversationId)).toBeVisible();
  await expect(row(today.conversationId)).toBeHidden();
  await page.getByRole("button", { name: "Clear search", exact: true }).click();

  await page.getByRole("button", { name: "View Archived" }).click();
  await expect(row(yesterday.conversationId)).toBeVisible();
  await expect(row(today.conversationId)).toBeHidden();
  await page.getByRole("button", { name: "Back to conversations" }).click();
  await previous.click();
  await expect(row(earlier.conversationId)).toBeVisible();
  expect(await picker.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBeTruthy();
  await previous.click();
  await expect(page.getByTestId("day-filter-empty")).toBeVisible();
  await expect(next).toBeEnabled();
  await picker.getByRole("button", { name: "Today", exact: true }).click();
  await expect(row(today.conversationId)).toBeVisible();

  await page.getByRole("button", { name: "Search conversations..." }).click();
  await expect(picker).toBeHidden();
  await expect(row(yesterday.conversationId)).toBeHidden();
  await page.getByRole("button", { name: "Search conversations..." }).click();
  await expect(picker.locator(".drawer-day-label")).toHaveText("Today");
  await picker.getByRole("button", { name: "All days", exact: true }).click();
  await expect(row(today.conversationId)).toBeVisible();
  await expect(row(yesterday.conversationId)).toBeVisible();
  await expect(row(earlier.conversationId)).toBeVisible();

  await page.setViewportSize({ width: 1280, height: 800 });
  const fits = await picker.evaluate((el) => el.scrollWidth <= el.clientWidth);
  expect(fits).toBeTruthy();
});
