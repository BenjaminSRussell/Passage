import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import type { ReactElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { configureApi } from "../src/api";
import { App } from "../src/App";
import { balances, OpenItems, Payments, Statement } from "../src/pages/Account";
import { InvoiceDetail, Invoices } from "../src/pages/Invoices";
import { OrderDetail, Orders } from "../src/pages/Orders";
import { ReturnDetail, Returns } from "../src/pages/Returns";

const ID = "22222222-2222-4222-8222-222222222222";

const invoice = {
  id: ID, type: "SHIPMENT", status: "POSTED", number: "INV-2027-1", currency: "USD", invoiceDate: "2027-03-01", dueDate: "2027-03-31", paymentTermDays: 30,
  totals: { net: "100.00", charges: "0.00", tax: "19.00", gross: "119.00" },
  lines: [{ lineNo: 1, description: "Pump housing", qty: "2", unitPrice: "50", discountPct: "0", net: "100.00", tax: "19.00" }],
};
const credit = { ...invoice, id: "33333333-3333-4333-8333-333333333333", type: "CREDIT_NOTE", number: "CN-1", originalNumber: "INV-2027-1" };
const order = {
  id: ID, orderNumber: "SO-0000001", orderType: "ORDER", status: "CONFIRMED", currency: "USD", requestedShipDate: "2027-04-01", externalRef: "PO-77", netTotal: "100.00", createdAt: "2027-03-01T00:00:00Z",
  lines: [{ lineNo: 1, quantity: "2", unit: "EA", unitPrice: "50", lineNet: "100", requestedShipDate: "2027-04-01", lineStatus: "OPEN", deliveredQty: "0", invoicedQty: "0" }],
};
const ret = {
  id: ID, rmaNumber: "RMA-1", originalInvoiceNumber: "INV-2027-1", returnReasonCode: "DEFECT", status: "AUTHORIZED", currency: "USD", expiresAt: "2027-05-01",
  gross: "50", restocking: "5", net: "45", tax: "0", createdAt: "2027-03-02T00:00:00Z",
  lines: [{ lineNo: 1, quantityExpected: "1", quantityReceived: "0", quantityAccepted: "0", unit: "EA", unitPrice: "50" }],
};
const item = (over: Record<string, unknown>) => ({ id: "i", documentNumber: "INV-1", documentDate: "2027-01-01", dueDate: "2020-02-01", currency: "USD", amount: "100.00", openAmount: "100.00", side: "DEBIT", status: "OPEN", sourceType: "INVOICE", ...over });

interface Call { method: string; url: string; headers: Record<string, string> }
let calls: Call[];
let routes: Record<string, unknown>;

beforeEach(() => {
  calls = [];
  routes = {};
  configureApi({ csrfToken: "csrf-1", legalEntityId: "le-1", onSignedOut: () => undefined });
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({ method, url, headers: init?.headers as Record<string, string> });
    const key = `${method} ${url.split("?")[0]}`;
    if (!(key in routes)) return new Response(JSON.stringify({ code: "NF", detail: `no stub for ${key}` }), { status: 404 });
    const r = routes[key];
    return r instanceof Response ? r : new Response(JSON.stringify(r), { status: 200, headers: { "content-type": "application/json" } });
  }));
});

function at(path: string, pattern: string, el: ReactElement) {
  return render(<MemoryRouter initialEntries={[path]}><Routes><Route path={pattern} element={el} /></Routes></MemoryRouter>);
}

