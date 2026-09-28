import { expect, test } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

test("opens terminals for bare shell commands", async ({ page, request }) => {
  const { conversationId } = await createConversationViaAPIWithDetails(
    request,
    "terminal composer test",
  );
  await page.goto(`/c/${conversationId}`);

  const input = page.getByTestId("message-input");
  await input.fill("!");
  await page.getByTestId("send-button").click();
  await expect(page.locator(".terminal-panel-tab")).toHaveCount(1);
  await expect(input).toHaveValue("");

  await input.fill("/shell");
  await expect(input).toHaveValue("!");
  await page.getByTestId("send-button").click();
  await expect(page.locator(".terminal-panel-tab")).toHaveCount(2);
  await expect(input).toHaveValue("");
});
