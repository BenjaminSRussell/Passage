import { beforeEach, describe, expect, it } from "vitest";
import { setLookupFetcher } from "../../shell/lookup/entities";
import { json, server, useFakeServer } from "../../shell/testing/fakeServer";
import type { Invoice } from "./api/client";
import { invoiceDocument } from "./invoiceDocument";

useFakeServer();
beforeEach(() => setLookupFetcher(null));

const invoice = {
  id: "inv1",
  legalEntityId: "le1",
  customerId: "c1",
  invoiceAccountId: "c1",
  type: "INVOICE",
  status: "POSTED",
  number: "INV-2026-0042",
  currency: "CZK",
  invoiceDate: "2026-11-10",
  dueDate: "2026-12-10",
  totals: { net: "3850.00", charges: "0.00", tax: "808.50", gross: "4658.50" },
  lines: [{ lineNo: 1, description: "Сервисное обслуживание\nнасосной станции", qty: "1", unitPrice: "3850.00", net: "3850.00", tax: "808.50" }],
} as unknown as Invoice;

const routes: Record<string, unknown> = {
  "/api/v1/org/legal-entities/le1": { name: "Cedar Ridge Pumps, Inc." },
  "/api/v1/party/customers/c1": { id: "c1", partyId: "p1", accountNumber: "C-10021" },
  "/api/v1/party/parties/p1": { id: "p1", name: "Žluťoučký kůň s.r.o." },
  "/api/v1/org/addresses": {
    items: [
      { formatted: "Sklad 4\n120 00 Praha 2\nCZ", purposes: [{ purpose: "delivery", primary: true }] },
      { formatted: "Náměstí Míru 15\n120 00 Praha 2\nCZ", purposes: [{ purpose: "invoice", primary: true }] },
    ],
  },
};

describe("invoice document", () => {
  it("bills the customer at its invoice address and keeps the description's line break", async () => {
    server.handler = (url) => (url.pathname in routes ? json(200, routes[url.pathname]) : undefined);
    const doc = await invoiceDocument(invoice);
    expect(doc.number).toBe("INV-2026-0042");
    expect(doc.counterparty).toEqual({ label: "Bill to", name: "Žluťoučký kůň s.r.o.", address: ["Náměstí Míru 15", "120 00 Praha 2", "CZ"] });
    expect(doc.fields?.map((f) => f.label)).toEqual(["Customer number", "Invoice date", "Due date", "Currency"]);
    expect(doc.lines[0][0]).toBe("Сервисное обслуживание\nнасосной станции");
    expect(doc.totals?.at(-1)).toEqual({ label: "Total CZK", value: "4658.50" });
    expect(doc).not.toHaveProperty("notes");
    expect(doc).not.toHaveProperty("additionalParties");
  });

  it("shows a credit note's reason and the invoice it credits", async () => {
    server.handler = (url) => (url.pathname in routes ? json(200, routes[url.pathname]) : undefined);
    const doc = await invoiceDocument({ ...invoice, type: "CREDIT_NOTE", originalNumber: "INV-2026-0040", reason: "Zboží vráceno" } as Invoice);
    expect(doc.fields?.map((f) => f.label)).toEqual(["Customer number", "Credit date", "Credits invoice", "Currency"]);
    expect(doc.notes).toBe("Zboží vráceno");
  });
});
