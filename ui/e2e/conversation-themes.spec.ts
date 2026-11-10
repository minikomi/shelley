import { expect, test } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

test("chapter hover stays open, caches its data, and jumps to a message", async ({ page, request }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: Chapter anchor");
  const other = await createConversationViaAPIWithDetails(request, "echo: Start elsewhere");
  const response = await request.get(`/api/conversation/${conversationId}`);
  const { messages } = await response.json();
  const messageId = messages.find((m: { type: string }) => m.type === "user").message_id;
  let reads = 0;
  await page.route(`**/api/conversation/${conversationId}/themes`, (route) => {
    reads++;
    return route.fulfill({
      json: {
        chapters: [{ title: "Exploring the first idea", message_id: messageId }],
        through_message_id: messageId,
      },
    });
  });
  await page.goto(`/c/${other.slug}`);
  const open = page.getByRole("button", { name: "Open conversations", exact: true });
  if (await open.isVisible()) await open.click();
  const expand = page.getByRole("button", { name: "Expand sidebar", exact: true });
  if (await expand.isVisible()) await expand.click();
  const row = page.locator(`.conversation-item[data-conversation-id="${conversationId}"]`);
  await row.hover();
  const card = page.locator(".conversation-themes-popover");
  const link = card.getByRole("link", { name: "Exploring the first idea" });
  await expect(link).toBeVisible();
  await link.hover();
  await expect(card).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(card).toBeHidden();
  await page.mouse.move(1400, 850);
  await row.hover();
  await expect(link).toBeVisible();
  expect(reads).toBe(1);
  await link.click();
  await expect(page).toHaveURL(new RegExp(`message_id=${messageId}`));
  await expect(page.locator(`[data-message-id="${messageId}"].message-highlight`)).toBeVisible();
});

test("a completed fetch does not reopen a row the pointer left", async ({ page, request }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const { conversationId } = await createConversationViaAPIWithDetails(request, "echo: No stale hover");
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route(`**/api/conversation/${conversationId}/themes`, async (route) => {
    await gate;
    await route.fulfill({ json: {
      chapters: [{ title: "Must not reopen", message_id: "unused" }],
      through_message_id: "unused",
    } });
  });
  await page.goto(`/c/${conversationId}`);
  const open = page.getByRole("button", { name: "Open conversations", exact: true });
  if (await open.isVisible()) await open.click();
  const expand = page.getByRole("button", { name: "Expand sidebar", exact: true });
  if (await expand.isVisible()) await expand.click();
  const pending = page.waitForRequest(`**/api/conversation/${conversationId}/themes`);
  await page.locator(`.conversation-item[data-conversation-id="${conversationId}"]`).hover();
  await pending;
  await page.mouse.move(1400, 850);
  const completed = page.waitForResponse(`**/api/conversation/${conversationId}/themes`);
  release();
  await completed;
  await expect(page.getByText("Must not reopen", { exact: true })).toBeHidden();
});
