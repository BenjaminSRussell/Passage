import { expect, test, type Page } from "@playwright/test";
import { ShellPage } from "./shell.page";

const WORKER = "0195f0a0-0000-7000-8000-000000000001";
const PARTY = "0195f0a0-0000-7000-8000-000000000020";
const RESOURCE = "0195f0a0-0000-7000-8000-000000000010";
const ORDER = "0195f0a0-0000-7000-8000-0000000000aa";
const JOB = "0195f0a0-0000-7000-8000-0000000000a1";
const PAIRS = 20;

type Item = { id: string; kind: string; offline?: boolean; startedAt: string };
type Batch = { deviceSentAt?: string; items: Item[] };

/**
 * AC3 of #25 (AC-25.5): the worker clocks in, the kiosk loses the network, the worker keeps going
 * (20 job start/stop pairs: 40 actions), and when the network is back the kiosk replays them
 * once, in the order recorded, marked offline and with its own clock. The APIs are mocked; the
 * server side of the replay is TestHTTP_Offline30MinReplayWithConflicts.
 */
test("kiosk queues 40 actions offline and replays them once, in order, when back online", async ({ page, context }) => {
  const batches: Batch[] = [];
  const net = { down: false };
  await mockShopfloor(page, batches, net);

  const shell = new ShellPage(page);
  await shell.goto("/shopfloor/kiosk");
  await page.getByLabel("Badge").fill("B-42");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Ana Silva" })).toBeVisible();
  await page.getByRole("button", { name: "Clock in" }).click();
  await expect(page.getByLabel(`Select ${ORDER} 20`)).toBeVisible();
  expect(batches).toHaveLength(1);
  batches.length = 0;

  // setOffline makes navigator.onLine false and fires online on the way back; route mocks
  // still answer while offline, so the mock also drops the requests the kiosk sends meanwhile.
  await context.setOffline(true);
  net.down = true;
  for (let i = 0; i < PAIRS; i++) {
    await page.getByLabel(`Select ${ORDER} 20`).check();
    await page.getByRole("button", { name: "Start job" }).click();
    await expect(page.getByRole("button", { name: "Stop job" })).toBeVisible();
    await page.getByRole("button", { name: "Stop job" }).click();
    await expect(page.getByRole("status").filter({ hasText: "Saved on this kiosk" })).toBeVisible();
  }
  await expect(page.getByText("40 actions waiting to sync")).toBeVisible();
  expect(batches).toHaveLength(0);

  net.down = false;
  await context.setOffline(false);
  await expect(page.getByText("Synced 40 offline actions.")).toBeVisible();
  expect(batches).toHaveLength(1);
  const items = batches[0].items;
  expect(items).toHaveLength(40);
  expect(new Set(items.map((it) => it.id)).size).toBe(40);
  expect(items.every((it) => it.offline === true)).toBe(true);
  expect(batches[0].deviceSentAt).toBeTruthy();
  expect(items.map((it) => it.kind)).toEqual(Array.from({ length: PAIRS }, () => ["job-start", "job-stop"]).flat());
  const times = items.map((it) => Date.parse(it.startedAt));
  expect(times.every((t, i) => i === 0 || t >= times[i - 1])).toBe(true);

  // A second sync sends nothing: every action was replayed exactly once.
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await page.waitForTimeout(200);
  expect(batches).toHaveLength(1);
});

async function mockShopfloor(page: Page, batches: Batch[], net: { down: boolean }) {
  await page.route("**/api/v1/shopfloor/**", async (route) => {
    if (net.down) return route.abort("internetdisconnected");
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname;
    if (path.endsWith("/sessions:badge") && method === "POST") {
      return route.fulfill({
        json: { workerId: WORKER, partyId: PARTY, name: "Ana Silva", userId: null, clockedInAt: null, defaultResourceId: RESOURCE, openJobs: [], idleTimeoutMinutes: 10 },
      });
    }
    if (path.endsWith("/jobs") && method === "GET") {
      return route.fulfill({
        json: {
          items: [
            {
              id: JOB, orderRef: ORDER, operationRef: "20", resourceId: RESOURCE, status: "released", plannedQuantity: "100.0000",
              goodQuantity: "0.0000", scrapQuantity: "0.0000", sequenceStart: "2026-10-21T08:00:00Z", checks: [], results: [],
              finishedAt: null, version: 1, updatedAt: "2026-10-21T07:00:00Z",
            },
          ],
        },
      });
    }
    if (path.endsWith("/registrations") && method === "POST") {
      const body = route.request().postDataJSON() as Batch;
      batches.push(body);
      return route.fulfill({ json: { items: body.items.map((it) => ({ id: it.id, status: "accepted", flags: [], autoStopped: [], warnings: [] })), clockCorrected: false } });
    }
    return route.fulfill({ status: 404, json: { code: "TEST-404", detail: `${method} ${path}` } });
  });
}
