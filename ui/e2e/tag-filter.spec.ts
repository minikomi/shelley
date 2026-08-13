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

async function getGitRoot(request: APIRequestContext): Promise<string> {
  const resp = await request.get("/api/git/diffs?cwd=.");
  expect(resp.ok()).toBeTruthy();
  const data = await resp.json();
  expect(data.gitRoot).toBeTruthy();
  return data.gitRoot;
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

const panel = (page: Page) => page.getByTestId("tag-filter-panel");
const option = (page: Page, tag: string) =>
  page.locator(`[data-testid="tag-filter-option"][data-tag="${tag}"]`);
const searchBox = (page: Page) => page.locator(".drawer-search-input");

async function openSearch(page: Page) {
  if ((await searchBox(page).count()) === 0) {
    await page.locator('button[aria-label="Search conversations..."]').click();
  }
  await expect(searchBox(page)).toBeVisible();
  return searchBox(page);
}

// These tests share one server and one drawer, so run them in order rather
// than letting a sibling's tags appear mid-assertion.
test.describe.configure({ mode: "serial" });

test.describe("Tag filter", () => {
  test("tag: opens a dropdown, completes, ANDs, and narrows the offers", async ({
    page,
    request,
  }) => {
    // alpha: tf-alpha, tf-shared   beta: tf-beta, tf-shared   gamma: tf-gamma
    // tf-alpha and tf-beta never co-occur, so tf-beta must not be offered
    // once tf-alpha is selected.
    const alpha = await createConversationViaAPIWithDetails(request, "tag filter alpha", {
      cwd: "/tmp",
    });
    const beta = await createConversationViaAPIWithDetails(request, "tag filter beta", {
      cwd: "/tmp",
    });
    const gamma = await createConversationViaAPIWithDetails(request, "tag filter gamma", {
      cwd: "/tmp",
    });
    await setTags(request, alpha.conversationId, ["tf-alpha", "tf-shared"]);
    await setTags(request, beta.conversationId, ["tf-beta", "tf-shared"]);
    await setTags(request, gamma.conversationId, ["tf-gamma"]);

    await page.goto(`/c/${gamma.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    const search = await openSearch(page);

    // No dropdown until the `tag:` cue is typed.
    await expect(panel(page)).toHaveCount(0);
    await search.fill("tag:");
    await expect(panel(page)).toBeVisible();
    // Counts are the size of the result set each tag would produce.
    await expect(option(page, "tf-shared")).toContainText("2");
    await expect(option(page, "tf-alpha")).toContainText("1");

    // Typing after the prefix narrows the dropdown by substring...
    await search.fill("tag:sha");
    await expect(option(page, "tf-shared")).toBeVisible();
    await expect(option(page, "tf-alpha")).toHaveCount(0);

    // ...and clicking one completes the term in place, leaving a trailing
    // space so the next keystroke starts fresh.
    await option(page, "tf-shared").click();
    await expect(search).toHaveValue("tag:tf-shared ");

    // The list is filtered.
    await expect(row(page, alpha.conversationId)).toBeVisible();
    await expect(row(page, beta.conversationId)).toBeVisible();
    await expect(row(page, gamma.conversationId)).toHaveCount(0);

    // A second tag ANDs, and the offers narrow: tf-gamma never co-occurs with
    // tf-shared, so it is not on offer.
    await search.fill("tag:tf-shared tag:");
    await expect(panel(page)).toBeVisible();
    await expect(option(page, "tf-gamma")).toHaveCount(0);
    await expect(option(page, "tf-shared")).toHaveCount(0); // already selected
    await expect(option(page, "tf-alpha")).toBeVisible();

    await option(page, "tf-alpha").click();
    await expect(row(page, alpha.conversationId)).toBeVisible();
    await expect(row(page, beta.conversationId)).toHaveCount(0);

    // Clearing the search clears the filter with it: one input, one state.
    await search.fill("");
    await expect(row(page, alpha.conversationId)).toBeVisible();
    await expect(row(page, beta.conversationId)).toBeVisible();
    await expect(row(page, gamma.conversationId)).toBeVisible();
  });

  test("keyboard drives the dropdown, and Escape peels one layer at a time", async ({
    page,
    request,
  }) => {
    const kb = await createConversationViaAPIWithDetails(request, "keyboard tag conversation", {
      cwd: "/tmp",
    });
    await setTags(request, kb.conversationId, ["kb-only"]);

    await page.goto(`/c/${kb.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);
    const search = await openSearch(page);

    await search.fill("tag:kb-on");
    await expect(option(page, "kb-only")).toBeVisible();
    // Enter takes the highlighted entry.
    await page.keyboard.press("Enter");
    await expect(search).toHaveValue("tag:kb-only ");
    await expect(panel(page)).toHaveCount(0);

    // Escape closes the dropdown first, keeping the query intact...
    await search.fill("tag:kb-only tag:");
    await expect(panel(page)).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(panel(page)).toHaveCount(0);
    await expect(search).toHaveValue("tag:kb-only tag:");
    // ...and the dropdown does not spring back until the term changes.
    await expect(panel(page)).toHaveCount(0);

    // A second Escape clears the query, a third closes the search box.
    await page.keyboard.press("Escape");
    await expect(search).toHaveValue("");
    await page.keyboard.press("Escape");
    await expect(searchBox(page)).toHaveCount(0);
  });

  test("tag terms and free text are independent predicates", async ({ page, request }) => {
    const hit = await createConversationViaAPIWithDetails(request, "searchable kumquat marker", {
      cwd: "/tmp",
    });
    const other = await createConversationViaAPIWithDetails(request, "searchable kumquat other", {
      cwd: "/tmp",
    });
    await setTags(request, hit.conversationId, ["sf-keep"]);
    await setTags(request, other.conversationId, ["sf-drop"]);

    await page.goto(`/c/${hit.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);
    const search = await openSearch(page);

    // Text and tag in one query: both must hold.
    await search.fill("kumquat tag:sf-keep ");
    await expect(row(page, hit.conversationId)).toBeVisible();
    await expect(row(page, other.conversationId)).toHaveCount(0);

    // Order does not matter -- they are ANDed predicates, not a pipeline.
    await search.fill("tag:sf-keep kumquat");
    await expect(row(page, hit.conversationId)).toBeVisible();
    await expect(row(page, other.conversationId)).toHaveCount(0);

    // A search that matches nothing is a search miss, NOT a filter miss: the
    // filter is only to blame when clearing it would bring rows back.
    await search.fill("zzz-no-such-conversation-zzz tag:sf-keep ");
    await expect(page.getByTestId("tag-filter-empty")).toHaveCount(0);
    await expect(page.locator(".drawer-empty-state")).toContainText("No matching conversations");

    // A search that DOES match, whose hits the tag then removes, IS a filter
    // miss -- and its clear action keeps the text but drops the tags.
    await search.fill("kumquat marker tag:sf-drop ");
    await expect(page.getByTestId("tag-filter-empty")).toBeVisible();
    await page.getByTestId("tag-filter-empty-clear").click();
    // The free text survives; only the tag terms are dropped.
    await expect(search).toHaveValue("kumquat marker ");
    await expect(row(page, hit.conversationId)).toBeVisible();
  });

  test("row tag chips write into the query and compose with git-repo grouping", async ({
    page,
    request,
  }) => {
    const gitRoot = await getGitRoot(request);
    const inRepo = await createConversationViaAPIWithDetails(request, "chip filter in repo", {
      cwd: gitRoot,
    });
    const outOfRepo = await createConversationViaAPIWithDetails(request, "chip filter elsewhere", {
      cwd: "/tmp",
    });
    await setTags(request, inRepo.conversationId, ["chip-tag"]);
    await setTags(request, outOfRepo.conversationId, ["chip-other"]);

    await page.goto(`/c/${inRepo.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    // Discovery path: click the #chip-tag chip on the row itself. It opens the
    // search box and writes the term, so the mechanism is immediately visible.
    const chip = row(page, inRepo.conversationId)
      .getByTestId("conversation-tag-chip")
      .filter({ hasText: "chip-tag" });
    await expect(chip).toBeVisible();
    await chip.click();

    await expect(searchBox(page)).toHaveValue("tag:chip-tag ");
    await expect(row(page, inRepo.conversationId)).toBeVisible();
    await expect(row(page, outOfRepo.conversationId)).toHaveCount(0);
    // Clicking a chip must not also navigate away from the current chat.
    await expect(page).toHaveURL(new RegExp(`/c/${inRepo.slug}$`));
    await expect(chip).toHaveAttribute("aria-pressed", "true");

    // A tag-only query is a filter, not a search: the list keeps its grouping.
    await page.locator('button[aria-label="Group conversations"]').click();
    await page.getByRole("button", { name: "Git Repo" }).click();
    const group = page.locator(".conversation-group").filter({
      has: row(page, inRepo.conversationId),
    });
    await expect(group).toHaveCount(1);
    await expect(group.locator(".conversation-group-label")).not.toHaveText("Other");

    // Clicking the same chip again removes its term.
    await chip.click();
    await expect(searchBox(page)).toHaveValue("");
    await expect(row(page, outOfRepo.conversationId)).toBeVisible();
  });

  test("a tag containing a space is quoted, and round-trips", async ({ page, request }) => {
    // The server's normalizeTags allows spaces, so `tag:in progress` unquoted
    // would parse as the tag `in` plus the word `progress`.
    const spaced = await createConversationViaAPIWithDetails(request, "spaced tag conversation", {
      cwd: "/tmp",
    });
    const plain = await createConversationViaAPIWithDetails(request, "spaced tag other", {
      cwd: "/tmp",
    });
    await setTags(request, spaced.conversationId, ["sp tag"]);
    await setTags(request, plain.conversationId, ["sp-plain"]);

    await page.goto(`/c/${spaced.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);

    // The chip writes the quoted term…
    const chip = row(page, spaced.conversationId)
      .getByTestId("conversation-tag-chip")
      .filter({ hasText: "sp tag" });
    await chip.click();
    await expect(searchBox(page)).toHaveValue('tag:"sp tag" ');
    await expect(row(page, spaced.conversationId)).toBeVisible();
    await expect(row(page, plain.conversationId)).toHaveCount(0);

    // …and clicking it again removes the whole quoted term.
    await chip.click();
    await expect(searchBox(page)).toHaveValue("");

    // Completing from the dropdown quotes too, and typing inside an open
    // quote — space included — keeps narrowing instead of committing.
    const search = searchBox(page);
    await search.fill('tag:"sp t');
    await expect(option(page, "sp tag")).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(search).toHaveValue('tag:"sp tag" ');
    await expect(row(page, spaced.conversationId)).toBeVisible();
    await expect(row(page, plain.conversationId)).toHaveCount(0);

    // A tag containing a quote mark is backslash-escaped, and round-trips
    // through its chip the same way.
    const quoted = 'sp" quote';
    await setTags(request, spaced.conversationId, ["sp tag", quoted]);
    await search.fill("");
    const quoteChip = row(page, spaced.conversationId)
      .locator(`[data-testid="conversation-tag-chip"][data-tag='${quoted}']`);
    await quoteChip.click();
    await expect(searchBox(page)).toHaveValue('tag:"sp\\" quote" ');
    await expect(row(page, spaced.conversationId)).toBeVisible();
    await expect(row(page, plain.conversationId)).toHaveCount(0);
    await quoteChip.click();
    await expect(searchBox(page)).toHaveValue("");
  });

  test("is:untagged filters to untagged, in its own namespace", async ({ page, request }) => {
    const tagged = await createConversationViaAPIWithDetails(request, "untagged probe tagged", {
      cwd: "/tmp",
    });
    const bare = await createConversationViaAPIWithDetails(request, "untagged probe bare", {
      cwd: "/tmp",
    });
    // A tag literally named "untagged" — the collision that `is:` avoids.
    const literal = await createConversationViaAPIWithDetails(request, "untagged probe literal", {
      cwd: "/tmp",
    });
    await setTags(request, tagged.conversationId, ["ut-real"]);
    await setTags(request, bare.conversationId, []);
    await setTags(request, literal.conversationId, ["untagged"]);

    await page.goto(`/c/${bare.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await openDrawer(page);
    const search = await openSearch(page);

    // Offered in the dropdown alongside the tags, and completing it inserts
    // the whole `is:untagged` token.
    await search.fill("tag:");
    await expect(page.getByTestId("tag-filter-panel")).toBeVisible();
    // Matched by test id, not by text: this very test creates a tag named
    // "untagged", so "the option reading Untagged" is genuinely ambiguous.
    await page.getByTestId("tag-filter-untagged-option").click();
    await expect(search).toHaveValue("is:untagged ");

    await expect(row(page, bare.conversationId)).toBeVisible();
    await expect(row(page, tagged.conversationId)).toHaveCount(0);
    // The conversation tagged #untagged HAS a tag, so it is excluded.
    await expect(row(page, literal.conversationId)).toHaveCount(0);

    // ...and that tag stays addressable on its own terms, meaning the
    // opposite: only the conversation carrying it.
    await search.fill("tag:untagged ");
    await expect(row(page, literal.conversationId)).toBeVisible();
    await expect(row(page, bare.conversationId)).toHaveCount(0);

    // Contradictory query: nothing has a tag and no tags.
    await search.fill("is:untagged tag:ut-real ");
    await expect(page.getByTestId("tag-filter-empty")).toBeVisible();
    // Its clear action drops both terms.
    await page.getByTestId("tag-filter-empty-clear").click();
    await expect(search).toHaveValue("");
  });

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

    // Grouping and filtering are orthogonal: they compose.
    const search = await openSearch(page);
    await search.fill("tag:gt-blue ");
    await expect(row(page, both.conversationId)).toBeVisible();
    await expect(row(page, single.conversationId)).toHaveCount(0);
    await expect(row(page, none.conversationId)).toHaveCount(0);
    await expect(pairGroup.locator(".conversation-group-label")).toHaveText("#gt-blue #gt-red");
  });
});
