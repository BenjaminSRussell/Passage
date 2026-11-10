import { expect, test, type Page } from "@playwright/test";

/**
 * Pseudo-locale pass (#509). With ?lang=en-XA every translated shell string is accented, padded by about a third and
 * wrapped in brackets. The pass checks that the shell uses the translated strings (a hard-coded English control
 * would not be bracketed), that the longer text does not push the page sideways, and that the P2P and O2C pages
 * still render and work with the longer text. It runs against the demo build.
 */

const PAGES = [
  "/",
  "/features",
  "/whats-new",
  "/purchasing/requisitions",
  "/purchasing/requisitions/new",
  "/purchasing/purchase-orders",
  "/receiving/receipts",
  "/ap-invoices",
  "/payables",
  "/sales/orders",
  "/fulfillment/shipments",
  "/billing",
  "/receivables/payments/new",
];

async function noSidewaysScroll(page: Page): Promise<void> {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow, "page is wider than the window").toBeLessThanOrEqual(1);
}

for (const path of PAGES) {
  test(`${path} renders in the pseudo-locale without overflow`, async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`.${path}?lang=en-XA`);
    await page.getByRole("heading", { level: 1 }).first().waitFor();
    await expect(page.locator("html")).toHaveAttribute("lang", "en");

    // Shell controls come from the catalog, so they are bracketed and accented.
    const help = page.getByRole("button", { name: /^\[.*~\]$/ }).first();
    await expect(help).toBeVisible();
    await expect(page.getByRole("button", { name: /^\[Ĥ.*\]$/ })).toHaveCount(1);

    await noSidewaysScroll(page);
    expect(errors).toEqual([]);
  });
}

test("the help panel and its controls hold the longer text", async ({ page }) => {
  await page.goto("./purchasing/requisitions?lang=en-XA");
  await page.getByRole("button", { name: /^\[Ĥ/ }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("link", { name: /^\[Ŕ/ })).toBeVisible();
  const box = await dialog.boundingBox();
  const viewport = page.viewportSize();
  expect(box && viewport && box.x + box.width <= viewport.width + 1).toBe(true);
  await expect(dialog.getByLabel(/^\[Šĥ/)).toBeVisible();
  await expect(dialog.getByText("Share anonymous usage data")).toHaveCount(0);
});
