import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AllocationPreview, Invoice } from "./api/client";
import { emptyPreview, toPreviewInput, validatePreview } from "./AllocationPreviewPage";
import { emptyFreeText, toFreeTextInput, validateFreeText } from "./NewFreeTextInvoicePage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-000000000002";
const ACCT = "0190f0a0-0000-7000-8000-000000000003";
const INV = "0190f0a0-0000-7000-8000-000000000901";

const proposal: Invoice = {
  id: INV,
  legalEntityId: LE,
  customerId: CUST,
  invoiceAccountId: ACCT,
  type: "FREE_TEXT",
  status: "PROPOSAL",
  currency: "EUR",
  invoiceDate: "2027-03-10",
  dueDate: "2027-04-09",
  paymentTermDays: 30,
  totals: { net: "100.00", charges: "0.00", tax: "19.00", gross: "119.00" },
  lines: [
    { lineNo: 1, description: "Consulting March", qty: "1", unitPrice: "100", discountPct: "0", net: "100.00", tax: "19.00", revenueAccount: "4900", taxCode: "DE19" },
  ],
  installments: [],
  version: 1,
};

const posted: Invoice = { ...proposal, status: "POSTED", number: "INV-2027-0000001", postingDate: "2027-03-12", version: 2 };

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Invoices", () => {
  it("lists invoices with status and gross and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/billing/invoices") return json(200, { items: [proposal, { ...posted, id: "inv-2" }] });
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) return json(200, proposal);
      return undefined;
    };
    const { container } = renderAt("/billing");
    const table = await screen.findByRole("table", { name: "Invoices" });
    await waitFor(() => expect(table).toHaveTextContent("INV-2027-0000001"));
    expect(table).toHaveTextContent("119.00 EUR");
    expect(table).toHaveTextContent("Free text");
    expect(table).toHaveTextContent("Posted");
    expect(server.calls[0].url.searchParams.get("limit")).toBe("100");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Proposal" }));
    expect(await screen.findByRole("heading", { name: "Proposal" })).toBeInTheDocument();
  });

  it("filters by status through the service", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/billing");
    expect(await screen.findByText("No invoices yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "POSTED" } });
    expect(await screen.findByText("No posted invoices")).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("POSTED");
  });

  it("shows the error state with a retry", async () => {
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty billing.invoice.view" });
    renderAt("/billing");
    expect(await screen.findByRole("alert")).toHaveTextContent("missing duty billing.invoice.view (AUTH-0003)");
    server.handler = () => json(200, { items: [proposal] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("table", { name: "Invoices" })).toHaveTextContent("Proposal");
  });
});

