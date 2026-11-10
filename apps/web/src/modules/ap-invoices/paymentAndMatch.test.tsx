import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { rememberOption } from "../../shell/lookup/entities";
import type { OpenItem } from "../../shell/api/openItems";
import type { Capture, Invoice } from "./api/client";
import { ALL_DUTIES, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

beforeEach(() => {
  rememberOption("vendor", { id: VENDOR, name: "Acme Supply", number: "V-100" });
});

const VENDOR = "11111111-1111-4111-8111-111111111111";
const ENTITY = "22222222-2222-4222-8222-222222222222";
const INVOICE = "33333333-3333-4333-8333-333333333333";
const RECEIPT = "0190a000-0000-7000-8000-000000000010";
const PROPOSAL = "0190a000-0000-7000-8000-000000000040";
const DUTIES = [...ALL_DUTIES, "open-item.read", "payment-proposal.read"];
const FUTURE = "2999-01-01";

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
    ledgerEntryId: "",
    createdAt: "2026-10-26T10:00:00Z",
    lines: [{ lineNo: 1, receiptId: RECEIPT, receiptLineNo: 1, qty: "6", unitPrice: "2.5", amount: "15.00", standardCost: false }],
    ...patch,
  };
}

function openItem(patch: Partial<OpenItem> = {}): OpenItem {
  return {
    id: "44444444-4444-4444-8444-444444444444",
    legalEntityId: ENTITY,
    vendorId: VENDOR,
    sourceInvoiceId: INVOICE,
    invoiceNumber: "INV-1",
    invoiceDate: "2026-10-26",
    dueDate: FUTURE,
    dueDateSource: "EVENT",
    currency: "USD",
    amount: "15.00",
    openAmount: "15.00",
    status: "OPEN",
    paymentHold: false,
    ...patch,
  };
}

function capture(patch: Partial<Capture> = {}): Capture {
  return {
    id: "55555555-5555-4555-8555-555555555555",
    legalEntityId: ENTITY,
    status: "NEEDS_REVIEW",
    invoiceNumber: "CAP-7",
    vendorTaxId: "TX-99",
    invoiceDate: "2026-10-20",
    currency: "USD",
    total: "80.00",
    reviewReasons: ["TOTAL_MISMATCH"],
    createdAt: "2026-10-20T10:00:00Z",
    ...patch,
  };
}

interface Backend {
  invoices?: Invoice[];
  openItems?: OpenItem[] | number;
  captures?: Capture[] | number;
}

function backend({ invoices = [invoice()], openItems = [], captures = [] }: Backend = {}) {
  server.handler = (url) => {
    if (url.pathname === "/api/v1/receiving/invoices") return json(200, { items: invoices });
    if (url.pathname.startsWith("/api/v1/receiving/invoices/")) {
      const found = invoices.find((i) => url.pathname.endsWith(`/${i.id}`));
      return found ? json(200, found) : json(404, { code: "RCV-NF-005", detail: "not found" });
    }
    if (url.pathname === "/api/v1/receiving/invoice-captures") {
      const wanted = url.searchParams.get("status");
      return typeof captures === "number" ? json(captures, { code: "COM-0500", detail: "boom" }) : json(200, { items: captures.filter((c) => !wanted || c.status === wanted) });
    }
    if (url.pathname === "/api/v1/payables/open-items") return typeof openItems === "number" ? json(openItems, { code: "COM-0500", detail: "boom" }) : json(200, { items: openItems });
    if (url.pathname === `/api/v1/receiving/receipts/${RECEIPT}`) return json(404, { code: "RCV-NF-001", detail: "n/a" });
    return undefined;
  };
}

const openItemCalls = () => server.calls.filter((c) => c.url.pathname === "/api/v1/payables/open-items");

