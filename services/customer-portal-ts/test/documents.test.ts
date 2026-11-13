import { afterEach, describe, expect, it } from "vitest";
import { invoiceDocument, MAX_LINES, statementDocument, type PortalInvoice, type PortalStatement } from "../src/documents.ts";
import { api, CUSTOMER_A, CUSTOMER_B, ID_1, LE_1, signIn, startRig, tokenClaims, type Rig } from "./helpers.ts";

const invoice: PortalInvoice = {
  id: ID_1,
  type: "INVOICE",
  status: "POSTED",
  number: "INV-2026-0042",
  currency: "USD",
  invoiceDate: "2026-11-01",
  dueDate: "2026-12-01",
  paymentTermDays: 30,
  totals: { net: "100.00", charges: "0.00", tax: "8.00", gross: "108.00" },
  lines: [{ lineNo: 1, description: "Pump seal kit", qty: "2", unitPrice: "50.00", discountPct: "0", net: "100.00", tax: "8.00" }],
};

const statement: PortalStatement = {
  asOf: "2026-11-10",
  sections: [
    { currency: "USD", closingBalance: "108.00", lines: [{ sourceType: "INVOICE", documentNumber: "INV-2026-0042", documentDate: "2026-11-01", dueDate: "2026-12-01", amount: "108.00", openAmount: "108.00", runningBalance: "108.00" }] },
    { currency: "EUR", closingBalance: "-20.00", lines: [{ sourceType: "CREDIT_NOTE", documentNumber: "CN-7", documentDate: "2026-10-01", amount: "-20.00", openAmount: "-20.00", runningBalance: "-20.00" }] },
  ],
};

describe("document requests", () => {
  it("prints an invoice as read, with the seller's name and its totals", () => {
    const d = invoiceDocument(invoice, "Cedar Ridge Pumps");
    expect(d).toMatchObject({ documentType: "CUSTOMER_INVOICE", number: "INV-2026-0042", legalEntity: { name: "Cedar Ridge Pumps" }, title: "Invoice" });
    expect(d.lines).toEqual([["1", "Pump seal kit", "2", "50.00", "0", "100.00", "8.00"]]);
    expect(d.columns).toHaveLength(7);
    expect(d.totals?.at(-1)).toEqual({ label: "Total USD", value: "108.00" });
    expect(d.totals?.some((t) => t.label === "Charges")).toBe(false);
    expect(invoiceDocument({ ...invoice, type: "CREDIT_NOTE", originalNumber: "INV-1" }, null)).toMatchObject({ title: "Credit note", legalEntity: { name: "Your supplier" } });
  });

  it("prints the statement as a letter with every currency's lines and closing balance", () => {
    const d = statementDocument(statement, "Cedar Ridge Pumps");
    expect(d.documentType).toBe("PERIODIC_STATEMENT");
    expect(d.body).toContain("Closing balance: 108.00 USD, -20.00 EUR.");
    expect(d.lines).toEqual([
      ["USD", "INV-2026-0042", "Invoice", "2026-11-01", "2026-12-01", "108.00", "108.00", "108.00"],
      ["EUR", "CN-7", "Credit note", "2026-10-01", "", "-20.00", "-20.00", "-20.00"],
    ]);
    expect(d.totals).toEqual([{ label: "Closing balance USD", value: "108.00" }, { label: "Closing balance EUR", value: "-20.00" }]);
    expect(statementDocument({ asOf: "2026-11-10", sections: [] }, null).body).toContain("Nothing is open");
  });

  it("turns the control characters print refuses into spaces, keeping a line break only in a table cell", () => {
    const d = invoiceDocument({ ...invoice, number: "INV\t7", lines: [{ ...invoice.lines![0]!, description: "Seal kit\r\nsize\tM\u2028" }] }, "Cedar\u0007 Ridge\nPumps");
    expect(d.number).toBe("INV 7");
    expect(d.lines[0]![1]).toBe("Seal kit\nsize M ");
    expect(d.legalEntity.name).toBe("Cedar  Ridge Pumps");
    const s = statementDocument({ asOf: "2026-11-10", sections: [{ ...statement.sections[0]!, lines: [{ ...statement.sections[0]!.lines[0]!, documentNumber: "INV\n42" }] }] }, null);
    expect(s.lines[0]![1]).toBe("INV 42");
  });

  it("stays inside print's line limit and says so", () => {
    const line = statement.sections[0]!.lines[0]!;
    const long = statementDocument({ asOf: "2026-11-10", sections: [{ currency: "USD", closingBalance: "0", lines: Array.from({ length: MAX_LINES + 5 }, () => line) }] }, null);
    expect(long.lines).toHaveLength(MAX_LINES);
    expect(long.notes).toContain(`first ${MAX_LINES} of ${MAX_LINES + 5}`);
  });
});

let rig: Rig | undefined;
afterEach(async () => {
  await rig?.close();
  rig = undefined;
});

const PDF = Buffer.from("%PDF-1.7 fake");

