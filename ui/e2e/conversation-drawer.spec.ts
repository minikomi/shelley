import { expect, test, type Page } from "@playwright/test";
import type { ConversationWithState } from "../src/types";

test.use({
  viewport: { width: 1280, height: 720 },
  isMobile: false,
  hasTouch: false,
});

function conversation(
  id: string,
  isDraft = false,
  participants?: Array<string | [string, number]>,
): ConversationWithState {
  return {
    conversation_id: id,
    slug: isDraft ? null : id,
    user_initiated: true,
    created_at: "2026-07-23T12:00:00Z",
    updated_at: "2026-07-23T12:00:00Z",
    cwd: null,
    archived: false,
    parent_conversation_id: null,
    model: "predictable",
    conversation_options: "{}",
    current_generation: 1,
    agent_working: false,
    tags: "[]",
    is_draft: isDraft,
    draft: isDraft ? "unfinished message" : "",
    queued_messages: "[]",
    working: false,
    subagent_count: 0,
    preview: "Preview",
    max_sequence_id: 0,
    participants: participants?.map((participant) => ({
      email: typeof participant === "string" ? participant : participant[0],
      message_count: typeof participant === "string" ? 1 : participant[1],
    })),
  };
}

async function stubConversationList(page: Page, conversations: ConversationWithState[]) {
  await page.route("**/api/conversations/snapshot", (route) =>
    route.fulfill({ json: { conversations, hash: `test-${conversations.length}` } }),
  );
  await page.route("**/api/conversations/search**", (route) =>
    route.fulfill({ json: conversations }),
  );
  await page.route("**/api/stream2**", (route) => route.abort());
}

function editableFilter(page: Page, kind: "tag" | "user") {
  const testId = kind === "tag" ? "selected-tag-filter" : "user-filter-token";
  return page
    .getByTestId(testId)
    .filter({ has: page.locator(`input[data-structured-edit-kind="${kind}"]`) });
}

