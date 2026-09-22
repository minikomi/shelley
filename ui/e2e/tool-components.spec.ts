import { test, expect, type Locator } from '@playwright/test';
import { createConversationViaAPI, mountAllToolCards } from './helpers';

test.describe('Tool Component Verification', () => {
  // Shared smorgasbord conversation (created once, reused by multiple tests).
  // The smorgasbord launches browser tools (chromedp) which need up to 60s
  // for the initial Chrome startup, so we only pay that cost once.
  let smorgasbordSlug: string;

  async function ensureSmorgasbord(request: any): Promise<string> {
    if (!smorgasbordSlug) {
      smorgasbordSlug = await createConversationViaAPI(request, 'tool smorgasbord', {
        agentTimeout: 150000
      });
    }
    return smorgasbordSlug;
  }

  test('all tools use custom components, not GenericTool', async ({ page, request }) => {
    test.setTimeout(180000);
    const registry = await request.get('/api/tools');
    expect(registry.ok()).toBeTruthy();
    const { tools } = await registry.json();
    expect(tools.map((tool: { name: string }) => tool.name)).not.toContain('keyword_search');

    const slug = await ensureSmorgasbord(request);

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // All tool results are already in the DB; wait for the UI to render them,
    // then mount the ones that are still cheap offscreen placeholders so the
    // whole set can be asserted without scrolling through it.
    await page.waitForFunction(
      () => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 15,
      undefined,
      { timeout: 30000 },
    );
    await mountAllToolCards(page);
    await expect(page.locator(".tool-status-icon")).toHaveCount(0);

    // The generic-shaped cards (.tool) are identified by their header summary;
    // each must render its specialized component's emoji. Assert on attachment
    // rather than visibility: the smorgasbord is taller than the viewport and
    // offscreen chunks are content-visibility: auto.
    const toolCard = (summary: string | RegExp): Locator =>
      page.locator('.tool').filter({ has: page.locator('.tool-command', { hasText: summary }) });
    const expectEmoji = async (card: Locator, emoji: string) => {
      await expect(card.first()).toBeAttached();
      await expect(card.first().locator('.tool-emoji')).toHaveText(emoji);
    };

    // bash and shell both render the BashTool card, with the command in the header.
    for (const command of ["echo 'hello from bash'", "echo 'hello from shell'"]) {
      const bashCard = page.locator('.bash-tool').filter({ hasText: command }).first();
      await expect(bashCard).toBeAttached();
      await expect(bashCard.locator('.bash-tool-emoji')).toBeAttached();
      await expect(bashCard.locator('.bash-tool-command')).toContainText(command);
    }

    // Thinking content appears inline (not a tool card).
    const thinkingContent = page.locator('.thinking-content').filter({ hasText: "I'm thinking about the best approach" });
    await expect(thinkingContent.first()).toBeVisible();
    await expect(thinkingContent.locator('text=💭').first()).toBeVisible();

    // Diff / image / iframe tools keep their own card shapes.
    const patchTool = page.locator('.patch-tool').first();
    await expect(patchTool).toBeAttached();
    await expect(patchTool.locator('.patch-tool-emoji')).toBeAttached();

    // A browser: screenshot action renders the ScreenshotTool card, same as the
    // standalone screenshot tool.
    const screenshotTool = page.locator('.screenshot-tool').filter({ hasText: /\.png$|screenshot/i }).first();
    await expect(screenshotTool).toBeAttached();
    await expect(page.locator('.screenshot-tool .screenshot-tool-emoji').filter({ hasText: '📷' }).first()).toBeAttached();

    const readImageTool = page.locator('.screenshot-tool').filter({ hasText: '/tmp/image.png' });
    await expect(readImageTool.first()).toBeAttached();
    await expect(readImageTool.locator('.screenshot-tool-emoji').filter({ hasText: '🖼️' }).first()).toBeAttached();

    // browser: screencast_stop -> ScreencastTool card.
    await expect(page.locator('.screencast-tool').first()).toBeAttached();

    // Spot-check the rest of the set.
    await expectEmoji(toolCard('https://example.com'), '🌐');
    await expectEmoji(toolCard('document.title'), '⚡');
    await expectEmoji(toolCard(/console/i), '📋');
    await expectEmoji(toolCard('/tmp/test-prompt.txt'), '🤖');
    // The emulate/network/accessibility/profile families are folded into the
    // single "browser" tool via `<family>_<sub>` actions, and the old
    // standalone browser_* tool names must still render with their specialized
    // components (for conversations in existing DBs). Both spellings reach the
    // same component, so the per-family emoji is what proves the dispatch.
    await expectEmoji(toolCard(/^device iphone_14$/), '📱');
    await expectEmoji(toolCard(/^device ipad$/), '📱');
    await expectEmoji(toolCard(/^enable$/), '📡');
    await expectEmoji(toolCard(/^tree$/), '🌳');
    await expectEmoji(toolCard(/^metrics$/), '📊');

    // No tool falls back to GenericTool's gear.
    await expect(page.locator('.tool-emoji').filter({ hasText: '⚙️' })).toHaveCount(0);
  });

  test('generic tool audit stays inside its card on mobile', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/new');
    await page.locator('body').evaluate((body) => {
      body.innerHTML = `
        <div style="padding: 16px">
          <details class="tool-result-details">
            <summary class="tool-result-summary">
              <div class="tool-result-meta">
                <div class="tool-result-primary flex items-center space-x-2">
                  <svg class="chat-tool-icon"></svg>
                  <span class="tool-result-name text-sm font-medium text-blue">
                    openai_audio_transcription
                  </span>
                  <span class="tool-result-status text-xs">
                    {"duration_ms":1731,"model":"gpt-transcribe"}...
                  </span>
                </div>
                <div class="tool-result-time"></div>
              </div>
            </summary>
          </details>
        </div>
      `;
    });

    const card = page.locator('.tool-result-details');
    const summary = page.locator('.tool-result-summary');
    const status = page.locator('.tool-result-status');
    await expect(card).toBeVisible();
    expect(await summary.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(
      true,
    );
    const statusBox = await status.boundingBox();
    const summaryBox = await summary.boundingBox();
    expect((statusBox?.x ?? 0) + (statusBox?.width ?? 0)).toBeLessThanOrEqual(
      (summaryBox?.x ?? 0) + (summaryBox?.width ?? 0),
    );
  });

  test('bash tool shows command in header', async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, 'bash: unique-test-command-xyz123');
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // The card renders inline and collapsed, with the command in its header.
    const bashToolWithOurCommand = page.locator('.bash-tool').filter({ hasText: 'unique-test-command-xyz123' });
    await expect(bashToolWithOurCommand).toBeVisible({ timeout: 15000 });
    const commandElement = bashToolWithOurCommand.locator('.bash-tool-command');
    await expect(commandElement).toBeVisible();
    const commandText = await commandElement.textContent();
    expect(commandText).toContain('unique-test-command-xyz123');
  });

  test('think tool shows thought prefix in header', async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, 'think: This is a long thought that should be truncated in the header display');
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // Wait for the thinking content to render
    await page.waitForFunction(() => document.body.textContent?.includes("I've considered my approach.") ?? false, undefined, { timeout: 15000 });

    // Verify thinking content shows the thought text with 💭 emoji
    const thinkingContent = page.locator('.thinking-content').filter({ hasText: 'This is a long thought' }).first();
    await expect(thinkingContent).toBeVisible();
    const thinkingText = await thinkingContent.textContent();
    expect(thinkingText).toContain('This is a long thought');
  });

  test('browser navigate tool shows URL in header', async ({ page, request }) => {
    test.setTimeout(180000);
    const slug = await ensureSmorgasbord(request);

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    await page.waitForFunction(
      () => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 15,
      undefined,
      { timeout: 30000 },
    );
    await mountAllToolCards(page);

    // The BrowserNavigateTool card shows the URL in its header; expanding it
    // shows the URL section (rendered as a link inside .tool-code).
    const navigateCard = page
      .locator('.tool')
      .filter({ has: page.locator('.tool-command', { hasText: 'https://example.com' }) })
      .first();
    await navigateCard.scrollIntoViewIfNeeded();
    await expect(navigateCard).toContainText('https://example.com');
    await navigateCard.locator('.tool-header').click();
    await expect(
      navigateCard.locator('.tool-code').filter({ hasText: 'https://example.com' }).first(),
    ).toBeVisible();
  });

  test('patch tool can be collapsed and expanded without errors', async ({ page, request }) => {
    const slug = await createConversationViaAPI(request, 'patch success');
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // Wait for successful patch tool
    const patchTool = page.locator('.patch-tool[data-testid="tool-call-completed"]').filter({ hasText: 'test-patch-success.txt' }).first();
    await expect(patchTool).toBeVisible({ timeout: 15000 });

    // Get console errors before toggling
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    const header = patchTool.locator('.patch-tool-header');

    // The toggle button should exist and respond to clicks
    const toggle = patchTool.locator('.patch-tool-toggle');
    await expect(toggle).toBeVisible();

    // Click to collapse
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeHidden();

    // Expand
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeVisible({ timeout: 10000 });

    // Collapse again
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeHidden();

    // Expand again
    await header.click();
    await expect(patchTool.locator('.patch-tool-details')).toBeVisible({ timeout: 10000 });

    // Check no Monaco model errors occurred
    const modelErrors = errors.filter((e) => e.includes('model') && e.includes('already exists'));
    expect(modelErrors).toHaveLength(0);
  });

  test('emoji sizes are consistent across all tools', async ({ page, request }) => {
    test.setTimeout(180000);
    const slug = await ensureSmorgasbord(request);

    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    await page.waitForFunction(
      () => document.querySelectorAll('[data-testid="tool-call-completed"]').length >= 15,
      undefined,
      { timeout: 30000 },
    );
    await mountAllToolCards(page);

    // Get every tool card's emoji and check its computed font-size.
    const emojiSizes = await page.$$eval('.tool-emoji, .bash-tool-emoji, .patch-tool-emoji, .screenshot-tool-emoji', (elements) => elements.map((el) => window.getComputedStyle(el).fontSize));

    // All emojis should be 1rem (16px by default)
    // Check that all sizes are the same
    const uniqueSizes = new Set(emojiSizes);
    expect(uniqueSizes.size).toBe(1);

    // Verify the size is 16px (1rem)
    expect(emojiSizes[0]).toBe('16px');
  });
});
