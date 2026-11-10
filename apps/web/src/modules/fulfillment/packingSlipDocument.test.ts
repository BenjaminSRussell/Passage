import { beforeEach, describe, expect, it } from "vitest";
import { setLookupFetcher } from "../../shell/lookup/entities";
import { json, server, useFakeServer } from "../../shell/testing/fakeServer";
import type { PackingSlip } from "./api/client";
import { packingSlipDocument } from "./packingSlipDocument";

useFakeServer();
beforeEach(() => setLookupFetcher(null));

const slip = {
  id: "ps1",
  packingSlipNumber: "PS-000777",
  orderId: "so1",
  postingDate: "2026-11-10",
  lines: [
    { orderLineId: "ol1", shipmentLineId: "sl1", qty: "3", costAmount: "30.00", costSnapshotId: "cs1" },
    { orderLineId: "ol2", shipmentLineId: "sl2", qty: "6", costAmount: "60.00", costSnapshotId: "cs2" },
  ],
} as unknown as PackingSlip;

const order = {
  id: "so1",
  legalEntityId: "le1",
  orderNumber: "SO-004512",
  customerId: "c1",
  lines: [
    { id: "ol1", lineNo: 1, productId: "prod1", unit: "pcs", warehouseId: "w1" },
    { id: "ol2", lineNo: 2, productId: "prod2", unit: "pcs", warehouseId: "w1" },
  ],
};

const routes: Record<string, unknown> = {
  "/api/v1/sales/orders/so1": order,
  "/api/v1/org/legal-entities/le1": { name: "Cedar Ridge Pumps, Inc." },
  "/api/v1/party/customers/c1": { id: "c1", partyId: "p1", accountNumber: "C-10021" },
  "/api/v1/party/parties/p1": { id: "p1", name: "Großhandel Müller GmbH" },
  "/api/v1/org/addresses": {
    items: [
      { formatted: "Königstraße 12\n70173 Stuttgart\nDE", purposes: [{ purpose: "invoice", primary: true }] },
      { formatted: "Lagerstraße 3\n70327 Stuttgart\nDE", purposes: [{ purpose: "delivery", primary: true }] },
    ],
  },
  "/api/v1/org/warehouses/w1": { name: "Finished goods", addressId: "a-w1", siteId: "s1" },
  "/api/v1/org/addresses/a-w1": { formatted: "1200 Dock Rd\nCedar Rapids, IA 52404\nUS" },
  "/api/v1/product/products/prod1": { id: "prod1", productNumber: "GSK-10", name: "Flange gasket DN10" },
  "/api/v1/product/products/prod2": { id: "prod2", productNumber: "GSK-20", name: "Flange gasket DN20" },
};

describe("packing slip document", () => {
  it("ships to the customer's delivery address from the warehouse of the lines, without prices", async () => {
    server.handler = (url) => (url.pathname in routes ? json(200, routes[url.pathname]) : undefined);
    const doc = await packingSlipDocument(slip);
    expect(doc.documentType).toBe("PACKING_SLIP");
    expect(doc.counterparty).toEqual({ label: "Ship to", name: "Großhandel Müller GmbH", address: ["Lagerstraße 3", "70327 Stuttgart", "DE"] });
    expect(doc.additionalParties).toEqual([{ label: "Ship from", name: "Finished goods", address: ["1200 Dock Rd", "Cedar Rapids, IA 52404", "US"] }]);
    expect(doc.fields).toEqual([
      { label: "Customer number", value: "C-10021" },
      { label: "Sales order", value: "SO-004512" },
      { label: "Shipped on", value: "2026-11-10" },
    ]);
    expect(doc.lines.map((l) => l.slice(1))).toEqual([
      ["1", "3", "pcs"],
      ["2", "6", "pcs"],
    ]);
    expect(doc.lines[0][0]).toContain("Flange gasket DN10");
    expect(JSON.stringify(doc)).not.toContain("30.00");
    expect(doc).not.toHaveProperty("totals");
  });

  it("uses the address a one-time customer's order carries", async () => {
    const oneTime = { name: "Jan Kowalski", line1: "ul. Długa 5", city: "Kraków", postalCode: "31-147", country: "PL" };
    server.handler = (url) => {
      if (url.pathname === "/api/v1/sales/orders/so1") return json(200, { ...order, oneTime });
      return url.pathname in routes ? json(200, routes[url.pathname]) : undefined;
    };
    const doc = await packingSlipDocument(slip);
    expect(doc.counterparty).toEqual({ label: "Ship to", name: "Jan Kowalski", address: ["ul. Długa 5", "31-147 Kraków", "PL"] });
    expect(server.calls.some((c) => c.url.pathname === "/api/v1/org/addresses")).toBe(false);
  });

  it("names no ship-from when the lines left from different warehouses", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/sales/orders/so1") return json(200, { ...order, lines: [order.lines[0], { ...order.lines[1], warehouseId: "w2" }] });
      return url.pathname in routes ? json(200, routes[url.pathname]) : undefined;
    };
    expect(await packingSlipDocument(slip)).not.toHaveProperty("additionalParties");
  });
});