describe("Invoice detail", () => {
  it("shows lines and totals and posts a proposal with If-Match", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/billing/invoices/${INV}:post` && init.method === "POST") return json(200, posted);
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) return json(200, proposal);
      return undefined;
    };
    const { container } = renderAt(`/billing/invoices/${INV}`);
    const lines = await screen.findByRole("table", { name: "Invoice lines" });
    expect(lines).toHaveTextContent("Consulting March");
    expect(lines).toHaveTextContent("DE19");
    expect(screen.getByText("Assigned at posting")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Post invoice" }));
    expect(await screen.findByRole("heading", { name: "INV-2027-0000001" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Posted as INV-2027-0000001");
    expect(screen.queryByRole("button", { name: "Post invoice" })).not.toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(new Headers(post?.init.headers).get("If-Match")).toBe('"1"');
  });

  it("explains a post refused because the tax engine is down and keeps the proposal", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(503, { code: "BIL-0001", detail: "Tax engine unavailable; invoice not posted" });
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) return json(200, proposal);
      return undefined;
    };
    renderAt(`/billing/invoices/${INV}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post invoice" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The tax engine is unavailable; nothing was posted");
    expect(alert).toHaveTextContent("(BIL-0001)");
    expect(screen.getByRole("button", { name: "Post invoice" })).toBeInTheDocument();
  });

  it("reloads the invoice when it changed since it was opened", async () => {
    let reads = 0;
    server.handler = (url, init) => {
      if (init.method === "POST") return json(412, { code: "BIL-0103", detail: "the invoice changed since it was read; reload and review it" });
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) {
        reads += 1;
        return json(200, reads === 1 ? proposal : posted);
      }
      return undefined;
    };
    renderAt(`/billing/invoices/${INV}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post invoice" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The invoice changed since you opened it");
    expect(await screen.findByRole("heading", { name: "INV-2027-0000001" })).toBeInTheDocument();
  });

  it("shows a load failure with a retry", async () => {
    server.handler = () => json(404, { code: "BIL-0100", detail: "invoice not found" });
    renderAt(`/billing/invoices/${INV}`);
    expect(await screen.findByRole("alert")).toHaveTextContent("invoice not found (BIL-0100)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("New free-text invoice", () => {
  it("checks the form before calling the service", async () => {
    const { container } = renderAt("/billing/free-text-invoices/new");
    fireEvent.click(screen.getByRole("button", { name: "Save proposal" }));
    expect(await screen.findByText("Enter the customer id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter the tax code in capitals, for example DE19.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a proposal with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/billing/free-text-invoices" && init.method === "POST") return json(201, proposal);
      if (url.pathname === `/api/v1/billing/invoices/${INV}`) return json(200, proposal);
      return undefined;
    };
    renderAt("/billing/free-text-invoices/new");
    fill("Legal entity id", LE);
    fill("Customer id", CUST);
    fill("Invoice account id", ACCT);
    fill("Invoice date", "2027-03-10");
    fill("Description", "Consulting March");
    fill("Unit price", "100.00");
    fill("Revenue account", "4900");
    fill("Tax code", "DE19");
    fireEvent.click(screen.getByRole("button", { name: "Save proposal" }));
    expect(await screen.findByRole("heading", { name: "Proposal" })).toBeInTheDocument();
    const create = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(create!)).toMatch(/.+/);
    expect(create?.body).toEqual({
      legalEntityId: LE,
      customerId: CUST,
      invoiceAccountId: ACCT,
      currency: "EUR",
      invoiceDate: "2027-03-10",
      paymentTermDays: 30,
      lines: [{ description: "Consulting March", qty: "1", unitPrice: "100.00", revenueAccount: "4900", taxCode: "DE19" }],
    });
  });

  it("adds and removes lines", () => {
    renderAt("/billing/free-text-invoices/new");
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(screen.getByRole("group", { name: "Line 2" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove line 2" }));
    expect(screen.queryByRole("group", { name: "Line 2" })).not.toBeInTheDocument();
  });

  it("shows service field errors next to the fields and a tax outage as an alert", async () => {
    const values = {
      ...emptyFreeText("2027-03-10"),
      legalEntityId: LE,
      customerId: CUST,
      invoiceAccountId: ACCT,
      lines: [{ description: "x", qty: "1", unitPrice: "1", discountPct: "", revenueAccount: "4900", taxCode: "XX" }],
    };
    expect(validateFreeText(values)).toEqual({});
    expect(toFreeTextInput({ ...values, chargeAmount: " 5.00 " }).chargeAmount).toBe("5.00");

    server.handler = () => json(400, { code: "COM-0001", detail: "request failed validation", errors: { "lines[0].taxCode": "must be a tax code" } });
    renderAt("/billing/free-text-invoices/new");
    fill("Legal entity id", LE);
    fill("Customer id", CUST);
    fill("Invoice account id", ACCT);
    fill("Description", "x");
    fill("Unit price", "1");
    fill("Revenue account", "4900");
    fill("Tax code", "XX");
    fireEvent.click(screen.getByRole("button", { name: "Save proposal" }));
    expect(await screen.findByText("must be a tax code")).toBeInTheDocument();

    server.handler = () => json(503, { code: "BIL-0001", detail: "Tax engine unavailable; invoice not posted" });
    fireEvent.click(screen.getByRole("button", { name: "Save proposal" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The tax engine is unavailable; nothing was saved");
  });
});

describe("Revenue allocation", () => {
  const preview: AllocationPreview = {
    currency: "USD",
    transactionPrice: "10800.00",
    residualRule: "LARGEST",
    obligations: [
      { id: "hardware", ssp: "8000.00", sspSource: "EXPLICIT", method: "POINT_IN_TIME", allocated: "7200.00" },
      { id: "installation", ssp: "1500.00", sspSource: "EXPLICIT", method: "MILESTONE", allocated: "1350.00" },
      {
        id: "support",
        ssp: "2500.00",
        sspSource: "EXPLICIT",
        method: "RATABLE",
        allocated: "2250.00",
        schedule: [
          { start: "2027-03-10", end: "2027-03-31", amount: "133.06" },
          { start: "2027-04-01", end: "2027-04-30", amount: "187.50" },
        ],
      },
    ],
  };

  it("validates ratable terms and unique names", () => {
    const v = {
      ...emptyPreview,
      transactionPrice: "10800",
      obligations: [
        { id: "a", ssp: "1", sspSource: "EXPLICIT" as const, method: "RATABLE" as const, termStart: "2027-03-10", termEnd: "2027-03-01" },
        { id: "a", ssp: "0", sspSource: "EXPLICIT" as const, method: "POINT_IN_TIME" as const, termStart: "", termEnd: "" },
      ],
    };
    expect(Object.keys(validatePreview(v)).sort()).toEqual(["obligations[0].term", "obligations[1].id", "obligations[1].ssp"]);
    expect(toPreviewInput({ ...v, obligations: [v.obligations[1]] }).obligations[0]).not.toHaveProperty("termStart");
  });

  it("allocates the BR-29.1 bundle and shows the support schedule", async () => {
    server.handler = (url, init) => (url.pathname === "/api/v1/revrec/allocations:preview" && init.method === "POST" ? json(200, preview) : undefined);
    const { container } = renderAt("/billing/revenue-allocation");
    fill("Contract price", "10800.00");
    fill("Name", "support");
    fill("Standalone selling price", "2500");
    fireEvent.change(screen.getByLabelText("Recognition"), { target: { value: "RATABLE" } });
    fill("Term start", "2027-03-10");
    fill("Term end", "2028-03-09");
    fireEvent.click(screen.getByRole("button", { name: "Allocate" }));
    const allocation = await screen.findByRole("table", { name: "Allocation" });
    expect(allocation).toHaveTextContent("7200.00 USD");
    expect(allocation).toHaveTextContent("Milestone");
    expect(screen.getByRole("table", { name: "Schedule for support" })).toHaveTextContent("133.06");
    expect(server.calls[0].body).toMatchObject({
      currency: "USD",
      transactionPrice: "10800.00",
      residualRule: "LARGEST",
      obligations: [{ id: "support", ssp: "2500", method: "RATABLE", termStart: "2027-03-10", termEnd: "2028-03-09" }],
    });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a refused preview", async () => {
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty revrec.view" });
    renderAt("/billing/revenue-allocation");
    fill("Contract price", "100");
    fill("Name", "a");
    fill("Standalone selling price", "1");
    fireEvent.click(screen.getByRole("button", { name: "Allocate" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("missing duty revrec.view (AUTH-0003)");
  });
});
