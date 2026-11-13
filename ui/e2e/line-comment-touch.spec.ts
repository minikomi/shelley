import { expect, test, type Locator, type Page } from "@playwright/test";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  createConversationViaAPI,
  git,
  initGitRepo,
  openWorkspaceTool,
  withTempDir,
} from "./helpers";

async function checkTouchComments(page: Page, line: Locator, label: string) {
  await expect(line).toBeVisible();
  const box = await line.boundingBox();
  if (!box) throw new Error("Line has no bounding box");
  const cdp = await page.context().newCDPSession(page);
  const touch = (
    type: "touchStart" | "touchMove" | "touchEnd" | "touchCancel",
    points: { x: number; y: number }[],
  ) => cdp.send("Input.dispatchTouchEvent", { type, touchPoints: points });
  const dialog = page.locator(".diff-viewer-comment-dialog");
  const lineEditor = line.locator(
    "xpath=ancestor::div[contains(concat(' ', normalize-space(@class), ' '), ' monaco-editor ')][1]",
  );
  await lineEditor.evaluate((el) => el.setAttribute("data-touch-test-editor", ""));
  const editor = page.locator("[data-touch-test-editor]");
  const slider = editor.locator(".scrollbar.vertical .slider");
  const before = await slider.boundingBox();
  if (!before) throw new Error("Editor scrollbar has no bounding box");
  const editorBox = await editor.boundingBox();
  if (!editorBox) throw new Error("Editor has no bounding box");
  const x = editorBox.x + editorBox.width / 2;
  const y = editorBox.y + editorBox.height / 2;


  await touch("touchStart", [{ x, y }]);
  for (let distance = 10; distance <= 100; distance += 10) {
    await touch("touchMove", [{ x, y: y - distance }]);
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())));
  }
  await touch("touchEnd", []);

  await expect(dialog).not.toBeVisible();
  await expect.poll(async () => (await slider.boundingBox())?.y).toBeGreaterThan(before.y);

  const reset = { x: editorBox.x + editorBox.width / 2, y: editorBox.y + editorBox.height / 2 };
  await touch("touchStart", [reset]);
  for (let distance = 10; distance <= 150; distance += 10) {
    await touch("touchMove", [{ x: reset.x, y: reset.y + distance }]);
    await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())));
  }
  await touch("touchEnd", []);
  await expect(line).toBeVisible();
  const next = await line.boundingBox();
  if (!next) throw new Error("Line has no bounding box after scroll");
  const point = { x: next.x + Math.min(30, next.width / 2), y: next.y + next.height / 2 };
  await touch("touchStart", [point]);
  await touch("touchCancel", []);
  await expect(dialog).not.toBeVisible();

  await page.evaluate(() => {
    document.body.dataset.commentDialogMounts = "0";
    new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (
            node instanceof Element &&
            (node.matches(".diff-viewer-comment-dialog") ||
              node.querySelector(".diff-viewer-comment-dialog"))
          ) {
            document.body.dataset.commentDialogMounts = String(
              Number(document.body.dataset.commentDialogMounts) + 1,
            );
          }
        }
      }
    }).observe(document.body, { childList: true, subtree: true });
  });
  await touch("touchStart", [point]);
  await touch("touchEnd", []);
  await expect(dialog).toContainText(label);
  await expect(dialog.locator(".diff-viewer-selected-text")).toContainText("bravo second line");
  await expect(page.locator("body")).toHaveAttribute("data-comment-dialog-mounts", "1");
  await cdp.detach();
}

test("touch line comments in the file editor reject scrolling and cancellation", async ({
  page,
  request,
}) => {
  await withTempDir("shelley-touch-file-", async (cwd) => {
    writeFileSync(
      join(cwd, "notes.txt"),
      `alpha first line\nbravo second line\n${"trailing line\n".repeat(100)}`,
    );
    const slug = await createConversationViaAPI(request, "Hello", { cwd });
    await page.goto(`/c/${slug}`);
    await page.keyboard.press("ControlOrMeta+P");
    const finder = page.locator(".grp-filter");
    await expect(finder).toBeVisible();
    await finder.fill("notes.txt");
    await expect(page.locator(".grp-body").getByText("notes.txt")).toBeVisible();
    await finder.press("Enter");
    const modal = page.getByRole("dialog", { name: /Edit .*notes\.txt/ });
    await expect(modal).toBeVisible();
    await modal.getByRole("button", { name: "Comment mode" }).click();
    await checkTouchComments(
      page,
      modal.locator(".view-line", { hasText: "bravo second line" }),
      "Add Comment (Line 2)",
    );
  });
});

test("touch line comments in the diff viewer reject scrolling and cancellation", async ({
  page,
  request,
}) => {
  await withTempDir("shelley-touch-diff-", async (cwd) => {
    initGitRepo(cwd);
    writeFileSync(
      join(cwd, "notes.txt"),
      `alpha first line\nold second line\n${"trailing line\n".repeat(100)}`,
    );
    git(cwd, "add", "notes.txt");
    git(cwd, "commit", "-m", "initial");
    writeFileSync(
      join(cwd, "notes.txt"),
      `alpha first line\nbravo second line\n${"trailing line\n".repeat(100)}`,
    );
    git(cwd, "commit", "-am", "update");
    const slug = await createConversationViaAPI(request, "Hello", { cwd });
    await page.goto(`/c/${slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible();
    await openWorkspaceTool(page, "Diffs");
    const overlay = page.locator(".diff-viewer-overlay");
    const fileSelect = overlay.locator("select.diff-viewer-select").first();
    await expect(fileSelect.locator('option[value="notes.txt"]')).toHaveCount(1);
    await fileSelect.selectOption("notes.txt");
    await checkTouchComments(
      page,
      overlay.locator(".editor.modified .view-line", { hasText: "bravo second line" }),
      "Add Comment (Line 2, new)",
    );
  });
});