describe("due date on the invoice list", () => {
  it("shows the due date from the payables open item and asks only for the listed invoices", async () => {
    backend({ openItems: [openItem({ dueDate: "2026-12-01" })] });
    const { container } = renderAt("/ap-invoices", DUTIES);
    const table = await screen.findByRole("table", { name: "Vendor invoices" });
    expect(within(table).getByRole("columnheader", { name: "Due date" })).toBeInTheDocument();
    expect(await within(table).findByText("2026-12-01")).toBeInTheDocument();
    expect(openItemCalls()[0].url.searchParams.get("source_invoices")).toBe(INVOICE);
    expect(openItemCalls()[0].url.searchParams.get("legal_entity")).toBe(ENTITY);
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when the date is only the invoice date because no payment terms exist", async () => {
    backend({ openItems: [openItem({ dueDate: "2026-10-26", dueDateSource: "DEFAULTED" })] });
    renderAt("/ap-invoices", DUTIES);
    expect(await screen.findByText("2026-10-26 (no payment terms yet)")).toBeInTheDocument();
  });

  it("says when a posted invoice has no open item yet", async () => {
    backend({ openItems: [] });
    renderAt("/ap-invoices", DUTIES);
    expect(await screen.findByText("Not scheduled yet")).toBeInTheDocument();
  });

  it("shows no date and makes no payables call for a user without the open item duty", async () => {
    backend({ openItems: [openItem()] });
    renderAt("/ap-invoices", ALL_DUTIES);
    expect(await screen.findByText("Not available to you")).toBeInTheDocument();
    expect(openItemCalls()).toHaveLength(0);
  });

  it("shows the same wording when payables refuses the read", async () => {
    backend({ openItems: 403 });
    renderAt("/ap-invoices", DUTIES);
    expect(await screen.findByText("Not available to you")).toBeInTheDocument();
  });

  it("keeps the invoices and says the date could not be loaded when payables fails", async () => {
    backend({ openItems: 500 });
    renderAt("/ap-invoices", DUTIES);
    expect(await screen.findByRole("link", { name: "INV-1" })).toBeInTheDocument();
    expect(await screen.findByText("Could not load")).toBeInTheDocument();
  });
});

describe("captured invoices on the list", () => {
  it("shows captured and needs-review documents with the reasons in words", async () => {
    backend({ captures: [capture(), capture({ id: "66666666-6666-4666-8666-666666666666", status: "CAPTURED", invoiceNumber: "CAP-8", vendorTaxId: "", reviewReasons: [] })] });
    const { container } = renderAt("/ap-invoices", DUTIES);
    const table = await screen.findByRole("table", { name: "Captured vendor invoices" });
    expect(within(table).getByText("CAP-7")).toBeInTheDocument();
    expect(within(table).getByText("Needs review")).toBeInTheDocument();
    expect(within(table).getByText("The total differs from the sum of the lines")).toBeInTheDocument();
    expect(within(table).getByText("Tax number TX-99")).toBeInTheDocument();
    expect(within(table).getByText("Unknown vendor")).toBeInTheDocument();
    expect(within(table).getByText("Captured")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters to needs review and hides the posted invoices", async () => {
    backend({ captures: [capture(), capture({ id: "66666666-6666-4666-8666-666666666666", status: "CAPTURED", invoiceNumber: "CAP-8", reviewReasons: [] })] });
    renderAt("/ap-invoices", DUTIES);
    await screen.findByRole("table", { name: "Captured vendor invoices" });

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "NEEDS_REVIEW" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));

    await waitFor(() => expect(screen.queryByRole("table", { name: "Vendor invoices" })).toBeNull());
    const table = await screen.findByRole("table", { name: "Captured vendor invoices" });
    await waitFor(() => expect(within(table).queryByText("CAP-8")).toBeNull());
    expect(within(table).getByText("CAP-7")).toBeInTheDocument();
  });

  it("says nothing matches when the captured filter finds none", async () => {
    backend({ captures: [] });
    renderAt("/ap-invoices", DUTIES);
    await screen.findByRole("link", { name: "INV-1" });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "CAPTURED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("No captured invoices match this filter")).toBeInTheDocument();
  });

  it("omits the section when there are none", async () => {
    backend({ captures: [] });
    renderAt("/ap-invoices", DUTIES);
    await screen.findByRole("link", { name: "INV-1" });
    expect(screen.queryByText("Captured, not yet posted")).toBeNull();
  });

  it("warns, keeps the posted invoices and retries when the captures fail", async () => {
    backend({ captures: 500 });
    renderAt("/ap-invoices", DUTIES);
    expect(await screen.findByRole("link", { name: "INV-1" })).toBeInTheDocument();
    expect(await screen.findByText("Could not load the captured invoices")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try loading captured invoices again" })).toBeInTheDocument();
  });

  it("shows no warning when the user may not read captures", async () => {
    backend({ captures: 403 });
    renderAt("/ap-invoices", DUTIES);
    expect(await screen.findByRole("link", { name: "INV-1" })).toBeInTheDocument();
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === "/api/v1/receiving/invoice-captures")).toBe(true));
    expect(screen.queryByText("Could not load the captured invoices")).toBeNull();
  });
});

