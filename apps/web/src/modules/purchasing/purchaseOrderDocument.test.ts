import { beforeEach, describe, expect, it } from "vitest";
import { setLookupFetcher } from "../../shell/lookup/entities";
import { json, server, useFakeServer } from "../../shell/testing/fakeServer";
import type { PurchaseOrder } from "./api/client";
import { purchaseOrderDocument } from "./purchaseOrderDocument";

useFakeServer();
beforeEach(() => setLookupFetcher(null));

const po = {
  id: "po1",
  number: "PO-000123",
  legalEntityId: "le1",
  vendorId: "v1",
  warehouseId: "w1",
  siteId: "s1",
  currency: "PLN",
  versionNo: 2,
  subtotal: "4500.00",
  discountTotal: "0.00",
  chargesTotal: "25.00",
  taxTotal: "1040.75",
  total: "5565.75",
  lines: [
    { lineNo: 1, productId: "prod1", description: "Odlew wirnika – stal nierdzewna", deliveryDate: "2026-12-01", qty: "40", unit: "szt", unitPrice: "112.50", lineAmount: "4500.00" },
    { lineNo: 2, productId: "prod2", deliveryDate: "2026-12-01", qty: "1", unit: "szt", unitPrice: "0.00", lineAmount: "0.00" },
  ],
} as unknown as PurchaseOrder;

const routes: Record<string, unknown> = {
  "/api/v1/org/legal-entities/le1": { name: "Łódzkie Zakłady Pomp Sp. z o.o.", primaryAddressId: "a-le" },
  "/api/v1/org/addresses/a-le": { formatted: "ul. Piotrkowska 104\n90-926 Łódź\nPL" },
  "/api/v1/party/vendors/v1": { id: "v1", partyId: "p1", accountNumber: "V-20004" },
  "/api/v1/party/parties/p1": { id: "p1", name: "Αθηναϊκή Χυτήρια Α.Ε." },
  "/api/v1/org/addresses": { items: [{ formatted: "Οδός Ερμού 25\n105 63 Αθήνα\nGR", purposes: [{ purpose: "business", primary: true }] }] },
  "/api/v1/org/warehouses/w1": { name: "Magazyn surowców", siteId: "s1" },
  "/api/v1/org/sites/s1": { name: "Zakład Łódź", addressId: "a-site" },
  "/api/v1/org/addresses/a-site": { formatted: "ul. Fabryczna 7\n90-001 Łódź\nPL" },
  "/api/v1/product/products/prod2": { id: "prod2", productNumber: "CST-200", name: "Volute casting" },
};

describe("purchase order document", () => {
  it("carries the vendor's name and address, the delivery address and Unicode text unchanged", async () => {
    server.handler = (url) => (url.pathname in routes ? json(200, routes[url.pathname]) : undefined);
    const doc = await purchaseOrderDocument(po);
    expect(doc.documentType).toBe("PURCHASE_ORDER");
    expect(doc.legalEntity).toEqual({ name: "Łódzkie Zakłady Pomp Sp. z o.o.", address: ["ul. Piotrkowska 104", "90-926 Łódź", "PL"] });
    expect(doc.counterparty).toEqual({ label: "Vendor", name: "Αθηναϊκή Χυτήρια Α.Ε.", address: ["Οδός Ερμού 25", "105 63 Αθήνα", "GR"] });
    expect(doc.additionalParties).toEqual([{ label: "Deliver to", name: "Magazyn surowców", address: ["ul. Fabryczna 7", "90-001 Łódź", "PL"] }]);
    expect(doc.fields).toContainEqual({ label: "Vendor number", value: "V-20004" });
    expect(doc.lines[0][0]).toBe("Odlew wirnika – stal nierdzewna");
    expect(doc.lines[1][0]).toContain("Volute casting");
    expect(doc.columns.filter((c) => c.align === "R").map((c) => c.header)).toEqual(["Qty", "Unit price", "Amount"]);
    expect(doc.totals?.at(-1)).toEqual({ label: "Total PLN", value: "5565.75" });
    expect(doc.totals?.map((t) => t.label)).toEqual(["Subtotal", "Charges", "Tax", "Total PLN"]);
  });

  it("still builds the document when the vendor, the addresses and the warehouse cannot be read", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/org/legal-entities/le1" ? json(200, { name: "Cedar Ridge Pumps" }) : undefined);
    const doc = await purchaseOrderDocument(po);
    expect(doc.legalEntity).toEqual({ name: "Cedar Ridge Pumps", address: [] });
    expect(doc.counterparty).toEqual({ label: "Vendor", name: "Vendor", address: [] });
    expect(doc).not.toHaveProperty("additionalParties");
    expect(doc.lines[1][0]).toBe("Line 2");
  });
});
