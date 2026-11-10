import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { rememberOption } from "../../shell/lookup/entities";
import type { Invoice } from "./api/client";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

beforeEach(() => {
  rememberOption("vendor", { id: "11111111-1111-4111-8111-111111111111", name: "Acme Supply", number: "V-100" });
});

const VENDOR = "11111111-1111-4111-8111-111111111111";
const ENTITY = "22222222-2222-4222-8222-222222222222";
const INVOICE = "33333333-3333-4333-8333-333333333333";
const RECEIPT = "0190a000-0000-7000-8000-000000000010";
const LEDGER = "0190a000-0000-7000-8000-000000000030";
const ORDER = "0190a000-0000-7000-8000-000000000020";

function invoice(patch: Partial<Invoice> = {}): Invoice {
  return {
    id: INVOICE,
    status: "POSTED",
    source: "MANUAL",
    vendorId: VENDOR,
    legalEntityId: ENTITY,
    invoiceNumber: "INV-1",
    invoiceDate: "2026-10-26",
    postingDate: "2026-10-27",
    currency: "USD",
    ledgerId: ENTITY,
    fiscalPeriodId: ENTITY,
    total: "15.00",
    ledgerEntryId: LEDGER,
    createdAt: "2026-10-26T10:00:00Z",
    lines: [{ lineNo: 1, receiptId: RECEIPT, receiptLineNo: 1, qty: "6", unitPrice: "2.5", amount: "15.00", standardCost: false }],
    ...patch,
  };
}

const receipt = {
  id: RECEIPT,
  receiptNumber: "RCV-00000007",
  state: "POSTED",
  version: 3,
  vendorId: VENDOR,
  purchaseOrderId: ORDER,
  legalEntityId: ENTITY,
  packingSlip: "PS-1",
  carrier: "",
  receiptDate: "2026-10-20",
  currency: "USD",
  exchangeRate: "1",
  createdAt: "2026-10-20T10:00:00Z",
  lines: [],
};

function api(invoices: (url: URL) => unknown) {
  server.handler = (url) => {
    if (url.pathname === "/api/v1/receiving/invoices") return json(200, invoices(url));
    if (url.pathname.startsWith("/api/v1/receiving/invoices/")) {
      const found = (invoices(url) as { items: Invoice[] }).items.find((i) => url.pathname.endsWith(`/${i.id}`));
      return found ? json(200, found) : json(404, { code: "RCV-NF-005", detail: "vendor invoice not found" });
    }
    if (url.pathname === `/api/v1/receiving/receipts/${RECEIPT}`) return json(200, receipt);
    return undefined;
  };
}

const invoiceCalls = () => server.calls.filter((c) => c.url.pathname === "/api/v1/receiving/invoices");

