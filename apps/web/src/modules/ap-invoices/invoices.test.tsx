import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { GrirItem, Invoice } from "./api/client";
import { validateFilters } from "./GrirPage";
import { subtractDecimal } from "./parts";
import { emptyInvoice, toInvoiceInput, validateInvoice } from "./PostInvoicePage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const UUID_A = "11111111-1111-4111-8111-111111111111";
const UUID_B = "22222222-2222-4222-8222-222222222222";
const UUID_C = "33333333-3333-4333-8333-333333333333";
const RECEIPT = "0190a000-0000-7000-8000-000000000010";

function item(patch: Partial<GrirItem> = {}): GrirItem {
  return {
    id: "g1",
    receiptId: RECEIPT,
    receiptNumber: "RCV-00000001",
    lineNo: 1,
    vendorId: UUID_A,
    poLineId: UUID_B,
    unitPrice: "2.5",
    receivedQty: "10",
    invoicedQty: "4",
    receivedAmount: "25.00",
    clearedAmount: "10.00",
    openAmount: "15.00",
    currency: "USD",
    status: "PARTIAL",
    ageDays: 3,
    createdAt: "2026-10-24T10:00:00Z",
    ...patch,
  };
}

function page(items: GrirItem[], nextCursor?: string) {
  return { items, totals: { count: items.length, openAmount: [{ amount: "15.00", currency: "USD" }] }, nextCursor };
}

function invoice(patch: Partial<Invoice> = {}): Invoice {
  return {
    id: UUID_C,
    vendorId: UUID_A,
    legalEntityId: UUID_B,
    invoiceNumber: "INV-1",
    invoiceDate: "2026-10-26",
    postingDate: "2026-10-26",
    currency: "USD",
    ledgerId: UUID_A,
    fiscalPeriodId: UUID_B,
    total: "15.00",
    ledgerEntryId: "je-9",
    createdAt: "2026-10-26T10:00:00Z",
    lines: [{ lineNo: 1, receiptId: RECEIPT, receiptLineNo: 1, qty: "6", unitPrice: "2.5", amount: "15.00", standardCost: false }],
    ...patch,
  };
}

describe("decimal helpers", () => {
  it("subtracts exactly and clamps at zero", () => {
    expect(subtractDecimal("10", "4")).toBe("6");
    expect(subtractDecimal("0.3", "0.1")).toBe("0.2");
    expect(subtractDecimal("1", "2")).toBe("0");
    expect(subtractDecimal("1.000001", "1")).toBe("0.000001");
  });
});

describe("client-side checks", () => {
  const ok = {
    ...emptyInvoice,
    vendorId: UUID_A,
    legalEntityId: UUID_B,
    invoiceNumber: " INV-1 ",
    invoiceDate: "2026-10-26",
    postingDate: "2026-10-26",
    ledgerId: UUID_A,
    lines: [{ receiptId: RECEIPT, receiptLineNo: "1", qty: "6", unitPrice: "2.5", standardCost: true }],
  };

  it("accepts a complete invoice, trims the number and sends decimals as strings", () => {
    expect(validateInvoice(ok)).toEqual({});
    expect(toInvoiceInput(ok)).toMatchObject({ invoiceNumber: "INV-1", lines: [{ receiptLineNo: 1, qty: "6", unitPrice: "2.5", standardCost: true }] });
  });

  it("rejects each bad field", () => {
    const bad = validateInvoice({ ...ok, vendorId: "x", invoiceNumber: " ", postingDate: "", currency: "usd", lines: [{ ...ok.lines[0], qty: "0", receiptLineNo: "0", unitPrice: "-1" }] });
    expect(Object.keys(bad).sort()).toEqual(["currency", "invoiceNumber", "lines[0].qty", "lines[0].receiptLineNo", "lines[0].unitPrice", "postingDate", "vendorId"]);
  });

  it("validates the GR/IR filters", () => {
    expect(validateFilters({ vendorId: "", status: "", ageGt: "" })).toEqual({});
    expect(Object.keys(validateFilters({ vendorId: "x", status: "", ageGt: "-1" })).sort()).toEqual(["ageGt", "vendorId"]);
  });
});

