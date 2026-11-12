import { expect, test } from "@playwright/test";

test("Live settings preserve current behavior until explicitly changed", async ({ page, request }) => {
  const baseline = { voice: "marin", speaking_pace: 0, progress_updates: false };
  expect(await (await request.get("/api/live-settings")).json()).toEqual(baseline);
  await page.goto("/new");

  const dialog = page.getByRole("dialog", { name: "Live settings" });
  const open = async () => {
    await page.keyboard.press("ControlOrMeta+k");
    await page.locator(".command-palette-input").fill("Live settings");
    await page.locator(".command-palette-item").filter({ hasText: "Live settings" }).click();
    await expect(dialog.locator("#live-settings-sound")).toHaveValue("marin");
  };

  await open();
  await expect(dialog).toContainText("Leave settings unchanged to keep current Live behavior");
  await expect(dialog.locator("#live-settings-pace")).toHaveValue("0");
  await expect(dialog.getByRole("switch")).not.toBeChecked();
  await dialog.getByRole("switch").check();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(await (await request.get("/api/live-settings")).json()).toEqual(baseline);

  await open();
  await expect(dialog.getByRole("switch")).not.toBeChecked();
  const saved = page.waitForResponse((r) =>
    r.url().endsWith("/api/live-settings") && r.request().method() === "PUT",
  );
  await dialog.getByRole("button", { name: "Save", exact: true }).click();
  expect((await saved).ok()).toBeTruthy();
  await expect(dialog).not.toBeVisible();
  expect(await (await request.get("/api/live-settings")).json()).toEqual(baseline);
});
