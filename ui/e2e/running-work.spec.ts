import { test, expect, type APIRequestContext } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

// These jobs and subagents sleep for minutes; stop them so a failed run does
// not leave them on the shared server.
async function stopWork(request: APIRequestContext, conversationIds: string[]) {
  for (const id of conversationIds) {
    const subagents = await (await request.get(`/api/conversation/${id}/subagents`)).json();
    for (const { conversation_id } of subagents ?? []) {
      await request.post(`/api/conversation/${conversation_id}/cancel`);
    }
    const jobs = await (await request.get(`/api/conversation/${id}/background-jobs`)).json();
    for (const { job_id } of jobs ?? []) {
      await request.post(`/api/conversation/${id}/background-jobs/${job_id}/kill`);
    }
  }
}

test.describe("running work", () => {
  test("keeps mobile work visible and retains finished jobs while others run", async ({
    page,
    request,
  }) => {
    await page.setViewportSize({ width: 390, height: 500 });
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "bash-bg: echo ready; sleep 600",
    );
    try {
      expect(
        (
          await request.post(`/api/conversation/${conversationId}/chat`, {
            data: { message: "bash-bg: sleep 601", model: "predictable" },
          })
        ).ok(),
      ).toBe(true);
      await page.goto(`/c/${slug}`);
      await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });

      const strip = page.getByLabel("Running work", { exact: true });
      const toggle = strip.getByTestId("running-work-toggle");
      await expect(toggle).toContainText("2 tasks", { timeout: 15000 });
      await expect(toggle).not.toContainText("Running:");
      expect(
        await strip.evaluate((element) =>
          element.nextElementSibling?.classList.contains("messages-bottom-sentinel"),
        ),
      ).toBe(true);

      const scroller = page.locator(".messages-container");
      await scroller.evaluate((element) => {
        element.scrollTop = element.scrollHeight;
      });
      await expect
        .poll(() =>
          scroller.evaluate(
            (element) => element.scrollHeight - element.clientHeight - element.scrollTop,
          ),
        )
        .toBeLessThanOrEqual(2);
      await toggle.evaluate((element) => {
        element.dispatchEvent(new Event("touchstart", { bubbles: true }));
        (element as HTMLElement).click();
      });
      const items = strip.getByTestId("running-work-items");
      const jobs = items.getByTestId("background-job");
      await expect(jobs).toHaveCount(2);
      await expect(jobs.filter({ hasText: "echo ready" })).toBeVisible();
      await expect(jobs.filter({ hasText: "sleep 601" })).toBeVisible();
      await expect
        .poll(() =>
          strip.evaluate((element) => {
            const container = element.closest(".messages-container")!.getBoundingClientRect();
            const panel = element.getBoundingClientRect();
            return panel.top >= container.top && panel.bottom <= container.bottom;
          }),
        )
        .toBe(true);

      await toggle.click();
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await scroller.evaluate((element) => {
        element.dispatchEvent(new WheelEvent("wheel", { deltaY: -20, bubbles: true }));
        element.scrollTop -= 20;
      });
      const readingPosition = await scroller.evaluate((element) => element.scrollTop);
      await toggle.evaluate((element) => (element as HTMLElement).click());
      await expect(items).toBeVisible();
      await expect
        .poll(async () =>
          Math.abs((await scroller.evaluate((element) => element.scrollTop)) - readingPosition),
        )
        .toBeLessThanOrEqual(2);

      const echo = jobs.filter({ hasText: "echo ready" });
      await echo.locator("summary").click();
      await expect(echo.getByTestId("background-job-tail")).toContainText("ready");
      const liveJobs = await (
        await request.get(`/api/conversation/${conversationId}/background-jobs`)
      ).json();
      const job = liveJobs.find((job: { command: string }) => job.command.includes("echo ready"));
      expect(
        (
          await request.post(
            `/api/conversation/${conversationId}/background-jobs/${job.job_id}/kill`,
          )
        ).ok(),
      ).toBe(true);
      await expect(toggle).toContainText("1 task", { timeout: 15000 });
      await expect(echo.locator(".running-command-time")).toHaveText("Finished");
      await expect(echo.getByTestId("background-job-kill")).toHaveCount(0);
      await expect(echo.getByTestId("background-job-tail")).toContainText("ready");
      await expect(
        jobs.filter({ hasText: "sleep 601" }).locator(".running-command-time"),
      ).toContainText("Running");
    } finally {
      await stopWork(request, [conversationId]);
    }
  });

  test("keeps a finished subagent row and opens its conversation", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "hello there",
    );
    try {
      await page.goto(`/c/${slug}`);
      const input = page.getByTestId("message-input");
      await expect(input).toBeVisible({ timeout: 30000 });

      await input.fill("subagent: statushelper bash: sleep 120");
      await page.getByTestId("send-button").click();

      const strip = page.getByLabel("Running work", { exact: true });
      await expect(strip.getByTestId("running-work-toggle")).toContainText("1 subagent", {
        timeout: 30000,
      });
      await strip.getByTestId("running-work-toggle").click();
      const row = strip.locator(".running-work-agent");
      await expect(row).toContainText("statushelper");
      await expect(row).toContainText("sleep", { timeout: 30000 });
      const [subagent] = await (
        await request.get(`/api/conversation/${conversationId}/subagents`)
      ).json();
      expect(
        (await request.post(`/api/conversation/${subagent.conversation_id}/cancel`)).ok(),
      ).toBe(true);
      await expect(row.locator(".running-work-agent-time")).toHaveText("Finished", {
        timeout: 15000,
      });
      await expect(strip.getByTestId("running-work-toggle")).toHaveAttribute(
        "aria-expanded",
        "true",
      );
      await row.click();
      await expect(page).toHaveURL(/\/c\/statushelper/, { timeout: 10000 });
    } finally {
      await stopWork(request, [conversationId]);
    }
  });

  test("discards finished subagent rows when the panel closes", async ({ page, request }) => {
    const { conversationId, slug } = await createConversationViaAPIWithDetails(
      request,
      "hello there",
    );
    try {
      await page.goto(`/c/${slug}`);
      const input = page.getByTestId("message-input");
      await expect(input).toBeVisible({ timeout: 30000 });
      await input.fill("subagent: finishhelper bash: sleep 120");
      await page.getByTestId("send-button").click();
      const strip = page.getByLabel("Running work", { exact: true });
      const toggle = strip.getByTestId("running-work-toggle");
      await expect(toggle).toContainText("1 subagent", { timeout: 30000 });
      await toggle.click();
      const row = strip.locator(".running-work-agent");
      await expect(row).toContainText("finishhelper");
      const [subagent] = await (
        await request.get(`/api/conversation/${conversationId}/subagents`)
      ).json();
      expect(
        (await request.post(`/api/conversation/${subagent.conversation_id}/cancel`)).ok(),
      ).toBe(true);
      await expect(row.locator(".running-work-agent-time")).toHaveText("Finished", {
        timeout: 15000,
      });
      await toggle.click();
      await expect(strip).toHaveCount(0);
    } finally {
      await stopWork(request, [conversationId]);
    }
  });
});