test.describe("conversation drawer startup and app bar", () => {
  test("single-user lists have no participant filter", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    await stubConversationList(page, [
      conversation("mine", false, ["me@example.com"]),
      conversation("draft", true),
    ]);

    await page.goto("/new");

    await expect(page.getByTestId("user-filter-token")).toHaveCount(0);
    await expect(page.getByTestId("unattributed-filter-token")).toHaveCount(0);
    await expect(page.locator(".drawer-search-input")).toHaveCount(0);
    await expect(page.locator(".conversation-participant-badge")).toHaveCount(0);
    await expect(page.locator('[data-conversation-id="mine"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="draft"]')).toBeVisible();
  });

  test("multi-user lists seed the current-user query once", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    await stubConversationList(page, [
      conversation("mine", false, [
        ["me@example.com", 3],
        ["collaborator@example.com", 2],
      ]),
      conversation("triple", false, [
        "me@example.com",
        "collaborator@example.com",
        "third@example.com",
      ]),
      conversation("other", false, ["other@example.com"]),
      conversation("draft", true),
    ]);

    await page.goto("/new");

    const userToken = page.getByTestId("user-filter-token");
    const searchToggle = page.getByRole("button", { name: "Search conversations..." });
    await expect(page.locator(".drawer-search-input")).toHaveCount(0);
    await expect(searchToggle).toHaveClass(/search-toggle-active/);
    await expect(page.locator('[data-conversation-id="mine"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="draft"]')).not.toBeVisible();
    await expect(page.locator('[data-conversation-id="other"]')).not.toBeVisible();
    const mineBadge = page.locator('[data-conversation-id="mine"] .conversation-participant-badge');
    await expect(mineBadge).toContainText("2");
    await expect(mineBadge.locator(".conversation-participant-badge-front-filled")).toHaveCount(1);
    await mineBadge.click();
    await expect(page.locator(".p-tooltip")).toContainText("collaborator@example.com");
    await expect(page.locator(".p-tooltip strong")).toHaveText("me@example.com");
    await expect(
      page.locator(".p-tooltip .conversation-participant-tooltip-identity-current"),
    ).toHaveCount(1);
    await expect(page.locator(".p-tooltip .conversation-participant-tooltip-identity")).toHaveCount(
      2,
    );
    await expect(
      page
        .locator(".p-tooltip .conversation-participant-tooltip-row")
        .filter({ hasText: "collaborator@example.com" })
        .locator("strong"),
    ).toHaveCount(0);
    await expect(
      page
        .locator(".p-tooltip .conversation-participant-tooltip-row")
        .filter({ hasText: "me@example.com" })
        .locator(".conversation-participant-tooltip-count"),
    ).toHaveText("3");

    await searchToggle.click();
    await expect(userToken).toHaveText(/user:me@example\.com/);
    await expect(page.getByTestId("conversation-query-text").last()).toHaveAttribute(
      "autocapitalize",
      "none",
    );
    await expect(page.getByTestId("unattributed-filter-token")).toHaveCount(0);
    await expect(page.locator(".drawer-search-input")).toHaveValue("");
    await expect(page.getByText("@ Participating", { exact: true })).toHaveCount(0);

    const addUserFilter = page.getByRole("button", { name: "Add user filter" });
    const addTagFilter = page.getByRole("button", { name: "Add tag filter" });
    await addUserFilter.click();
    await expect(page.locator(".drawer-search-input")).toHaveValue("");
    await expect(editableFilter(page, "user").getByTestId("conversation-query-keyword")).toHaveText(
      "user:",
    );
    await addTagFilter.click();
    await expect(page.locator(".drawer-search-input")).toHaveValue("");
    await expect(editableFilter(page, "tag").getByTestId("conversation-query-keyword")).toHaveText(
      "tag:",
    );
    await addUserFilter.click();
    await addUserFilter.click();
    await expect(page.locator(".drawer-search-input")).toHaveValue("");
    await expect(editableFilter(page, "user").getByTestId("conversation-query-keyword")).toHaveText(
      "user:",
    );
    await page.locator(".drawer-search-input").press("Escape");

    const visibleIds = () =>
      page
        .locator("[data-conversation-id]:visible")
        .evaluateAll((rows) => rows.map((row) => row.getAttribute("data-conversation-id")));
    const defaultOrder = await visibleIds();
    await addUserFilter.click();
    const userPanel = page.getByTestId("user-filter-panel");
    await expect(userPanel.getByRole("option", { name: /other@example\.com/ })).toHaveCount(0);
    await userPanel.getByRole("option", { name: /third@example\.com/ }).click();
    await expect(userToken).toHaveCount(2);
    await userToken
      .filter({ hasText: "user:third@example.com" })
      .getByRole("button", { name: "Remove user:third@example.com filter" })
      .click();
    expect(await visibleIds()).toEqual(defaultOrder);

    await searchToggle.click();
    await expect(page.locator(".drawer-search-input")).toHaveCount(0);
    await expect(searchToggle).toHaveClass(/search-toggle-active/);
    await searchToggle.click();

    await userToken.getByRole("button", { name: "Remove user:me@example.com filter" }).click();
    await expect(page.locator('[data-conversation-id="other"]')).toBeVisible();
    await expect(page.locator('[data-conversation-id="draft"]')).toBeVisible();
    await expect(
      page
        .locator('[data-conversation-id="other"]')
        .getByRole("button", { name: "other@example.com" }),
    ).toContainText("1");
    await expect(userToken).toHaveCount(0);
    await addUserFilter.click();
    await expect(page.locator(".drawer-search-input")).toHaveValue("");
    await expect(editableFilter(page, "user").getByTestId("conversation-query-keyword")).toHaveText(
      "user:",
    );
    await expect(userPanel).toBeVisible();
    await page.locator(".drawer-title").click();
    await expect(userPanel).toHaveCount(0);
    await addUserFilter.click();
    await expect(userPanel).toBeVisible();
    await userPanel.getByRole("option", { name: /me@example\.com/ }).click();
    await expect(userToken).toHaveText(/user:me@example\.com/);
    await expect(page.locator('[data-conversation-id="other"]')).not.toBeVisible();
    await userToken.getByRole("button", { name: "Remove user:me@example.com filter" }).click();
    await searchToggle.click();
    await expect(page.locator(".drawer-search-input")).toHaveCount(0);
    await expect(searchToggle).not.toHaveClass(/search-toggle-active/);
    await searchToggle.click();
    await expect(userToken).toHaveCount(0);
  });

  test("tokenized search edits pills in place and wraps", async ({ page }) => {
    await page.setExtraHTTPHeaders({ "X-ExeDev-Email": "me@example.com" });
    const mine = conversation("mine", false, ["me@example.com", "other@example.com"]);
    const other = conversation("other", false, ["other@example.com"]);
    const alpine = conversation("alpine", false, ["third@example.com"]);
    mine.tags = JSON.stringify(["alpha"]);
    alpine.tags = JSON.stringify(["alpine"]);
    await stubConversationList(page, [mine, other, alpine]);

    await page.goto("/new");
    await page.getByRole("button", { name: "Search conversations..." }).click();
    await page.locator(".drawer-search-clear").click();

    const editor = page.getByTestId("conversation-query-editor");
    const activeInput = page.locator(".drawer-search-input");
    await activeInput.fill("test user:aaa@x.com tag:aaa big");
    await expect(page.getByTestId("user-filter-token")).toHaveText(/user:aaa@x\.com/);
    await expect(page.getByTestId("selected-tag-filter")).toHaveText(/tag:aaa/);
    await expect
      .poll(() =>
        editor.evaluate((element) =>
          Array.from(element.children).map((child) => {
            if (child instanceof HTMLInputElement) return `text:${child.value}`;
            const keyword = child.querySelector(".conversation-query-filter-keyword");
            const valueInput = child.querySelector<HTMLInputElement>(
              ".conversation-query-filter-value-input",
            );
            const value = child.querySelector(".conversation-query-filter-value");
            if (keyword)
              return `filter:${keyword.textContent?.trim()}${valueInput?.value ?? value?.textContent}`;
            return `pill:${child.querySelector(".drawer-filter-token-label")?.textContent}`;
          }),
        ),
      )
      .toEqual(["text:test", "filter:user:aaa@x.com", "text:", "filter:tag:aaa", "text:big"]);

    // Backspace next to a tag/user filter replaces only its value region with
    // an input. The keyword segment is the same DOM node through committed,
    // editing, and bare states.
    const tagToken = page.getByTestId("selected-tag-filter");
    const tagKeyword = tagToken.getByTestId("conversation-query-keyword");
    await expect(tagKeyword).toHaveText("tag:");
    await tagKeyword.evaluate((element) => {
      (window as Window & { __queryKeyword?: Element }).__queryKeyword = element;
    });
    await activeInput.evaluate((input: HTMLInputElement) => input.setSelectionRange(0, 0));
    await activeInput.press("Backspace");
    await expect(tagToken).toHaveCount(1);
    await expect(page.getByTestId("user-filter-token")).toHaveCount(1);
    const tagEdit = tagToken.getByTestId("conversation-query-value");
    await expect(tagEdit).toHaveValue("aaa");
    expect(
      await tagKeyword.evaluate(
        (element) => (window as Window & { __queryKeyword?: Element }).__queryKeyword === element,
      ),
    ).toBe(true);
    await expect
      .poll(() => tagEdit.evaluate((input: HTMLInputElement) => input.selectionStart))
      .toBe("aaa".length);
    await expect(activeInput).toHaveValue("big");
    await tagEdit.press("Backspace");
    await tagEdit.press("Backspace");
    await tagEdit.press("Backspace");
    await expect(tagEdit).toHaveValue("");
    await expect(tagKeyword).toHaveText("tag:");
    expect(
      await tagKeyword.evaluate(
        (element) => (window as Window & { __queryKeyword?: Element }).__queryKeyword === element,
      ),
    ).toBe(true);
    await tagEdit.press("Backspace");
    await expect(tagEdit).toHaveCount(0);
    await expect(tagToken).toHaveCount(0);
    await expect(activeInput).toHaveValue("big");

    // Delete is symmetric, placing the caret at the start. A bare modifier
    // is also removed in one keypress rather than one punctuation character
    // at a time.
    await page.locator(".drawer-search-clear").click();
    await activeInput.fill("left tag:aaa right");
    const firstText = page.getByTestId("conversation-query-text").first();
    await firstText.focus();
    await firstText.evaluate((input: HTMLInputElement) => {
      input.setSelectionRange(input.value.length, input.value.length);
    });
    await page.keyboard.press("Delete");
    await expect(page.getByTestId("selected-tag-filter")).toHaveCount(1);
    const forwardTagEdit = page
      .getByTestId("selected-tag-filter")
      .getByTestId("conversation-query-value");
    await expect(forwardTagEdit).toHaveValue("aaa");
    await expect
      .poll(() => forwardTagEdit.evaluate((input: HTMLInputElement) => input.selectionStart))
      .toBe(0);
    await forwardTagEdit.evaluate((input: HTMLInputElement) => {
      input.setSelectionRange(0, input.value.length);
    });
    await page.keyboard.press("Delete");
    await expect(forwardTagEdit).toHaveValue("");
    await page.keyboard.press("Delete");
    await expect(forwardTagEdit).toHaveCount(0);
    await expect(activeInput).toHaveValue("left right");

    // Autocomplete follows an unwrapped middle term and replaces that exact
    // range, preserving both surrounding text segments.
    await activeInput.fill("left tag:alpha right");
    await activeInput.evaluate((input: HTMLInputElement) => input.setSelectionRange(0, 0));
    await activeInput.press("Backspace");
    const middleTagEdit = page
      .getByTestId("selected-tag-filter")
      .getByTestId("conversation-query-value");
    await middleTagEdit.evaluate((input: HTMLInputElement) => {
      input.setSelectionRange(0, input.value.length);
    });
    await page.keyboard.type("alpi");
    const tagPanel = page.getByTestId("tag-filter-panel");
    await expect(tagPanel).toBeVisible();
    await expect(tagPanel.locator('[data-tag="alpine"]')).toBeVisible();
    await expect(tagPanel.locator('[data-tag="alpha"]')).toHaveCount(0);
    await tagPanel.locator('[data-tag="alpine"]').click();
    await expect(middleTagEdit).toHaveCount(0);
    await expect
      .poll(() =>
        editor.evaluate((element) =>
          Array.from(element.children).map((child) => {
            if (child instanceof HTMLInputElement) return `text:${child.value}`;
            const keyword = child.querySelector(".conversation-query-filter-keyword");
            const value = child.querySelector(".conversation-query-filter-value");
            if (keyword) return `filter:${keyword.textContent?.trim()}${value?.textContent}`;
            return `pill:${child.querySelector(".drawer-filter-token-label")?.textContent}`;
          }),
        ),
      )
      .toEqual(["text:left", "filter:tag:alpine", "text:right"]);

    // Moving focus away commits a still-valid edited term back to a pill.
    await activeInput.evaluate((input: HTMLInputElement) => input.setSelectionRange(0, 0));
    await activeInput.press("Backspace");
    const blurTagEdit = page
      .getByTestId("selected-tag-filter")
      .getByTestId("conversation-query-value");
    await blurTagEdit.press("Backspace");
    await page.getByTestId("conversation-query-text").first().focus();
    await expect(blurTagEdit).toHaveCount(0);
    await expect(page.getByTestId("selected-tag-filter")).toHaveText(/tag:alpin/);

    // A trailing action partial is split immediately, including after free
    // text. Repeated action clicks replace only the keyword, and bare
    // Backspace removes the whole modifier rather than one punctuation mark.
    await page.locator(".drawer-search-clear").click();
    const addUser = page.getByRole("button", { name: "Add user filter" });
    const addTag = page.getByRole("button", { name: "Add tag filter" });
    await activeInput.fill("find");
    await addUser.click();
    await addUser.click();
    await addTag.click();
    await addTag.click();
    const trailingTag = editableFilter(page, "tag");
    const trailingKeyword = trailingTag.getByTestId("conversation-query-keyword");
    await expect(activeInput).toHaveValue("");
    await expect(trailingKeyword).toHaveText("tag:");
    await expect(activeInput).toHaveAttribute("autocapitalize", "none");
    await expect(page.getByTestId("conversation-query-text").first()).toHaveValue("find");
    await expect(addUser).toHaveText("@ user");
    await expect(addTag).toHaveText("# tag");
    await activeInput.press("Backspace");
    await expect(trailingTag).toHaveCount(0);
    await expect(activeInput).toHaveValue("find");
    await expect(page.getByTestId("tag-filter-panel")).toHaveCount(0);

    // Typing edits only the value region, keeps the keyword unchanged, and
    // autocomplete commits the partial in place after the preceding text.
    await addTag.click();
    await trailingKeyword.evaluate((element) => {
      (window as Window & { __trailingKeyword?: Element }).__trailingKeyword = element;
    });
    await activeInput.pressSequentially("alpi");
    await expect(activeInput).toHaveValue("alpi");
    expect(
      await trailingKeyword.evaluate(
        (element) =>
          (window as Window & { __trailingKeyword?: Element }).__trailingKeyword === element,
      ),
    ).toBe(true);
    const trailingTagPanel = page.getByTestId("tag-filter-panel");
    await expect(trailingTagPanel.locator('[data-tag="alpine"]')).toBeVisible();
    await trailingTagPanel.locator('[data-tag="alpine"]').click();
    await expect(activeInput).toHaveValue("");
    await expect(page.getByTestId("selected-tag-filter")).toHaveText(/tag:alpine/);
    await expect(page.getByTestId("conversation-query-text").first()).toHaveValue("find");

    // Narrow fields wrap downward. Neither the bordered editor nor the drawer
    // gains horizontal overflow, and the action buttons remain underneath.
    await page.locator(".drawer").evaluate((element: HTMLElement) => {
      element.style.width = "250px";
    });
    await activeInput.fill(
      "tag:one tag:two user:a@example.com tag:three user:b@example.com tag:four tail",
    );
    const layout = await page.evaluate(() => {
      const shell = document.querySelector<HTMLElement>(".drawer-search-shell");
      const editorElement = document.querySelector<HTMLElement>(".conversation-query-editor");
      const actions = document.querySelector<HTMLElement>(".drawer-filter-actions");
      const drawer = document.querySelector<HTMLElement>(".drawer");
      if (!shell || !editorElement || !actions || !drawer) throw new Error("search UI missing");
      return {
        shellHeight: shell.getBoundingClientRect().height,
        shellOverflow: shell.scrollWidth - shell.clientWidth,
        editorOverflow: editorElement.scrollWidth - editorElement.clientWidth,
        drawerOverflow: drawer.scrollWidth - drawer.clientWidth,
        actionsTop: actions.getBoundingClientRect().top,
        shellBottom: shell.getBoundingClientRect().bottom,
      };
    });
    expect(layout.shellHeight).toBeGreaterThan(34);
    expect(layout.shellOverflow).toBeLessThanOrEqual(0);
    expect(layout.editorOverflow).toBeLessThanOrEqual(0);
    expect(layout.drawerOverflow).toBeLessThanOrEqual(0);
    expect(layout.actionsTop).toBeGreaterThanOrEqual(layout.shellBottom);
  });

  test("starts expanded even when the only item is a draft", async ({ page }) => {
    await stubConversationList(page, [conversation("draft", true)]);

    await page.goto("/new");

    await expect(page.locator(".drawer")).not.toHaveClass(/collapsed/);
    await expect(page.getByRole("button", { name: "Collapse sidebar" })).toBeVisible();
  });

  test("starts expanded for multiple conversations with aligned app-bar titles", async ({
    page,
  }) => {
    await stubConversationList(page, [conversation("first"), conversation("second")]);

    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await expect(drawer).not.toHaveClass(/collapsed/);
    await expect(page.locator(".drawer-title")).toBeVisible();

    const metrics = await page.evaluate(() => {
      function inspect(selector: string) {
        const element = document.querySelector<HTMLElement>(selector);
        if (!element) throw new Error(`Missing ${selector}`);
        const rect = element.getBoundingClientRect();
        const style = getComputedStyle(element);
        return {
          top: rect.top,
          bottom: rect.bottom,
          fontFamily: style.fontFamily,
          fontSize: style.fontSize,
          fontWeight: style.fontWeight,
          lineHeight: style.lineHeight,
          letterSpacing: style.letterSpacing,
          margin: style.margin,
        };
      }
      return {
        drawerHeader: inspect(".drawer-header"),
        chatHeader: inspect(".header"),
        drawerTitle: inspect(".drawer-title"),
        chatTitle: inspect(".header-title"),
      };
    });

    expect(metrics.drawerHeader.top).toBe(metrics.chatHeader.top);
    expect(metrics.drawerHeader.bottom).toBe(metrics.chatHeader.bottom);
    expect(metrics.drawerTitle.top).toBe(metrics.chatTitle.top);
    expect(metrics.drawerTitle.bottom).toBe(metrics.chatTitle.bottom);
    expect(metrics.drawerTitle.fontFamily).toBe(metrics.chatTitle.fontFamily);
    expect(metrics.drawerTitle.fontSize).toBe(metrics.chatTitle.fontSize);
    expect(metrics.drawerTitle.fontWeight).toBe(metrics.chatTitle.fontWeight);
    expect(metrics.drawerTitle.lineHeight).toBe(metrics.chatTitle.lineHeight);
    expect(metrics.drawerTitle.letterSpacing).toBe(metrics.chatTitle.letterSpacing);
    expect(metrics.drawerTitle.margin).toBe("0px");
    expect(metrics.chatTitle.margin).toBe("0px");
  });

  test("manual collapse survives reload with a single conversation", async ({ page }) => {
    await stubConversationList(page, [conversation("only")]);

    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await expect(drawer).not.toHaveClass(/collapsed/);
    await page.getByRole("button", { name: "Collapse sidebar" }).click();
    await expect(drawer).toHaveClass(/collapsed/);

    await page.reload();

    await expect(drawer).toHaveClass(/collapsed/);
    await page.getByRole("button", { name: "Expand sidebar" }).click();
    await expect(drawer).not.toHaveClass(/collapsed/);

    await page.reload();

    await expect(drawer).not.toHaveClass(/collapsed/);
  });

  test("manual collapse survives reload with many conversations", async ({ page }) => {
    await stubConversationList(page, [conversation("first"), conversation("second")]);

    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await expect(drawer).not.toHaveClass(/collapsed/);
    await page.getByRole("button", { name: "Collapse sidebar" }).click();
    await expect(drawer).toHaveClass(/collapsed/);

    await page.reload();

    await expect(drawer).toHaveClass(/collapsed/);
  });
});