describe("vendor invoice list", () => {
  it("shows invoices with the vendor name, status in words and amount, and pages on request", async () => {
    api((url) =>
      url.searchParams.get("cursor") === "next"
        ? { items: [invoice({ id: "i2", invoiceNumber: "INV-2", status: "AUDIT_BLOCKED", ledgerEntryId: "" })] }
        : { items: [invoice()], nextCursor: "next" },
    );
    const { container } = renderAt("/ap-invoices");

    expect(await screen.findByRole("link", { name: "INV-1" })).toHaveAttribute("href", `/ap-invoices/${INVOICE}`);
    expect(await screen.findByText("Acme Supply (V-100)")).toBeInTheDocument();
    expect(screen.getByText("15.00 USD")).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Vendor invoices" });
    expect(within(table).getByText("Posted")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Load more invoices" }));
    expect(await screen.findByRole("link", { name: "INV-2" })).toBeInTheDocument();
    expect(within(table).getByText("Held for audit")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more invoices" })).toBeNull();
  });

  it("filters by status and sends no vendor filter from typed text", async () => {
    api(() => ({ items: [invoice()] }));
    renderAt("/ap-invoices");
    await screen.findByRole("link", { name: "INV-1" });

    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: "nope" } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "AUDIT_REJECTED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));

    await waitFor(() => expect(invoiceCalls().at(-1)?.url.searchParams.get("status")).toBe("AUDIT_REJECTED"));
    expect(invoiceCalls().at(-1)?.url.searchParams.has("vendorId")).toBe(false);
  });

  it("says nothing matches when filters are active and clears them", async () => {
    api((url) => (url.searchParams.get("status") === "AUDIT_REJECTED" ? { items: [] } : { items: [invoice()] }));
    renderAt("/ap-invoices");
    await screen.findByRole("link", { name: "INV-1" });

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "AUDIT_REJECTED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("No vendor invoices match these filters")).toBeInTheDocument();
    expect(screen.queryByText("No vendor invoices")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(await screen.findByRole("link", { name: "INV-1" })).toBeInTheDocument();
    expect(screen.getByLabelText("Status")).toHaveValue("");
  });

  it("shows a loading state before the first page arrives", () => {
    api(() => ({ items: [] }));
    renderAt("/ap-invoices");
    expect(screen.getByRole("table", { name: "Vendor invoices" })).toHaveAttribute("aria-busy", "true");
  });

  it("offers a next step when there are no invoices", async () => {
    api(() => ({ items: [] }));
    const { container } = renderAt("/ap-invoices");
    expect(await screen.findByText("No vendor invoices")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "See goods waiting for an invoice" })).toHaveAttribute("href", "/ap-invoices/grir");
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a failure and retries", async () => {
    let attempts = 0;
    server.handler = () => {
      attempts += 1;
      return attempts === 1 ? json(500, { code: "COM-0500", detail: "boom" }) : json(200, { items: [invoice()] });
    };
    renderAt("/ap-invoices");
    expect(await screen.findByText("Could not load the vendor invoices")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("link", { name: "INV-1" })).toBeInTheDocument();
  });

  it("tells a user without the duty they cannot view invoices and offers no retry", async () => {
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty" });
    renderAt("/ap-invoices");
    expect(await screen.findByText("You can't view vendor invoices")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
  });
});

describe("vendor invoice detail", () => {
  it("shows the lines, names, ledger entry and links to the receipt and purchase order", async () => {
    api(() => ({ items: [invoice()] }));
    const { container } = renderAt(`/ap-invoices/${INVOICE}`);

    expect(await screen.findByRole("heading", { name: "Invoice INV-1" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "RCV-00000007" })).toHaveAttribute("href", `/receiving/receipts/${RECEIPT}`);
    expect(await screen.findByRole("link", { name: "View the purchase order" })).toHaveAttribute("href", `/purchasing/purchase-orders/${ORDER}`);
    expect(screen.getByText("Acme Supply (V-100)")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Journal entry 0190a000" })).toHaveAttribute("href", `/ledger/entries/${LEDGER}`);
    expect(screen.getByText("Posted")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Invoice lines" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("reads the invoice with one call", async () => {
    api(() => ({ items: [invoice()] }));
    renderAt(`/ap-invoices/${INVOICE}`);
    expect(await screen.findByRole("heading", { name: "Invoice INV-1" })).toBeInTheDocument();
    expect(server.calls.filter((c) => c.url.pathname.startsWith("/api/v1/receiving/invoices"))).toHaveLength(1);
  });

  it("shows references as plain text, with no receipt fetch, when the user cannot open those pages", async () => {
    api(() => ({ items: [invoice()] }));
    renderAt(`/ap-invoices/${INVOICE}`, ["receiving.ap.inquire"]);

    expect(await screen.findByText("Receipt 0190a000")).toBeInTheDocument();
    expect(screen.getByText("Journal entry 0190a000")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Receipt|Journal entry|purchase order/i })).toBeNull();
    expect(server.calls.some((c) => c.url.pathname.startsWith("/api/v1/receiving/receipts"))).toBe(false);
  });

  it("keeps the receipt text plain when the receipt fetch is refused", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/receiving/invoices") return json(200, { items: [invoice()] });
      if (url.pathname === `/api/v1/receiving/invoices/${INVOICE}`) return json(200, invoice());
      return json(403, { code: "AUTH-0003", detail: "missing duty" });
    };
    renderAt(`/ap-invoices/${INVOICE}`);
    expect(await screen.findByRole("heading", { name: "Invoice INV-1" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Receipt 0190a000" })).toBeInTheDocument();
  });

  it("explains a hold and says no ledger entry exists yet", async () => {
    api(() => ({ items: [invoice({ status: "AUDIT_BLOCKED", ledgerEntryId: "", auditReason: "stochastic sample" })] }));
    renderAt(`/ap-invoices/${INVOICE}`);
    expect(await screen.findByText(/Nothing is posted to the ledger until an auditor releases it/)).toBeInTheDocument();
    expect(screen.getByText(/Reason: stochastic sample/)).toBeInTheDocument();
    expect(screen.getByText(/None yet/)).toBeInTheDocument();
  });

  it("explains a rejection", async () => {
    api(() => ({ items: [invoice({ status: "AUDIT_REJECTED", rejectReason: "Duplicate of INV-0", rejectedBy: "ana", rejectedAt: "2026-10-28T09:00:00Z" })] }));
    renderAt(`/ap-invoices/${INVOICE}`);
    expect(await screen.findByText(/Duplicate of INV-0/)).toBeInTheDocument();
    expect(screen.getByText(/Rejected by another user on 2026-10-28/)).toBeInTheDocument();
    expect(screen.queryByText(/ana/)).toBeNull();
  });

  it("shows a loading state", () => {
    api(() => ({ items: [] }));
    renderAt(`/ap-invoices/${INVOICE}`);
    expect(screen.getByText("Loading the invoice…")).toBeInTheDocument();
  });

  it("says when the invoice does not exist, without a retry", async () => {
    api(() => ({ items: [invoice({ id: "other" })] }));
    renderAt(`/ap-invoices/${INVOICE}`);
    expect(await screen.findByText("Invoice not found")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
  });

  it("tells a user without the duty and offers a retry on other failures", async () => {
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty" });
    const { unmount } = renderAt(`/ap-invoices/${INVOICE}`);
    expect(await screen.findByText("You can't view vendor invoices")).toBeInTheDocument();
    unmount();

    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderAt(`/ap-invoices/${INVOICE}`);
    expect(await screen.findByText("Could not load the invoice")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
