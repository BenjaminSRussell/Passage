import { devices, expect, test, type Page } from "@playwright/test";

/** GS1-128 labels from the ASN fixture vectors used by the designer simulator. */
const SSCC = "]C100340123456789012340";
const GTIN = "]C10109501234567891";
const LOCATION = "A-01-02";
const LOT_LABEL = "]C1" + "01" + "09501234567891" + "10LOT42" + "\u001D" + "21SER";
const SHIP_FLOW = {
  code: "SHIP",
  entryStepId: "scan_container",
  steps: [
    { id: "scan_container", type: "scan", prompt: "Scan the container to load", expectedScan: ["LP"], next: { default: "confirm_ship" } },
    { id: "confirm_ship", type: "confirm", prompt: "Confirm the shipment", next: { default: null } },
  ],
};

interface Replayed {
  idempotencyKey: string;
  headerKey: string;
  command: string;
  payload: { flowCode: string; fields: Record<string, Record<string, string>> };
}

async function replaysFor(page: Page, deviceId: string): Promise<Replayed[]> {
  const res = await page.request.get("/api/v1/mobile/offline:replay");
  expect(res.ok()).toBe(true);
  return ((await res.json()) as Replayed[]).filter((r) => r.idempotencyKey.startsWith(`${deviceId}:`));
}

async function publishFromDesigner(designer: Page, flow: unknown): Promise<void> {
  await designer.goto("/mobile-flows");
  await expect(designer.getByRole("heading", { name: "Mobile flow designer" })).toBeVisible();
  await designer.getByLabel("Flow definition (JSON)").fill(JSON.stringify(flow, null, 2));
  await designer.getByRole("button", { name: "Publish" }).click();
  await expect(designer.getByText(/Published \w+ version \d+/)).toBeVisible({ timeout: 15_000 });
}

test.describe.configure({ mode: "serial" });

async function openDevice(page: Page, deviceId: string): Promise<void> {
  await page.addInitScript((id) => {
    localStorage.setItem("passage.deviceId", id);
    localStorage.setItem("passage.userId", "picker-1");
    localStorage.setItem("passage.menuId", "default");
  }, deviceId);
  await page.goto("/device");
  await expect(page.getByRole("navigation", { name: "Flows" })).toBeVisible({ timeout: 30_000 });
}

/** Hardware scan: Zebra DataWedge intent forwarded as `passage:scan` (FR-32.10). */
async function scan(page: Page, raw: string): Promise<void> {
  await page.evaluate((value) => {
    window.dispatchEvent(
      new CustomEvent("passage:scan", {
        detail: { source: "DATAWEDGE", extras: { "com.symbol.datawedge.data_string": value } },
      }),
    );
  }, raw);
}

async function enterValue(page: Page, value: string): Promise<void> {
  await page.getByLabel("Value").fill(value);
  await page.getByLabel("Value").press("Enter");
}

test.describe("AC1 inbound-to-outbound GS1-128 on an emulated device", () => {
  test("receive → pick → pack → ship with scanned GS1-128 labels, then Sync replays in order", async ({ page, browser }) => {
    const designerContext = await browser.newContext({ ...devices["Desktop Chrome"] });
    try {
      await publishFromDesigner(await designerContext.newPage(), SHIP_FLOW);
    } finally {
      await designerContext.close();
    }
    await openDevice(page, "e2e-ac1");

    await page.getByRole("button", { name: "receive putaway" }).click();
    await expect(page.getByText("Scan the license plate")).toBeVisible();
    await scan(page, SSCC);
    await expect(page.getByText("Scan the put-away location")).toBeVisible();
    await scan(page, LOCATION);
    await expect(page.getByText("Confirm put-away")).toBeVisible();
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect(page.getByRole("status")).toHaveText("Confirmation queued.");
    await page.getByRole("button", { name: "Menu" }).click();

    await page.getByRole("button", { name: "pick", exact: true }).click();
    await expect(page.getByText("Scan the pick location")).toBeVisible();
    await scan(page, LOCATION);
    await expect(page.getByText("Scan the item")).toBeVisible();
    await scan(page, GTIN);
    await expect(page.getByText("Quantity picked")).toBeVisible();
    await enterValue(page, "5");
    await expect(page.getByText("Was the full quantity picked?")).toBeVisible();
    await page.getByRole("button", { name: "full" }).click();
    await expect(page.getByText("Confirm pick")).toBeVisible();
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect(page.getByRole("status")).toHaveText("Confirmation queued.");
    await page.getByRole("button", { name: "Menu" }).click();

    await page.getByRole("button", { name: "pack" }).click();
    await expect(page.getByText("Scan the shipping container")).toBeVisible();
    await scan(page, SSCC);
    await expect(page.getByText("Scan the item to pack")).toBeVisible();
    await scan(page, GTIN);
    await expect(page.getByText("Quantity packed")).toBeVisible();
    await enterValue(page, "4");
    await expect(page.getByText("Confirm the pack")).toBeVisible();
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect(page.getByRole("status")).toHaveText("Confirmation queued.");
    await page.getByRole("button", { name: "Menu" }).click();

    await page.getByRole("button", { name: "ship" }).click();
    await expect(page.getByText("Scan the container to load")).toBeVisible();
    await scan(page, SSCC);
    await expect(page.getByText("Confirm the shipment")).toBeVisible();
    await page.getByRole("button", { name: "Confirm" }).click();
    await expect(page.getByRole("status")).toHaveText("Confirmation queued.");
    await expect(page.getByText("4 queued, 496 left")).toBeVisible();

    await page.getByRole("button", { name: "Sync 4 queued" }).click();
    await expect(page.getByRole("status")).toHaveText("Synced 4; 0 waiting (drained).");
    await expect(page.getByText("0 queued, 500 left")).toBeVisible();

    const replayed = await replaysFor(page, "e2e-ac1");
    expect(replayed.map((r) => r.idempotencyKey)).toEqual(["e2e-ac1:1", "e2e-ac1:2", "e2e-ac1:3", "e2e-ac1:4"]);
    expect(replayed.map((r) => r.headerKey)).toEqual(replayed.map((r) => r.idempotencyKey));
    expect(replayed.map((r) => r.command)).toEqual(Array(4).fill("ConfirmWorkLine"));
    expect(replayed.map((r) => r.payload.flowCode)).toEqual(["RECEIVE_PUTAWAY", "PICK", "PACK", "SHIP"]);
  });
});