async function swipe(
  page: Page,
  selector: string,
  start: { x: number; y: number },
  end: { x: number; y: number },
) {
  await page.evaluate(
    ({ selector, start, end }) => {
      const target = document.querySelector(selector);
      if (!(target instanceof Element)) throw new Error(`Missing ${selector}`);

      const makeTouch = ({ x, y }: { x: number; y: number }) =>
        new Touch({
          identifier: 1,
          target,
          clientX: x,
          clientY: y,
          screenX: x,
          screenY: y,
          pageX: x,
          pageY: y,
        });
      const startTouch = makeTouch(start);
      const endTouch = makeTouch(end);

      target.dispatchEvent(
        new TouchEvent("touchstart", {
          bubbles: true,
          cancelable: true,
          touches: [startTouch],
          targetTouches: [startTouch],
          changedTouches: [startTouch],
        }),
      );
      target.dispatchEvent(
        new TouchEvent("touchmove", {
          bubbles: true,
          cancelable: true,
          touches: [endTouch],
          targetTouches: [endTouch],
          changedTouches: [endTouch],
        }),
      );
      target.dispatchEvent(
        new TouchEvent("touchend", {
          bubbles: true,
          cancelable: true,
          touches: [],
          targetTouches: [],
          changedTouches: [endTouch],
        }),
      );
    },
    { selector, start, end },
  );
}

