import { expect, test, type Page } from "@playwright/test";
import { ShellPage } from "./shell.page";

const ORDER = "0195f0a0-0000-7000-8000-0000000000aa";
const JOB = "0195f0a0-0000-7000-8000-0000000000a1";
const WORKER = "0195f0a0-0000-7000-8000-000000000001";
const PARTY = "0195f0a0-0000-7000-8000-000000000020";
const RESOURCE = "0195f0a0-0000-7000-8000-000000000010";

/**
 * AC1 of #25: a worker clocks in, starts and stops a job on the kiosk, and the
 * hours show as actual labor on the production order (2.00 h × worker 26.00).
 * Playwright here is Vite-only; the APIs are mocked. The Go store test posts
 * the job-card at the worker rate.
 */
test("kiosk clock-in and job start/stop lands labor cost on the production order", async ({ page }) => {
  let stopped = false;
  await mockShopfloor(page, () => {
    stopped = true;
  });
  await mockProduction(page, () => stopped);

  const shell = new ShellPage(page);
  await shell.goto("/shopfloor/kiosk");
  await expect(shell.title).toHaveText("Kiosk");

  await page.getByLabel("Badge").fill("B-42");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Ana Silva" })).toBeVisible();

  await page.getByRole("button", { name: "Clock in" }).click();
  await expect(page.getByRole("status").filter({ hasText: /^Clocked in at / })).toBeVisible();
  await expect(page.getByRole("button", { name: "Clock out" })).toBeVisible();

  await page.getByLabel(`Select ${ORDER} 20`).check();
  await page.getByRole("button", { name: "Start job" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Job started" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Stop job" })).toBeVisible();

  await page.getByRole("button", { name: "Stop job" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Work stopped" })).toBeVisible();
  expect(stopped).toBe(true);

  await shell.goto(`/production/${ORDER}`);
  await expect(shell.title).toHaveText("Production order");
  const actual = page.getByRole("list", { name: "Actual cost by group" });
  await expect(actual).toContainText("Actual labor 52.00 USD");
});

async function mockShopfloor(page: Page, onStop: () => void) {
  await page.route("**/api/v1/shopfloor/**", async (route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname;
    if (path.endsWith("/sessions:badge") && method === "POST") {
      return route.fulfill({ json: session() });
    }
    if (path.endsWith("/jobs") && method === "GET") {
      return route.fulfill({ json: { items: [job()] } });
    }
    if (path.endsWith("/registrations") && method === "POST") {
      const body = route.request().postDataJSON() as { items?: Array<{ id: string; kind: string }> };
      const items = Array.isArray(body.items) ? body.items : [];
      if (items.some((item) => item.kind === "job-stop")) onStop();
      return route.fulfill({
        json: {
          items: items.map((item) => ({ id: item.id, status: "accepted", flags: [], autoStopped: [], warnings: [] })),
        },
      });
    }
    return route.fulfill({ status: 404, json: { code: "TEST-404", detail: `${method} ${path}` } });
  });
}

async function mockProduction(page: Page, stopped: () => boolean) {
  await page.route("**/api/v1/production/**", async (route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname;
    if (method !== "GET") {
      return route.fulfill({ status: 404, json: { code: "TEST-404", detail: `${method} ${path}` } });
    }
    if (path.endsWith("/costs")) {
      return route.fulfill({ json: costs(stopped()) });
    }
    if (path.endsWith("/status-log")) {
      return route.fulfill({ json: { orderId: ORDER, items: [] } });
    }
    if (path.endsWith("/journals")) {
      return route.fulfill({ json: { orderId: ORDER, items: [] } });
    }
    if (path.endsWith("/jobs")) {
      return route.fulfill({ json: { orderId: ORDER, items: [], plannedHours: "0" } });
    }
    if (path.endsWith("/as-built") || path.endsWith("/trigger-points") || path.endsWith("/bom-edits")) {
      return route.fulfill({ json: { items: [] } });
    }
    if (path.endsWith("/orders") || url.searchParams.has("reworkOf")) {
      return route.fulfill({ json: { items: [] } });
    }
    if (path.endsWith(`/orders/${ORDER}`)) {
      return route.fulfill({ json: order() });
    }
    return route.fulfill({ status: 404, json: { code: "TEST-404", detail: `${method} ${path}` } });
  });
}

function session() {
  return {
    workerId: WORKER,
    partyId: PARTY,
    name: "Ana Silva",
    userId: null,
    clockedInAt: null,
    defaultResourceId: RESOURCE,
    openJobs: [],
    idleTimeoutMinutes: 10,
  };
}

function job() {
  return {
    id: JOB,
    orderRef: ORDER,
    operationRef: "20",
    resourceId: RESOURCE,
    status: "released",
    plannedQuantity: "100.0000",
    goodQuantity: "0.0000",
    scrapQuantity: "0.0000",
    sequenceStart: "2026-10-21T08:00:00Z",
    checks: [],
    results: [],
    finishedAt: null,
    version: 1,
    updatedAt: "2026-10-21T07:00:00Z",
  };
}

function order() {
  return {
    id: ORDER,
    legalEntityId: "0195d6a0-0000-7000-8000-0000000000e1",
    orderNumber: "PO-000001",
    productId: "0195d6a0-0000-7000-8000-0000000000f1",
    siteId: "0195d6a0-0000-7000-8000-0000000000a1",
    warehouseId: "0195d6a0-0000-7000-8000-0000000000b1",
    currency: "USD",
    quantity: "100",
    qtyRaf: "0",
    qtyError: "0",
    status: "Started",
    deliveryDate: "2026-11-30",
    originType: "Manual",
    version: 5,
    lines: [],
    operations: [
      {
        operNumber: 20,
        resourceId: "WC-WELD",
        setupTimeH: "0",
        runTimeHPer: "0.5",
        scrapPct: "0",
        laborRate: "30",
        overheadRate: "0",
        subcontract: false,
        subcontractPrice: "0",
        processQty: "100",
      },
    ],
    costs: [],
    createdAt: "2026-10-26T12:00:00Z",
    updatedAt: "2026-10-26T12:00:00Z",
  };
}

function costs(stopped: boolean) {
  return {
    orderId: ORDER,
    currency: "USD",
    totals: { labor: "1500.00" },
    total: "1500.00",
    rows: [{ group: "labor", key: "WC-WELD/20", qty: "50", amount: "1500", fixed: "0" }],
    actualTotals: stopped ? { labor: "52.00" } : {},
    actualTotal: stopped ? "52.00" : "0",
    actualRows: [],
    wipBalance: stopped ? "52.00" : "0",
    variances: [],
    varianceTotal: "0.00",
  };
}