describe("match on the invoice detail", () => {
  const priced = (status: "MATCHED" | "PRICE_VARIANCE", amount: string, withinTolerance: boolean) => ({
    status,
    withinTolerance,
    lines: [{ lineNo: 1, status, receivedQty: "6", priceVariance: { amount, currency: "USD" } }],
  });

  it("says the invoice matches within the standard tolerances", async () => {
    backend({ invoices: [invoice({ match: priced("MATCHED", "0.00", true) })] });
    renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    const card = (await screen.findByRole("heading", { name: "Match with the receipt" })).closest("section, div")!.parentElement!;
    expect(within(card).getAllByText("Matches the receipt").length).toBeGreaterThan(0);
    expect(within(card).getByText(/Every line is within the standard tolerances/)).toBeInTheDocument();
    expect(within(card).getByText("None")).toBeInTheDocument();
  });

  it("shows the price difference in money and plain words when outside the tolerance", async () => {
    backend({ invoices: [invoice({ match: priced("PRICE_VARIANCE", "1500.00", false) })] });
    const { container } = renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    expect(await screen.findByText("1500.00 USD above the receipt price")).toBeInTheDocument();
    expect(screen.getAllByText("Price is outside the standard tolerance").length).toBeGreaterThan(0);
    expect(screen.getByText(/At least one line is outside the standard tolerances/)).toBeInTheDocument();
    expect(screen.queryByText(/PRICE_VARIANCE|score|rule/i)).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("leaves the card out when the invoice carries no match", async () => {
    backend({ invoices: [invoice()] });
    renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    await screen.findByRole("heading", { name: "Invoice INV-1" });
    expect(screen.queryByRole("heading", { name: "Match with the receipt" })).toBeNull();
  });
});

describe("payment status on the invoice detail", () => {
  const paymentCard = async () => (await screen.findByRole("heading", { name: "Payment status" })).parentElement!.parentElement!;

  it("says not due yet and links to the open items", async () => {
    backend({ openItems: [openItem()] });
    const { container } = renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    expect(await screen.findByText("Not due yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "See it among the vendor open items" })).toHaveAttribute("href", "/payables/open-items");
    expect(pageViolations(container)).toEqual([]);
  });

  it("says due when the due date has passed", async () => {
    backend({ openItems: [openItem({ dueDate: "2020-01-01" })] });
    renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    expect(await screen.findByText("Due")).toBeInTheDocument();
  });

  it("says paid for a settled item", async () => {
    backend({ openItems: [openItem({ status: "SETTLED", openAmount: "0.00" })] });
    renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    expect(await screen.findByText("Paid")).toBeInTheDocument();
  });

  it("says on hold and gives the reason", async () => {
    backend({ openItems: [openItem({ paymentHold: true, paymentHoldReason: "Disputed quantity" })] });
    renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    expect(await screen.findByText("On hold")).toBeInTheDocument();
    expect(screen.getByText("On hold: Disputed quantity")).toBeInTheDocument();
  });

  it("links to the payment proposal when the user can read proposals", async () => {
    backend({ openItems: [openItem({ proposalId: PROPOSAL })] });
    renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    expect(await screen.findByText("In a payment proposal")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "View the payment proposal" })).toHaveAttribute("href", `/payables/proposals/${PROPOSAL}`);
  });

  it("names the proposal without a link when the user cannot read proposals", async () => {
    backend({ openItems: [openItem({ proposalId: PROPOSAL })] });
    renderAt(`/ap-invoices/${INVOICE}`, [...ALL_DUTIES, "open-item.read"]);
    expect(await screen.findByText("Included in a payment proposal")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "View the payment proposal" })).toBeNull();
  });

  it("says payment status is not available, with no payables call, without the open item duty", async () => {
    backend({ openItems: [openItem()] });
    renderAt(`/ap-invoices/${INVOICE}`, ALL_DUTIES);
    expect(await screen.findByText("Payment status is not available to you.")).toBeInTheDocument();
    expect(openItemCalls()).toHaveLength(0);
  });

  it("says the same when payables refuses the read", async () => {
    backend({ openItems: 403 });
    renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    expect(await screen.findByText("Payment status is not available to you.")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("explains a failure, keeps the invoice and retries", async () => {
    let failing = true;
    backend({ openItems: [openItem()] });
    const ok = server.handler!;
    server.handler = (url, init) => (url.pathname === "/api/v1/payables/open-items" && failing ? json(500, { code: "COM-0500", detail: "boom" }) : ok(url, init));
    renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    expect(await screen.findByText("Could not load the payment status")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Invoice INV-1" })).toBeInTheDocument();
    failing = false;
    fireEvent.click(screen.getByRole("button", { name: "Try loading the payment status again" }));
    expect(await screen.findByText("Not due yet")).toBeInTheDocument();
  });

  it("says no payment is scheduled when the invoice has no open item", async () => {
    backend({ openItems: [] });
    renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    const card = await paymentCard();
    expect(await within(card).findByText(/No payment is scheduled yet/)).toBeInTheDocument();
  });

  it("says nothing is payable while the invoice is held for audit", async () => {
    backend({ invoices: [invoice({ status: "AUDIT_BLOCKED" })], openItems: [] });
    renderAt(`/ap-invoices/${INVOICE}`, DUTIES);
    expect(await screen.findByText("Nothing is payable until the invoice is released from audit.")).toBeInTheDocument();
  });
});