describe("PDF downloads", () => {
  it("reads the invoice for the customer, has print render it with the portal's render duty, and returns the PDF", async () => {
    rig = await startRig();
    rig.on("http://billing.test", () => Response.json(invoice));
    rig.on("http://identity.test", () => Response.json({ displayName: "Cedar Ridge Pumps" }));
    rig.on("http://print.test", () => new Response(PDF, { headers: { "content-type": "application/pdf" } }));
    const s = await signIn(rig);
    const res = await api(rig, s, "GET", `/api/v1/invoices/${ID_1}/pdf`);
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("application/pdf");
    expect(res.headers.get("content-disposition")).toBe('attachment; filename="INV-2026-0042.pdf"');
    expect(Buffer.from(await res.arrayBuffer()).equals(PDF)).toBe(true);
    const read = rig.calls.find((c) => c.url.startsWith("http://billing.test"))!;
    expect(read.url).toBe(`http://billing.test/v1/billing/customer-portal/invoices/${ID_1}`);
    expect(tokenClaims(read.headers.get("x-passage-service-token")!)).toMatchObject({ aud: "billing", cus: CUSTOMER_A, le: LE_1, duties: ["billing.customer-portal.read"] });
    const render = rig.calls.find((c) => c.url.startsWith("http://print.test"))!;
    expect(render.url).toBe("http://print.test/print/v1/documents:render");
    expect(render.method).toBe("POST");
    expect(tokenClaims(render.headers.get("x-passage-service-token")!)).toMatchObject({ aud: "print", cus: CUSTOMER_A, duties: ["print.customer-portal.render"] });
    expect(JSON.parse(render.body)).toMatchObject({ documentType: "CUSTOMER_INVOICE", number: "INV-2026-0042", legalEntity: { name: "Cedar Ridge Pumps" } });
  });

  it("renders the statement as of a date, for an account the contact may act for", async () => {
    rig = await startRig();
    rig.on("http://party.test", () => Response.json({ items: [{ id: CUSTOMER_B }] }));
    rig.on("http://receivables.test", () => Response.json(statement));
    rig.on("http://print.test", () => new Response(PDF, { headers: { "content-type": "application/pdf" } }));
    const s = await signIn(rig);
    const res = await api(rig, s, "GET", "/api/v1/statement/pdf?asOf=2026-11-10", undefined, { "x-customer": CUSTOMER_B });
    expect(res.status).toBe(200);
    expect(res.headers.get("content-disposition")).toBe('attachment; filename="statement-2026-11-10.pdf"');
    const read = rig.calls.find((c) => c.url.startsWith("http://receivables.test"))!;
    expect(read.url).toBe("http://receivables.test/v1/receivables/customer-portal/statement?asOf=2026-11-10");
    expect(tokenClaims(read.headers.get("x-passage-service-token")!)).toMatchObject({ cus: CUSTOMER_B });
    const render = rig.calls.find((c) => c.url.startsWith("http://print.test"))!;
    expect(JSON.parse(render.body)).toMatchObject({ documentType: "PERIODIC_STATEMENT", legalEntity: { name: "Your supplier" } });
  });

  it("passes on a refused read and never calls print", async () => {
    rig = await startRig();
    rig.on("http://billing.test", () => Response.json({ code: "BIL-0404", status: 404, detail: "invoice not found" }, { status: 404 }));
    const s = await signIn(rig);
    const res = await api(rig, s, "GET", `/api/v1/invoices/${ID_1}/pdf`);
    expect(res.status).toBe(404);
    expect(((await res.json()) as { code: string }).code).toBe("BIL-0404");
    expect(rig.calls.some((c) => c.url.startsWith("http://print.test"))).toBe(false);
  });

  it("answers 502, not a sign-in, when the service refuses the portal's own token", async () => {
    rig = await startRig();
    rig.on("http://billing.test", () => Response.json({ code: "AUTH-0001", status: 401, detail: "service token rejected" }, { status: 401 }));
    const s = await signIn(rig);
    const res = await api(rig, s, "GET", `/api/v1/invoices/${ID_1}/pdf`);
    expect(res.status).toBe(502);
    expect(((await res.json()) as { code: string }).code).toBe("CUS-BFF-502");
    expect(rig.calls.some((c) => c.url.startsWith("http://print.test"))).toBe(false);
  });

  it("refuses a malformed id or date, another customer's account and a failed render", async () => {
    rig = await startRig();
    rig.on("http://party.test", () => Response.json({ items: [] }));
    const s = await signIn(rig);
    expect((await api(rig, s, "GET", "/api/v1/invoices/not-an-id/pdf")).status).toBe(404);
    expect((await api(rig, s, "GET", "/api/v1/statement/pdf?asOf=10.11.2026")).status).toBe(400);
    expect((await api(rig, s, "GET", "/api/v1/statement/pdf", undefined, { "x-customer": CUSTOMER_B })).status).toBe(403);
    rig.on("http://receivables.test", () => Response.json(statement));
    rig.on("http://print.test", () => Response.json({ code: "PRN-0008", status: 422, detail: "number is too long" }, { status: 422 }));
    const failed = await api(rig, s, "GET", "/api/v1/statement/pdf");
    expect(failed.status).toBe(502);
    expect(((await failed.json()) as { code: string }).code).toBe("CUS-BFF-409");
    expect((await api(rig, undefined, "GET", "/api/v1/statement/pdf")).status).toBe(401);
  });
});