describe("GR/IR open items", () => {
  it("shows items, totals per currency, filters and pages", async () => {
    server.handler = (url) => {
      if (url.searchParams.get("cursor") === "next") return json(200, page([item({ id: "g2", receiptNumber: "RCV-00000002" })]));
      return json(200, page([item()], url.searchParams.get("status") === "OPEN" ? undefined : "next"));
    };
    const { container } = renderAt("/ap-invoices/grir");

    expect(await screen.findByText("RCV-00000001")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Open GR/IR totals" })).toHaveTextContent("Open USD 15.00");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Load more items" }));
    expect(await screen.findByText("RCV-00000002")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "OPEN" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("OPEN"));
  });

  it("refuses a bad filter without calling the service", async () => {
    server.handler = () => json(200, page([item()]));
    renderAt("/ap-invoices/grir");
    await screen.findByText("RCV-00000001");
    const calls = server.calls.length;
    fireEvent.change(screen.getByLabelText("Vendor ID"), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("Enter a vendor UUID.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(calls);
  });

  it("carries the selected items, with the receipt unit price, into the invoice form", async () => {
    server.handler = () => json(200, page([item({ unitPrice: "2.505" })]));
    renderAt("/ap-invoices/grir");
    expect(screen.queryByRole("button", { name: "Invoice selected items" })).toBeDisabled();
    fireEvent.click(await screen.findByRole("checkbox", { name: "Select RCV-00000001 line 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Invoice selected items" }));

    expect(await screen.findByRole("form", { name: "Vendor invoice" })).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent("/ap-invoices/post");
    expect(screen.getByLabelText("Vendor ID")).toHaveValue(UUID_A);
    expect(screen.getByLabelText("Line 1 receipt ID")).toHaveValue(RECEIPT);
    expect(screen.getByLabelText("Line 1 quantity")).toHaveValue("6");
    expect(screen.getByLabelText("Line 1 unit price")).toHaveValue("2.505");
  });

  it("will not mix vendors on one invoice and cannot select cleared items", async () => {
    server.handler = () => json(200, page([item(), item({ id: "g2", receiptNumber: "RCV-00000002", vendorId: UUID_B }), item({ id: "g3", receiptNumber: "RCV-00000003", status: "CLEARED" })]));
    renderAt("/ap-invoices/grir");
    fireEvent.click(await screen.findByRole("checkbox", { name: "Select RCV-00000001 line 1" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Select RCV-00000002 line 1" }));
    expect(screen.getByRole("checkbox", { name: "Select RCV-00000003 line 1" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Invoice selected items" }));
    expect(await screen.findByText(/one vendor and one currency/)).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Vendor invoice" })).not.toBeInTheDocument();
  });

  it("shows an empty state and a retryable error", async () => {
    server.handler = () => json(200, page([]));
    const { unmount } = renderAt("/ap-invoices/grir");
    expect(await screen.findByText("No GR/IR items")).toBeInTheDocument();
    unmount();

    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty" });
    renderAt("/ap-invoices/grir");
    expect(await screen.findByRole("alert")).toHaveTextContent("missing duty");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("post vendor invoice", () => {
  function fill(label: string, value: string) {
    fireEvent.change(screen.getByLabelText(label), { target: { value } });
  }

  function fillValid() {
    fill("Vendor ID", UUID_A);
    fill("Legal entity ID", UUID_B);
    fill("Invoice number", "INV-1");
    fill("Invoice date", "2026-10-26");
    fill("Posting date", "2026-10-26");
    fill("Ledger ID", UUID_A);
    fill("Line 1 receipt ID", RECEIPT);
    fill("Line 1 quantity", "6");
    fill("Line 1 unit price", "2.5");
  }

  it("blocks an empty form with field errors and sends nothing", async () => {
    const { container } = renderAt("/ap-invoices/post");
    fireEvent.click(screen.getByRole("button", { name: "Post invoice" }));
    expect(await screen.findAllByRole("alert")).not.toHaveLength(0);
    expect(screen.getByLabelText("Invoice number")).toBeInvalid();
    expect(screen.getByLabelText("Line 1 quantity")).toBeInvalid();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("posts with an Idempotency-Key and reports the result", async () => {
    server.handler = (_, init) => (init.method === "POST" ? json(201, invoice()) : undefined);
    const { container } = renderAt("/ap-invoices/post");
    fillValid();
    fireEvent.click(screen.getByRole("button", { name: "Post invoice" }));

    expect(await screen.findByText("Invoice INV-1 posted")).toBeInTheDocument();
    expect(screen.getByText("Total 15.00 USD across 1 line, ledger entry je-9.")).toBeInTheDocument();
    const [call] = server.calls;
    expect(call.url.pathname).toBe("/api/v1/receiving/invoices:post");
    expect(idempotencyKey(call)).toMatch(/^[0-9a-f-]{36}$/);
    expect(call.body).toMatchObject({ invoiceNumber: "INV-1", currency: "USD", lines: [{ receiptId: RECEIPT, receiptLineNo: 1, qty: "6", unitPrice: "2.5", standardCost: false }] });
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Post another invoice" }));
    expect(screen.getByLabelText("Invoice number")).toHaveValue("");
  });

  it("keeps the same key for a retry after a failure and shows the service's reason", async () => {
    let attempts = 0;
    server.handler = () => {
      attempts += 1;
      return attempts === 1 ? json(422, { code: "PER-AP-001", detail: "AP period for 2026-10-26 is closed" }) : json(201, invoice());
    };
    renderAt("/ap-invoices/post");
    fillValid();
    fireEvent.click(screen.getByRole("button", { name: "Post invoice" }));

    expect(await screen.findByText("AP period for 2026-10-26 is closed (PER-AP-001)")).toBeInTheDocument();
    expect(screen.getByLabelText("Invoice number")).toHaveValue("INV-1");
    fireEvent.click(screen.getByRole("button", { name: "Post invoice" }));
    await screen.findByText("Invoice INV-1 posted");
    expect(server.calls).toHaveLength(2);
    expect(idempotencyKey(server.calls[1])).toBe(idempotencyKey(server.calls[0]));
  });

  it("maps a service field error onto the field", async () => {
    server.handler = () => json(400, { code: "COM-0001", detail: "request failed validation", errors: { "lines[0].qty": "must be greater than zero" } });
    renderAt("/ap-invoices/post");
    fillValid();
    fireEvent.click(screen.getByRole("button", { name: "Post invoice" }));
    const field = await screen.findByLabelText("Line 1 quantity");
    await waitFor(() => expect(field).toBeInvalid());
    expect(within(field.closest("div") as HTMLElement).getByText("must be greater than zero")).toBeInTheDocument();
  });

  it("adds and removes lines with unique labels", () => {
    renderAt("/ap-invoices/post");
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(screen.getByLabelText("Line 2 quantity")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove line 2" }));
    expect(screen.queryByLabelText("Line 2 quantity")).not.toBeInTheDocument();
  });
});
