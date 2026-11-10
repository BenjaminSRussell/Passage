import { expect, test } from "@playwright/test";
import { ShellPage } from "./shell.page";

const PO = "0195f0a0-0000-7000-8000-000000000071";
const VENDOR = "0195f0a0-0000-7000-8000-000000000072";
const LE = "00000000-0000-0000-0000-0000000000e1";

test("the guided page shows counts and a purchase order offers receiving with the order carried over", async ({ page }) => {
  await page.route("**/api/v1/party/**", (route) => route.fulfill({ status: 404, json: { code: "COM-0404", detail: "none" } }));
  await page.route("**/api/v1/procurement/purchase-orders?**", (route) => route.fulfill({ json: { items: [{}, {}] } }));
  await page.route(`**/api/v1/procurement/purchase-orders/${PO}`, (route) =>
    route.fulfill({
      json: { id: PO, number: "PO-1042", legalEntityId: LE, vendorId: VENDOR, currency: "USD", approvalStatus: "CONFIRMED", documentStatus: "OPEN", versionNo: 0, changeManagement: false, subtotal: "0", discountTotal: "0", chargesTotal: "0", taxTotal: "0", total: "0", rowVersion: 1, lines: [], charges: [], createdAt: "2026-11-10T10:00:00Z", updatedAt: "2026-11-10T10:00:00Z" },
      headers: { ETag: '"1"' },
    }),
  );
  await page.route("**/api/v1/procurement/purchase-orders/*/**", (route) => route.fulfill({ json: { items: [] } }));
  // The last route registered wins, so the catch-all goes first.
  await page.route("**/api/v1/receiving/**", (route) => route.fulfill({ json: { items: [] } }));
  await page.route("**/api/v1/receiving/related-documents**", (route) => route.fulfill({ json: { purchaseOrderId: PO, receipts: [], invoices: [], truncated: false } }));
  const shell = new ShellPage(page);

  await shell.goto("/purchasing/procure-to-pay");
  await expect(page.getByRole("list", { name: "Procure to pay steps" })).toBeVisible();
  await expect(page.getByText("2 purchase orders awaiting receipt")).toBeVisible();

  await shell.goto(`/purchasing/purchase-orders/${PO}`);
  const flow = page.getByRole("region", { name: "Document flow of this purchase order" });
  await expect(flow.getByText("Next: receive the goods")).toBeVisible();
  await flow.getByRole("link", { name: "Record arrival" }).click();
  await expect(page).toHaveURL(new RegExp(`/receiving/receipts/new\\?purchaseOrderId=${PO}&vendorId=${VENDOR}`));
});