async function swipePatchDiff(page: Page) {
  await page.evaluate(() => {
    const mainContent = document.querySelector(".main-content");
    if (!(mainContent instanceof HTMLElement)) throw new Error("Missing main content");

    const diffsContainer = document.createElement("diffs-container");
    const shadowRoot = diffsContainer.shadowRoot ?? diffsContainer.attachShadow({ mode: "open" });
    const scrollContainer = document.createElement("div");
    const diffLine = document.createElement("span");
    diffsContainer.style.cssText = "display:block;width:120px";
    scrollContainer.style.cssText = "overflow-x:auto;width:120px";
    diffLine.style.cssText = "display:block;width:320px";
    scrollContainer.append(diffLine);
    shadowRoot.append(scrollContainer);
    mainContent.append(diffsContainer);
    Object.defineProperties(scrollContainer, {
      clientWidth: { value: 120 },
      scrollWidth: { value: 320 },
    });

    if (scrollContainer.scrollWidth <= scrollContainer.clientWidth) {
      throw new Error("Patch diff test fixture is not horizontally scrollable");
    }

    const makeTouch = (x: number, y: number) =>
      new Touch({
        identifier: 1,
        target: diffLine,
        clientX: x,
        clientY: y,
        screenX: x,
        screenY: y,
        pageX: x,
        pageY: y,
      });
    const startTouch = makeTouch(160, 300);
    const endTouch = makeTouch(220, 304);

    diffLine.dispatchEvent(
      new TouchEvent("touchstart", {
        bubbles: true,
        cancelable: true,
        composed: true,
        touches: [startTouch],
        targetTouches: [startTouch],
        changedTouches: [startTouch],
      }),
    );
    diffLine.dispatchEvent(
      new TouchEvent("touchmove", {
        bubbles: true,
        cancelable: true,
        composed: true,
        touches: [endTouch],
        targetTouches: [endTouch],
        changedTouches: [endTouch],
      }),
    );
    diffLine.dispatchEvent(
      new TouchEvent("touchend", {
        bubbles: true,
        cancelable: true,
        composed: true,
        touches: [],
        targetTouches: [],
        changedTouches: [endTouch],
      }),
    );
  });
}

