import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BankStatementBatch, Deduction, Payment } from "./api/client";
import { validateStatement } from "./BankStatements";
import { needsApproval, parseParts, toDeductionInput, validateDeduction, validateMatch } from "./Deductions";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-0000000000c1";
const ITEM = "0190f0a0-0000-7000-8000-000000000401";
const PAY = "0190f0a0-0000-7000-8000-000000000301";
const DED = "0190f0a0-0000-7000-8000-000000000d01";

const payment: Payment = {
  id: PAY,
  legalEntityId: LE,
  customerId: null,
  paymentMode: "WIRE",
  source: "CAMT053",
  sourceRef: "STMT-0327:E2E-1",
  amount: "2380.00",
  unappliedAmount: "2380.00",
  currency: "EUR",
  receivedDate: "2027-03-20",
  valueDate: "2027-03-20",
  payerName: "Acme GmbH",
  remittance: [{ reference: "INV-2001", amount: "2380.00" }],
  status: "RECEIVED",
  version: 1,
};

const statement: BankStatementBatch = {
  id: "0190f0a0-0000-7000-8000-000000000e01",
  legalEntityId: LE,
  fileSha256: "b".repeat(64),
  statementCount: 1,
  paymentCount: 1,
  duplicateCount: 2,
  skippedCount: 3,
  importedAt: "2027-03-21T08:00:00Z",
};

