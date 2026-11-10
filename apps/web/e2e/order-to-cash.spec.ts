import { expect, test } from "@playwright/test";
import { ShellPage } from "./shell.page";

const ORDER = "0195f0a0-0000-7000-8000-000000000061";
const CUSTOMER = "0195f0a0-0000-7000-8000-000000000062";

const order = {
  id: ORDER,
  legalEntityId: "00000000-0000-0000-0000-0000000000e1",
  orderNumber: "SO-1001",
  customerId: CUSTOMER,
  invoiceAccountId: CUSTOMER,
  orderType: "STANDARD",
  status: "CONFIRMED",
  currency: "USD",
  requestedShipDate: "2026-11-20",
  channel: "WEB",
  netTotal: "100.00",
  version: 1,
  lines: [],
  holds: [],
  createdAt: "2026-11-10T10:00:00Z",
  updatedAt: "2026-11-10T10:00:00Z",
};

test("the guided page shows counts and an order offers its next step", async ({ page }) => {
  await page.route("**/api/v1/party/**", (route) => route.fulfill({ status: 404, json: { code: "COM-0404", detail: "none" } }));
  await page.route("**/api/v1/sales/orders?**", (route) => route.fulfill({ json: { items: [order] } }));
  await page.route(`**/api/v1/sales/orders/${ORDER}`, (route) => route.fulfill({ json: order, headers: { ETag: '"1"' } }));
  await page.route("**/api/v1/fulfillment/packing-slips**", (route) =>
    route.fulfill({ json: { items: [{ id: "s1", packingSlipNumber: "PS-1", shipmentId: "sh1", orderId: ORDER, status: "POSTED", postingDate: "2026-11-11", currency: "USD", totalCost: "60.00", reversalOfId: null }] } }),
  );
  await page.route("**/api/v1/billing/invoices**", (route) => route.fulfill({ json: { items: [] } }));
  await page.route("**/api/v1/receivables/open-items**", (route) => route.fulfill({ json: { items: [] } }));
  const shell = new ShellPage(page);

  await shell.goto("/sales/order-to-cash");
  await expect(page.getByRole("list", { name: "Order to cash steps" })).toBeVisible();
  await expect(page.getByText("1 shipments not invoiced")).toBeVisible();

  await shell.goto(`/sales/orders/${ORDER}`);
  const flow = page.getByRole("region", { name: "Document flow of this sales order" });
  await expect(flow.getByText("Packing slip PS-1")).toBeVisible();
  await expect(flow.getByText("Next: invoice the shipment")).toBeVisible();
  await flow.getByRole("link", { name: "Invoice shipments" }).click();
  await expect(page).toHaveURL(new RegExp(`/billing/proposals/new\\?customerId=${CUSTOMER}`));
});