test.describe("AC4 flow designer publish reaches the device", () => {
  test("a lot-confirmation step runs on the device without an app update", async ({ browser }) => {
    const deviceContext = await browser.newContext({ ...devices["Pixel 5"] });
    const designerContext = await browser.newContext({ ...devices["Desktop Chrome"] });
    const device = await deviceContext.newPage();
    const designer = await designerContext.newPage();
    try {
      await openDevice(device, "e2e-ac4");
      await device.getByRole("button", { name: "receive putaway" }).click();
      await expect(device.getByText("Scan the license plate")).toBeVisible();
      await expect(device.getByText("Scan the lot")).toHaveCount(0);
      await device.getByRole("button", { name: "Menu" }).click();

      await designer.goto("/mobile-flows");
      await expect(designer.getByRole("heading", { name: "Mobile flow designer" })).toBeVisible();
      const editor = designer.getByLabel("Flow definition (JSON)");
      const current = JSON.parse(await editor.inputValue()) as {
        code: string;
        entryStepId: string;
        steps: Array<{ id: string; type: string; prompt: string; expectedScan?: string[]; next: Record<string, string | null> }>;
      };
      const location = current.steps.find((s) => s.id === "scan_location");
      if (!location) throw new Error("receive/put-away is missing scan_location");
      location.next = { default: "confirm_lot" };
      const confirmAt = current.steps.findIndex((s) => s.id === "confirm_putaway");
      current.steps.splice(confirmAt, 0, {
        id: "confirm_lot",
        type: "scan",
        prompt: "Scan the lot",
        expectedScan: ["LOT"],
        next: { default: "confirm_putaway" },
      });
      await editor.fill(JSON.stringify(current, null, 2));
      const menuReload = device.waitForResponse((r) => r.url().includes("/api/v1/mobile/menus/") && r.ok());
      await designer.getByRole("button", { name: "Publish" }).click();
      await expect(designer.getByText(/Published RECEIVE_PUTAWAY version 2/)).toBeVisible({ timeout: 15_000 });
      await menuReload;

      await device.getByRole("button", { name: "receive putaway" }).click();
      await expect(device.getByText("Scan the license plate")).toBeVisible();
      await scan(device, SSCC);
      await expect(device.getByText("Scan the put-away location")).toBeVisible();
      await scan(device, LOCATION);
      await expect(device.getByText("Scan the lot")).toBeVisible({ timeout: 60_000 });
      await scan(device, LOT_LABEL);
      await expect(device.getByText("Confirm put-away")).toBeVisible();
      await device.getByRole("button", { name: "Confirm" }).click();
      await expect(device.getByRole("status")).toHaveText("Confirmation queued.");
      await device.getByRole("button", { name: "Sync 1 queued" }).click();
      await expect(device.getByRole("status")).toHaveText("Synced 1; 0 waiting (drained).");
      const [lotTx] = await replaysFor(device, "e2e-ac4");
      expect(lotTx.payload.flowCode).toBe("RECEIVE_PUTAWAY");
      expect(lotTx.payload.fields.confirm_lot).toMatchObject({ "01": "09501234567891", "10": "LOT42", "21": "SER" });
    } finally {
      await deviceContext.close();
      await designerContext.close();
    }
  });
});