const deduction: Deduction = {
  id: DED,
  legalEntityId: LE,
  customerId: CUST,
  openItemId: ITEM,
  documentNumber: "INV-30001",
  paymentId: PAY,
  type: "PROMO",
  reasonCode: "promo spring",
  currency: "USD",
  amount: "300.00",
  status: "OPEN",
  createdAt: "2027-03-08T09:00:00Z",
  version: 1,
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

function lastCall(suffix: string) {
  return [...server.calls].reverse().find((c) => c.url.pathname.endsWith(suffix));
}

describe("Bank statements", () => {
  it("lists imported statements with their counts", async () => {
    server.handler = (url) => (url.pathname.endsWith("/bank-statements/imports") ? json(200, { items: [statement] }) : undefined);
    const { container } = renderAt("/receivables/bank-statements");
    const table = await findTable({ name: "Bank statement imports" });
    await waitFor(() => expect(table).toHaveTextContent("bbbbbbbbbbbb"));
    const row = within(table).getAllByRole("row")[1];
    expect(row).toHaveTextContent("1");
    expect(row).toHaveTextContent("2");
    expect(row).toHaveTextContent("3");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/receivables/bank-statements");
    expect(await screen.findByText("No statements")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty receivables.view" });
    renderAt("/receivables/bank-statements");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the bank statement imports");
    expect(alert).toHaveTextContent("AUTH-0003");
  });

  it("checks the form, imports the XML and lists the payments created", async () => {
    server.handler = (url, init) =>
      url.pathname.endsWith("/bank-statements/imports") && init.method === "POST" ? json(201, { ...statement, payments: [payment] }) : undefined;
    const { container } = renderAt("/receivables/bank-statements/new");
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    expect(await screen.findByText("Choose a legal entity.")).toBeInTheDocument();
    expect(screen.getByText("Choose the camt.053 file or paste its XML.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    fill("Legal entity", LE);
    fill("Statement XML", "<Document/>");
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    const status = await findStatus();
    expect(status).toHaveTextContent("1 payment(s) from 1 statement(s); 2 already imported, 3 entries not customer credits");
    expect(lastCall("/bank-statements/imports")!.body).toEqual({ legalEntityId: LE, content: "<Document/>" });
    const created = screen.getByRole("table", { name: "Payments created" });
    expect(created).toHaveTextContent("STMT-0327:E2E-1");
    expect(created).toHaveTextContent("Acme GmbH");
    expect(created).toHaveTextContent("INV-2001");
    expect(pageViolations(container)).toEqual([]);
  });

  it("names a duplicate file and shows a malformed line", async () => {
    server.handler = () => json(409, { code: "AR-0003", detail: "File already imported (bbbb)" });
    const first = renderAt("/receivables/bank-statements/new");
    fill("Legal entity", LE);
    fill("Statement XML", "<Document/>");
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    expect(await findAlert()).toHaveTextContent("This file was already imported");
    first.unmount();
    server.handler = () => json(400, { code: "AR-0004", detail: "Line 12: Amt must be a positive decimal" });
    renderAt("/receivables/bank-statements/new");
    fill("Legal entity", LE);
    fill("Statement XML", "<Document/>");
    fireEvent.click(screen.getByRole("button", { name: "Import statement" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not import the statement");
    expect(alert).toHaveTextContent("Line 12");
  });

  it("validates the statement draft", () => {
    expect(validateStatement({ legalEntityId: LE, content: "<x/>" })).toEqual({});
    expect(Object.keys(validateStatement({ legalEntityId: "le", content: " " })).sort()).toEqual(["content", "legalEntityId"]);
  });
});

describe("Deductions", () => {
  it("lists deductions with their age and claim, filters and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/deductions")) return json(200, { items: [deduction, { ...deduction, id: "d2", status: "MATCHED", claimRef: "FUND-7" }] });
      if (url.pathname.endsWith(`/deductions/${DED}`)) return json(200, deduction);
      return undefined;
    };
    const { container } = renderAt("/receivables/deductions");
    const table = await findTable({ name: "Deductions" });
    await waitFor(() => expect(table).toHaveTextContent("300.00 USD"));
    expect(table).toHaveTextContent("Promotion");
    expect(table).toHaveTextContent("FUND-7");
    expect(table).toHaveTextContent("Matched");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "MATCHED" } });
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("status") === "MATCHED")).toBe(true));
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "PRICE" } });
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("type") === "PRICE")).toBe(true));
    fireEvent.click(within(await findTable({ name: "Deductions" })).getAllByRole("button", { name: "INV-30001" })[0]);
    expect(await screen.findByRole("heading", { name: "Deduction 300.00 USD" })).toBeInTheDocument();
  });

  it("shows the empty and error states", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/receivables/deductions");
    expect(await screen.findByText("No deductions")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "database unavailable" });
    renderAt("/receivables/deductions");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the deductions");
    expect(alert).toHaveTextContent("COM-0500");
  });

  it("records a deduction from an open item with an Idempotency-Key", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/deductions") && init.method === "POST") return json(201, deduction);
      if (url.pathname.endsWith(`/deductions/${DED}`)) return json(200, deduction);
      return undefined;
    };
    const { container } = renderAt(`/receivables/deductions/new?legalEntity=${LE}&openItem=${ITEM}&amount=300.00`);
    expect(screen.getByLabelText("Open item")).toHaveAttribute("data-value", ITEM);
    fill("Amount", "300.01");
    fireEvent.click(screen.getByRole("button", { name: "Record deduction" }));
    expect(await screen.findByText("At most 300.00 is open on the item.")).toBeInTheDocument();
    expect(screen.getByText("Enter the customer's reason, up to 60 characters.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fill("Amount", "300.00");
    fill("Customer's reason", " promo spring ");
    fill("Payment", PAY);
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "PROMO" } });
    fireEvent.click(screen.getByRole("button", { name: "Record deduction" }));
    expect(await screen.findByRole("heading", { name: "Deduction 300.00 USD" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ legalEntityId: LE, openItemId: ITEM, paymentId: PAY, type: "PROMO", reasonCode: "promo spring", amount: "300.00" });
    expect(idempotencyKey(post)).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("shows a refused deduction with its code and keeps the field errors", async () => {
    server.handler = () => json(409, { code: "AR-DISPUTE-EXISTS", detail: "open item INV-30001 has an active dispute" });
    renderAt(`/receivables/deductions/new?legalEntity=${LE}&openItem=${ITEM}&amount=300.00`);
    fill("Customer's reason", "promo");
    fireEvent.click(screen.getByRole("button", { name: "Record deduction" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not record the deduction");
    expect(alert).toHaveTextContent("AR-DISPUTE-EXISTS");
  });

  it("matches an open deduction with If-Match and refuses a claim that does not cover it", async () => {
    let current: Deduction = deduction;
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/deductions/${DED}:match`)) {
        current = { ...deduction, status: "MATCHED", claimType: "PROMOTION_FUND", claimRef: "FUND-7", claimRemaining: "300.00", version: 2 };
        return json(200, current);
      }
      if (url.pathname.endsWith(`/deductions/${DED}`) && (init.method ?? "GET") === "GET") return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/receivables/deductions/${DED}`);
    expect(await screen.findByRole("heading", { name: "Deduction 300.00 USD" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fill("Claim reference", "FUND-7");
    fill("Claim remaining", "299.99");
    fireEvent.click(screen.getByRole("button", { name: "Match" }));
    expect(await screen.findByText("The claim does not cover 300.00; split the deduction first.")).toBeInTheDocument();
    expect(lastCall(":match")).toBeUndefined();

    fill("Claim remaining", "300.00");
    fireEvent.click(screen.getByRole("button", { name: "Match" }));
    expect(await findStatus()).toHaveTextContent("Matched to the claim");
    const call = lastCall(":match")!;
    expect(new Headers(call.init.headers).get("If-Match")).toBe('"1"');
    expect(call.body).toEqual({ claimType: "PROMOTION_FUND", claimRef: "FUND-7", claimRemaining: "300.00" });
    expect(await screen.findByRole("button", { name: "Approve" })).toBeInTheDocument();
  });

  it("approves a matched deduction with the note and warns when it changed", async () => {
    const matched: Deduction = { ...deduction, status: "MATCHED", claimType: "PROMOTION_FUND", claimRef: "FUND-7", claimRemaining: "300.00", version: 2 };
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":approve")) return json(412, { code: "conflict_version", detail: "the deduction changed since it was read" });
      if ((init.method ?? "GET") === "GET") return json(200, matched);
      return undefined;
    };
    renderAt(`/receivables/deductions/${DED}`);
    expect(await screen.findByText("Promotion fund FUND-7, 300.00 USD remaining")).toBeInTheDocument();
    fill("Decision note", "fund confirmed");
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(await findAlert()).toHaveTextContent("The deduction changed since you opened it");
    const call = lastCall(":approve")!;
    expect(new Headers(call.init.headers).get("If-Match")).toBe('"2"');
    expect(call.body).toEqual({ note: "fund confirmed" });
  });

  it("denies with the reason, splits with parts that sum, and flags a write-off that needs approval", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":deny")) return json(200, { ...deduction, status: "DENIED", denialReason: "EXPIRED", rebillItemId: ITEM, version: 2 });
      if (url.pathname.endsWith(":split")) return json(200, { ...deduction, status: "SPLIT", version: 2 });
      if (url.pathname.endsWith(":write-off")) return json(403, { code: "AR-0006", detail: "Write-off 300.00 USD exceeds the deduction limit 50.00" });
      if ((init.method ?? "GET") === "GET") return json(200, deduction);
      return undefined;
    };
    renderAt(`/receivables/deductions/${DED}`);
    expect(await screen.findByText(/above the deduction limit; it needs the receivables.write-off.approve duty/)).toBeInTheDocument();

    fill("Part amounts", "200.00, 99.99");
    fireEvent.click(screen.getByRole("button", { name: "Split" }));
    expect(await screen.findByText("The parts sum to 299.99; they must sum to 300.00.")).toBeInTheDocument();
    fill("Part amounts", "200.00, 100.00");
    fireEvent.click(screen.getByRole("button", { name: "Split" }));
    expect(await findStatus()).toHaveTextContent("Split.");
    expect(lastCall(":split")!.body).toEqual({ parts: ["200.00", "100.00"] });

    fireEvent.click(await screen.findByRole("button", { name: "Write off" }));
    expect(await findAlert()).toHaveTextContent("AR-0006");

    fireEvent.change(screen.getByLabelText("Denial reason"), { target: { value: "EXPIRED" } });
    fireEvent.click(screen.getByRole("button", { name: "Deny and re-bill" }));
    await waitFor(() => expect(lastCall(":deny")).toBeDefined());
    expect(lastCall(":deny")!.body).toEqual({ reason: "EXPIRED" });
    expect(await findStatus()).toHaveTextContent("re-billed");
  });

  it("shows a split deduction's parts and no workbench actions", async () => {
    const child: Deduction = { ...deduction, id: "0190f0a0-0000-7000-8000-000000000d02", parentId: DED, amount: "100.00" };
    server.handler = () => json(200, { ...deduction, status: "SPLIT", resolvedAt: "2027-03-09T10:00:00Z", children: [child] });
    const { container } = renderAt(`/receivables/deductions/${DED}`);
    const parts = await findTable({ name: "Parts" });
    expect(parts).toHaveTextContent("100.00 USD");
    expect(screen.queryByRole("button", { name: "Write off" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Match" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the detail load failure with a retry", async () => {
    server.handler = () => json(404, { code: "AR-DEDUCTION-NOT-FOUND", detail: "deduction not found" });
    renderAt(`/receivables/deductions/${DED}`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("AR-DEDUCTION-NOT-FOUND");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("deduction rules in the browser", () => {
  const draft = { legalEntityId: LE, openItemId: ITEM, paymentId: "", type: "PRICE" as const, reasonCode: "price diff", amount: "12.50", openAmount: "" };

  it("validates a draft and maps it to the request", () => {
    expect(validateDeduction(draft)).toEqual({});
    expect(Object.keys(validateDeduction({ ...draft, openItemId: "x", amount: "0", paymentId: "p" })).sort()).toEqual(["amount", "openItemId", "paymentId"]);
    expect(toDeductionInput({ ...draft, reasonCode: "  price diff " })).toEqual({
      legalEntityId: LE,
      openItemId: ITEM,
      paymentId: null,
      type: "PRICE",
      reasonCode: "price diff",
      amount: "12.50",
    });
  });

  it("checks the claim covers the whole deduction", () => {
    expect(validateMatch("FUND-7", "300", "300.00")).toEqual({});
    expect(Object.keys(validateMatch(" ", "abc", "300.00")).sort()).toEqual(["claimRef", "claimRemaining"]);
  });

  it("parses split parts exactly, without floats", () => {
    expect(parseParts("0.10, 0.20", "0.30")).toEqual({ parts: ["0.10", "0.20"] });
    expect(parseParts("300.00", "300.00").error).toMatch(/2 to 20/);
    expect(parseParts("300.00, 0", "300.00").error).toMatch(/above zero/);
  });

  it("knows when a write-off needs receivables.write-off.approve", () => {
    expect(needsApproval("50.00", "USD")).toBe(false);
    expect(needsApproval("50.01", "USD")).toBe(true);
    expect(needsApproval("45.01", "EUR")).toBe(true);
    expect(needsApproval("0.01", "GBP")).toBe(true);
  });
});