async function accessible(container: HTMLElement) {
  const r = await axe.run(container, { rules: { "color-contrast": { enabled: false } } });
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.html}`)).toEqual([]);
}

describe("invoices", () => {
  it("lists invoices and credit notes without ids on screen, and links to the invoice", async () => {
    routes["GET /api/v1/invoices"] = { items: [invoice, credit] };
    const { container } = at("/invoices", "/invoices", <Invoices />);
    expect(await screen.findByRole("link", { name: "INV-2027-1" })).toHaveAttribute("href", `/invoices/${ID}`);
    expect(screen.getByText("Credit note")).toBeInTheDocument();
    expect(container.textContent).not.toContain(ID);
    await accessible(container);
  });

  it("shows an invoice with its lines and totals", async () => {
    routes[`GET /api/v1/invoices/${ID}`] = invoice;
    const { container } = at(`/invoices/${ID}`, "/invoices/:id", <InvoiceDetail />);
    expect(await screen.findByText("Pump housing")).toBeInTheDocument();
    expect(screen.getByText("119.00 USD")).toBeInTheDocument();
    await accessible(container);
  });

  it("downloads the invoice PDF with the legal entity header and the BFF's file name", async () => {
    routes[`GET /api/v1/invoices/${ID}`] = invoice;
    routes[`GET /api/v1/invoices/${ID}/pdf`] = new Response("%PDF-1.7", { status: 200, headers: { "content-type": "application/pdf", "content-disposition": 'attachment; filename="INV-2027-1.pdf"' } });
    const created = vi.fn(() => "blob:pdf");
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: created, revokeObjectURL: vi.fn() }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    at(`/invoices/${ID}`, "/invoices/:id", <InvoiceDetail />);
    await userEvent.click(await screen.findByRole("button", { name: "Download invoice PDF" }));
    await waitFor(() => expect(click).toHaveBeenCalled());
    const call = calls.find((c) => c.url === `/api/v1/invoices/${ID}/pdf`)!;
    expect(call.headers["x-legal-entity"]).toBe("le-1");
    expect((click.mock.instances[0] as unknown as HTMLAnchorElement).download).toBe("INV-2027-1.pdf");
    click.mockRestore();
  });

  it("says why a PDF could not be made", async () => {
    routes[`GET /api/v1/invoices/${ID}`] = invoice;
    routes[`GET /api/v1/invoices/${ID}/pdf`] = new Response(JSON.stringify({ code: "CUS-BFF-409", detail: "the document could not be rendered; try again later" }), { status: 502 });
    at(`/invoices/${ID}`, "/invoices/:id", <InvoiceDetail />);
    await userEvent.click(await screen.findByRole("button", { name: "Download invoice PDF" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("the document could not be rendered");
  });

  it("shows the problem when the invoice is not the customer's", async () => {
    routes[`GET /api/v1/invoices/${ID}`] = new Response(JSON.stringify({ code: "BIL-0100", detail: "invoice not found" }), { status: 404 });
    at(`/invoices/${ID}`, "/invoices/:id", <InvoiceDetail />);
    expect(await screen.findByRole("alert")).toHaveTextContent("invoice not found");
  });
});

describe("account", () => {
  it("adds up what is owed per currency, nets credits and counts overdue debits", () => {
    const rows = balances([
      item({ openAmount: "100.00" }),
      item({ openAmount: "-30.00", side: "CREDIT" }),
      item({ currency: "EUR", openAmount: "10.00", dueDate: "2999-01-01" }),
    ]);
    expect(rows).toEqual([{ currency: "USD", open: 70, overdue: 100 }, { currency: "EUR", open: 10, overdue: 0 }]);
  });

  it("shows open items with the balance", async () => {
    routes["GET /api/v1/open-items"] = { items: [item({ id: "a" })] };
    const { container } = at("/open-items", "/open-items", <OpenItems />);
    expect(await screen.findByText("Balance 100.00 USD, of which 100.00 USD is overdue")).toBeInTheDocument();
    expect(calls[0]?.url).toBe("/api/v1/open-items?status=open&limit=200");
    await accessible(container);
  });

  it("shows payments", async () => {
    routes["GET /api/v1/payments"] = { items: [{ id: "p", paymentMode: "ACH", amount: "50.00", unappliedAmount: "0.00", currency: "USD", receivedDate: "2027-03-05", valueDate: "2027-03-05", status: "APPLIED" }] };
    const { container } = at("/payments", "/payments", <Payments />);
    expect(await screen.findByText("ach")).toBeInTheDocument();
    await accessible(container);
  });

  it("asks for the statement as of the chosen day, and never names a customer", async () => {
    routes["GET /api/v1/statement"] = { asOf: "2027-03-31", sections: [{ currency: "USD", closingBalance: "100.00", aging: [], lines: [{ sourceType: "INVOICE", documentNumber: "INV-9", documentDate: "2027-03-01", amount: "100.00", openAmount: "100.00", runningBalance: "100.00" }] }] };
    const { container } = at("/statement", "/statement", <Statement />);
    expect(await screen.findByText("INV-9")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download statement PDF" })).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("As of"), "2027-03-31");
    await waitFor(() => expect(calls.some((c) => c.url === "/api/v1/statement?asOf=2027-03-31")).toBe(true));
    expect(JSON.stringify(calls)).not.toMatch(/customer/i);
    await accessible(container);
  });
});

describe("orders and returns", () => {
  it("lists orders with the customer's own reference", async () => {
    routes["GET /api/v1/orders"] = { items: [order] };
    const { container } = at("/orders", "/orders", <Orders />);
    expect(await screen.findByRole("link", { name: "SO-0000001" })).toHaveAttribute("href", `/orders/${ID}`);
    expect(screen.getByText("PO-77")).toBeInTheDocument();
    expect(container.textContent).not.toContain(ID);
    await accessible(container);
  });

  it("shows an order with its lines", async () => {
    routes[`GET /api/v1/orders/${ID}`] = order;
    const { container } = at(`/orders/${ID}`, "/orders/:id", <OrderDetail />);
    expect(await screen.findByText("2 EA")).toBeInTheDocument();
    await accessible(container);
  });

  it("lists returns and shows one with its credit", async () => {
    routes["GET /api/v1/returns"] = { items: [ret] };
    routes[`GET /api/v1/returns/${ID}`] = ret;
    const list = at("/returns", "/returns", <Returns />);
    expect(await screen.findByRole("link", { name: "RMA-1" })).toBeInTheDocument();
    await accessible(list.container);
    list.unmount();
    const { container } = at(`/returns/${ID}`, "/returns/:id", <ReturnDetail />);
    expect(await screen.findByText(/45 USD/)).toBeInTheDocument();
    await accessible(container);
  });
});

describe("the portal only reads", () => {
  it("sends only GETs with the legal entity and no customer, through every page", async () => {
    routes["GET /bff/me"] = { userId: "u", name: "Pat Buyer", legalEntityIds: ["le-1", "le-2"], csrfToken: "csrf-1" };
    routes["GET /api/v1/open-items"] = { items: [] };
    render(<MemoryRouter initialEntries={["/"]}><App /></MemoryRouter>);
    expect(await screen.findByText("You have nothing to pay.")).toBeInTheDocument();
    // Companies are named by position, never by their id.
    expect(screen.getByRole("option", { name: "Company 1" })).toBeInTheDocument();
    expect(screen.queryByText("le-1")).toBeNull();
    for (const c of calls) {
      expect(c.method).toBe("GET");
      expect(c.url).not.toMatch(/customer/i);
    }
    expect(calls.find((c) => c.url.startsWith("/api/v1/"))?.headers["x-legal-entity"]).toBe("le-1");
    expect(calls.every((c) => !("x-csrf-token" in c.headers))).toBe(true);
  });
});
