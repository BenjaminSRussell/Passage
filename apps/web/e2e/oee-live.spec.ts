import { expect, test } from "@playwright/test";
import { ShellPage } from "./shell.page";

const RESOURCE = "0195f0a0-0000-7000-8000-000000000010";

function bucket(good: number, total: number, oee: string) {
  return {
    resourceId: RESOURCE, bucketStart: "2026-10-21T06:00:00Z", plannedMinutes: "10", runMinutes: "10", downMinutes: "0",
    totalCount: total, goodCount: good, availability: "1", performance: "0.8", quality: "0.8", oee, overRate: false, sourceEvents: 2,
  };
}

function frame(good: number, total: number, oee: string, at: string): string {
  const tile = { resourceId: RESOURCE, bucketStart: "2026-10-21T06:00:00Z", bucketEnd: "2026-10-21T06:15:00Z", computedAt: at, bucket: bucket(good, total, oee) };
  return `event: oee\ndata: ${JSON.stringify(tile)}\n\n`;
}

/**
 * AC-25.4: the live OEE tile of a machine follows the Server-Sent Events of GET /oee/stream.
 * The mocked stream ends after each frame (as the service ends a stream before its request
 * timeout) and the browser's EventSource reconnects after the retry hint, getting the next bucket.
 */
test("live OEE tile shows the pushed bucket and its update", async ({ page }) => {
  let opened = 0;
  await page.route("**/api/v1/shopfloor/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path.endsWith("/machines")) {
      return route.fulfill({
        json: { items: [{ id: "0195f0a0-0000-7000-8000-0000000000c1", machineId: "M-1", resourceId: RESOURCE, siteId: null, idealCycleSeconds: "60", idleThresholdSeconds: 300, createdAt: "2026-10-01T00:00:00Z" }] },
      });
    }
    if (path.endsWith("/oee/stream")) {
      opened += 1;
      expect(url.searchParams.get("resourceId")).toBe(RESOURCE);
      const body = opened === 1 ? `retry: 200\n\n${frame(4, 5, "0.64", "2026-10-21T06:10:00Z")}` : `retry: 200\n\n${frame(7, 8, "0.7", "2026-10-21T06:10:02Z")}`;
      return route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" }, body });
    }
    if (path.endsWith("/oee")) return route.fulfill({ json: { items: [] } });
    return route.fulfill({ status: 404, json: { code: "TEST-404", detail: path } });
  });

  const shell = new ShellPage(page);
  await shell.goto("/shopfloor/oee");
  await expect(shell.title).toHaveText("OEE");
  await page.getByLabel("Machine").selectOption({ label: "M-1" });

  const tile = page.getByRole("list", { name: "Current bucket" });
  await expect(page.getByRole("heading", { name: "Live OEE: M-1" })).toBeVisible();
  await expect(tile).toContainText("64.0 %");
  await expect(tile).toContainText("4 / 5");
  await expect(tile).toContainText("7 / 8", { timeout: 5_000 });
  await expect(tile).toContainText("70.0 %");
  expect(opened).toBeGreaterThanOrEqual(2);
});
