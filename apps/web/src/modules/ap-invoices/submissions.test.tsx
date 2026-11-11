import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { rememberOption } from "../../shell/lookup/entities";
import type { Invoice, Submission } from "./api/client";
import { postState } from "./SubmissionDetailPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const VENDOR = "11111111-1111-4111-8111-111111111111";
const ENTITY = "22222222-2222-4222-8222-222222222222";
const ID = "44444444-4444-4444-8444-444444444444";
const INVOICE = "33333333-3333-4333-8333-333333333333";
const RECEIPT = "0190a000-0000-7000-8000-000000000010";
const DUTIES = ["receiving.receipt.maintain", "receiving.ap.inquire", "receiving.vendor-invoice.post"];

beforeEach(() => {
  rememberOption("vendor", { id: VENDOR, name: "Acme Supply", number: "V-100" });
});

function submission(patch: Partial<Submission> = {}): Submission {
  return {
    id: ID,
    legalEntityId: ENTITY,
    vendorId: VENDOR,
    invoiceNumber: "AC-9001",
    invoiceDate: "2026-11-09",
    currency: "USD",
    total: "150.00",
    status: "SUBMITTED",
    note: "Partial delivery",
    lines: [{ receiptId: RECEIPT, receiptLineNo: 1, qty: "10", unitPrice: "15", amount: "150.00" }],
    createdAt: "2026-11-10T08:00:00Z",
    ...patch,
  };
}

const posted = { id: INVOICE, invoiceNumber: "AC-9001", total: "150.00", currency: "USD", invoiceDate: "2026-11-09" } as Invoice;

describe("invoice submissions", () => {
  it("lists submissions with the vendor name and status in words, and opens one", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/receiving/invoice-submissions" ? json(200, { items: [submission(), submission({ id: "s2", invoiceNumber: "AC-8000", status: "REJECTED" })] }) : undefined);
    const { container } = renderAt("/ap-invoices/submissions", DUTIES);
    const table = await screen.findByRole("table", { name: "Invoice submissions" });
    expect(await within(table).findByText("AC-9001")).toBeInTheDocument();
    expect(await within(table).findAllByText("Acme Supply (V-100)")).toHaveLength(2);
    expect(within(table).getByText("Waiting for review")).toBeInTheDocument();
    expect(within(table).getByText("Rejected")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("fills the post form from the submission", () => {
    expect(postState(submission())).toEqual({
      header: { legalEntityId: ENTITY, invoiceNumber: "AC-9001", invoiceDate: "2026-11-09" },
      items: [{ vendorId: VENDOR, currency: "USD", receiptId: RECEIPT, receiptLineNo: 1, qty: "10", unitPrice: "15" }],
    });
  });

  it("records the posted invoice with an idempotency key", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/receiving/invoice-submissions/${ID}:resolve` && init.method === "POST") return json(200, submission({ status: "POSTED", invoiceId: INVOICE }));
      if (url.pathname === `/api/v1/receiving/invoice-submissions/${ID}`) return json(200, submission());
      if (url.pathname === "/api/v1/receiving/invoices") return json(200, { items: [{ ...posted, lines: [], status: "POSTED", vendorId: VENDOR, legalEntityId: ENTITY, postingDate: "2026-11-09", ledgerId: "l", fiscalPeriodId: "p", ledgerEntryId: "e", createdAt: "2026-11-09T00:00:00Z" }] });
      if (url.pathname === `/api/v1/receiving/receipts/${RECEIPT}`) return json(200, { id: RECEIPT, receiptNumber: "RCV-000007", state: "POSTED", version: 1, vendorId: VENDOR, purchaseOrderId: "p", legalEntityId: ENTITY, packingSlip: "PS", carrier: "", receiptDate: "2026-11-01", currency: "USD", exchangeRate: "1", createdAt: "2026-11-01T00:00:00Z", lines: [] });
      return undefined;
    };
    const { container } = renderAt(`/ap-invoices/submissions/${ID}`, DUTIES);
    expect(await screen.findByRole("heading", { name: "Invoice submission AC-9001" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "RCV-000007" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Post as vendor invoice" })).toHaveAttribute("href", "/ap-invoices/post");
    fireEvent.click(screen.getByRole("button", { name: "Mark as posted" }));
    expect(await screen.findByText(/Choose the vendor invoice/)).toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText("Posted vendor invoice"), { target: { value: INVOICE } });
    fireEvent.click(screen.getByRole("button", { name: "Mark as posted" }));
    expect(await screen.findByText(/Recorded. The vendor sees the submission as posted/)).toBeInTheDocument();
    const call = server.calls.find((c) => c.init.method === "POST");
    expect(call?.body).toEqual({ invoiceId: INVOICE });
    expect(call && idempotencyKey(call)).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("rejects with a reason after confirming, and hides the actions without the post duty", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/receiving/invoice-submissions/${ID}:reject` && init.method === "POST") return json(200, submission({ status: "REJECTED", decisionNote: "Wrong price" }));
      if (url.pathname === `/api/v1/receiving/invoice-submissions/${ID}`) return json(200, submission());
      if (url.pathname === "/api/v1/receiving/invoices") return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/ap-invoices/submissions/${ID}`, DUTIES);
    fireEvent.change(await screen.findByLabelText("Reason"), { target: { value: "Wrong price" } });
    fireEvent.click(screen.getByRole("button", { name: "Reject submission" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm rejection" }));
    expect(await screen.findByText(/Rejected. The vendor sees your reason/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("Wrong price")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Reject submission" })).toBeNull();
  });

  it("explains that deciding needs the post duty", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/receiving/invoice-submissions/${ID}` ? json(200, submission()) : undefined);
    renderAt(`/ap-invoices/submissions/${ID}`, ["receiving.ap.inquire"]);
    expect(await screen.findByText(/needs the receiving.vendor-invoice.post duty/)).toBeInTheDocument();
  });
});
