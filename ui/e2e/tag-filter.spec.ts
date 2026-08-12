import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

async function setTags(
  request: APIRequestContext,
  conversationId: string,
  tags: string[],
): Promise<void> {
  const resp = await request.post(`/api/conversation/${conversationId}/tags`, { data: { tags } });
  expect(resp.ok()).toBeTruthy();
}

// Every spec shares one server, so other tests' conversations are in the
// drawer too. Scope assertions to the rows we created by conversation id.
function row(page: Page, conversationId: string) {
  return page.locator(`.conversation-item[data-conversation-id="${conversationId}"]`);
}

async function openDrawer(page: Page): Promise<void> {
  const drawer = page.locator(".drawer");
  if (!(await drawer.evaluate((el) => el.classList.contains("open")))) {
    await page.locator('button[aria-label="Open conversations"]').click();
  }
  await expect(page.locator(".drawer.open")).toBeVisible();
}

// These tests share one server and one drawer, so run them in order rather
// than letting a sibling's tags appear mid-assertion.
test.describe.configure({ mode: "serial" });

test.describe("Group by tag", () => {
  test("tag groups are ordered by first tag, then second", async ({ page, request }) => {
    // The four sets from the ordering rule: `#gо-a #gо-b`, `#gо-b`,
    // `#gо-b #gо-c`, `#gо-d`-style. Created in a deliberately wrong order so
    // the assertion cannot pass by accident of creation time.
    const bd = await createConversationViaAPIWithDetails(request, "order tags bd", { cwd: "/tmp" });
    const b = await createConversationViaAPIWithDetails(request, "order tags b", { cwd: "/tmp" });
    const ab = await createConversationViaAPIWithDetails(request, "order tags ab", { cwd: "/tmp" });
    const bc = await createConversationViaAPIWithDetails(request, "order tags bc", { cwd: "/tmp" });
    await setTags(request, bd.conversationId, ["ord-b", "ord-d"]);
    await setTags(request, b.conversationId, ["ord-b"]);
    await setTags(request, ab.conversationId, ["ord-a", "ord-b"]);
    await setTags(request, bc.conversationId, ["ord-b", "ord-c"]);

    await page.goto(`/c/${b.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);
    await page.locator('button[aria-label="Group conversations"]').click();
    await page.getByRole("button", { name: "Tags", exact: true }).click();

    // Scope to our own groups: the shared server has other tagged rows.
    const ours = page.locator(".conversation-group").filter({ hasText: /#ord-/ });
    await expect(ours.locator(".conversation-group-label")).toHaveText([
      "#ord-a #ord-b",
      "#ord-b",
      "#ord-b #ord-c",
      "#ord-b #ord-d",
    ]);
  });

  test("group by tag buckets whole tag sets, with untagged last", async ({ page, request }) => {
    const both = await createConversationViaAPIWithDetails(request, "group tags both", {
      cwd: "/tmp",
    });
    const bothAgain = await createConversationViaAPIWithDetails(request, "group tags both again", {
      cwd: "/tmp",
    });
    const single = await createConversationViaAPIWithDetails(request, "group tags single", {
      cwd: "/tmp",
    });
    const none = await createConversationViaAPIWithDetails(request, "group tags none", {
      cwd: "/tmp",
    });
    // Same set, opposite order: these must land in ONE group.
    await setTags(request, both.conversationId, ["gt-red", "gt-blue"]);
    await setTags(request, bothAgain.conversationId, ["gt-blue", "gt-red"]);
    await setTags(request, single.conversationId, ["gt-red"]);
    await setTags(request, none.conversationId, []);

    await page.goto(`/c/${both.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    await page.locator('button[aria-label="Group conversations"]').click();
    await page.getByRole("button", { name: "Tags", exact: true }).click();

    // Order-insensitive: one group holds both conversations, labelled with the
    // sorted set.
    const pairGroup = page.locator(".conversation-group").filter({
      has: row(page, both.conversationId),
    });
    await expect(pairGroup).toHaveCount(1);
    await expect(pairGroup.locator(".conversation-group-label")).toHaveText("#gt-blue #gt-red");
    // The narrow drawer truncates long labels, so the hover title carries the
    // full readable tag set (not the internal NUL-joined key).
    await expect(pairGroup.locator(".conversation-group-label")).toHaveAttribute(
      "title",
      "#gt-blue #gt-red",
    );
    await expect(
      pairGroup.locator(`[data-conversation-id="${bothAgain.conversationId}"]`),
    ).toHaveCount(1);

    // A subset is its own group, not folded into the pair.
    const singleGroup = page.locator(".conversation-group").filter({
      has: row(page, single.conversationId),
    });
    await expect(singleGroup.locator(".conversation-group-label")).toHaveText("#gt-red");
    await expect(
      singleGroup.locator(`[data-conversation-id="${both.conversationId}"]`),
    ).toHaveCount(0);

    // Untagged conversations collect under their own heading.
    const noneGroup = page.locator(".conversation-group").filter({
      has: row(page, none.conversationId),
    });
    await expect(noneGroup.locator(".conversation-group-label")).toHaveText("Untagged");

    // Every conversation appears exactly once overall -- the property a
    // tag-per-group layout would break.
    await expect(page.locator(`[data-conversation-id="${both.conversationId}"]`)).toHaveCount(1);
  });
});