test.describe("mobile drawer swipe", () => {
  test.use({
    viewport: { width: 393, height: 851 },
    isMobile: true,
    hasTouch: true,
  });

  test("opens and closes with opposite page swipes", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    const drawer = page.locator(".drawer");
    const input = page.getByTestId("message-input");
    await expect(drawer).not.toHaveClass(/open/);
    await input.focus();
    await expect(input).toBeFocused();

    await swipe(page, ".main-content", { x: 180, y: 300 }, { x: 256, y: 304 });
    await expect(drawer).toHaveClass(/open/);
    await expect(input).not.toBeFocused();

    await swipe(page, ".backdrop", { x: 380, y: 300 }, { x: 328, y: 304 });
    await expect(drawer).not.toHaveClass(/open/);
  });

  test("keeps structured filter values lowercase-ready", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    await swipe(page, ".main-content", { x: 180, y: 300 }, { x: 256, y: 304 });
    await page.getByRole("button", { name: "Search conversations..." }).click();
    await page.getByRole("button", { name: "Add tag filter" }).click();

    const tagFilter = editableFilter(page, "tag");
    await expect(tagFilter.getByTestId("conversation-query-keyword")).toHaveText("tag:");
    await expect(tagFilter.getByTestId("conversation-query-value")).toHaveAttribute(
      "autocapitalize",
      "none",
    );
  });

  test("leaves the system edge, short movement, and vertical scrolling alone", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    const drawer = page.locator(".drawer");
    await swipe(page, ".main-content", { x: 12, y: 300 }, { x: 112, y: 304 });
    await expect(drawer).not.toHaveClass(/open/);

    await swipe(page, ".main-content", { x: 160, y: 300 }, { x: 224, y: 304 });
    await expect(drawer).not.toHaveClass(/open/);

    await swipe(page, ".main-content", { x: 64, y: 200 }, { x: 92, y: 300 });
    await expect(drawer).not.toHaveClass(/open/);
  });

  test("leaves horizontal patch diff scrolling alone", async ({ page }) => {
    await stubConversationList(page, [conversation("first")]);
    await page.goto("/new");

    await swipePatchDiff(page);

    await expect(page.locator(".drawer")).not.toHaveClass(/open/);
  });

  test("does not open while a diff viewer modal is shown", async ({ page }) => {
    const first = conversation("first");
    first.cwd = "/tmp";
    await stubConversationList(page, [first]);
    await page.goto("/new");

    await page.keyboard.press("Control+Shift+D");
    await expect(page.locator(".diff-viewer-overlay")).toBeVisible();

    await swipe(page, ".diff-viewer-header", { x: 180, y: 100 }, { x: 256, y: 104 });

    await expect(page.locator(".drawer")).not.toHaveClass(/open/);
  });
});
