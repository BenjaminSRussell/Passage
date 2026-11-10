import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BankAccount, BankReconciliation, StatementLine } from "./api/client";
import { header, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const ACCT = "0190f0a0-0000-7000-8000-000000000101";
const ST = "0190f0a0-0000-7000-8000-000000000201";
const RC = "0190f0a0-0000-7000-8000-000000000701";

const account: BankAccount = { id: ACCT, legalEntityId: LE, accountCode: "OPS-USD", ibanMasked: "US** 4821", ibanLast4: "4821", currency: "USD", mainAccountId: ACCT, status: "active", version: 1 };

function recon(patch: Partial<BankReconciliation> = {}): BankReconciliation {
  return {
    id: RC,
    legalEntityId: LE,
    bankAccountId: ACCT,
    statementId: ST,
    currency: "USD",
    periodFrom: "2026-10-01",
    periodTo: "2026-10-31",
    state: "open",
    openingBalance: "100.00",
    closingBalance: "160.00",
    bookBalance: "100.00",
    outstandingAmount: "0.00",
    difference: "60.00",
    summary: { lines: 2, matchedCount: 1, matchedTotal: "10.00", excludedCount: 0, excludedTotal: "0.00", unmatchedCount: 1, unmatchedTotal: "50.00" },
    version: 3,
    ...patch,
  };
}

const lines: StatementLine[] = [
  { lineNo: 1, bookingDate: "2026-10-05", valueDate: "2026-10-05", amount: "10.00", currency: "USD", status: "matched", counterpartyName: "Acme" },
  { lineNo: 2, bookingDate: "2026-10-06", valueDate: "2026-10-06", amount: "-5.00", currency: "USD", status: "unmatched", counterpartyName: "Bank fee" },
];

function serve(current: () => BankReconciliation) {
  server.handler = (url, init) => {
    const p = url.pathname;
    if (p === `/api/v1/treasury/bank-reconciliations/${RC}`) return json(200, current());
    if (p === `/api/v1/treasury/bank-accounts/${ACCT}`) return json(200, account);
    if (p === `/api/v1/treasury/statements/${ST}/lines`) return json(200, { items: lines, nextAfterLineNo: null });
    if (p === "/api/v1/treasury/bank-accounts") return json(200, { items: [account] });
    if (p === "/api/v1/treasury/bank-reconciliations" && init.method === undefined) return json(200, { items: [current()] });
    if (p.includes(":")) return json(200, current());
    return undefined;
  };
}

describe("reconciliation screens", () => {
  it("lists reconciliations with the account code instead of ids", async () => {
    serve(() => recon());
    renderAt(`/treasury/reconciliations?legalEntityId=${LE}`);
    await screen.findByText("2026-10-01 to 2026-10-31");
    expect(screen.getByText("OPS-USD (US** 4821, USD)", { selector: "td" })).toBeTruthy();
    expect(screen.queryByText(RC)).toBeNull();
  });

  it("shows the difference and keeps Close disabled until it is zero", async () => {
    serve(() => recon());
    const { container } = renderAt(`/treasury/reconciliations/${RC}`);
    await screen.findByText("Summary");
    expect(screen.getAllByText("60.00 USD").length).toBeGreaterThan(0);
    expect((screen.getByRole("button", { name: "Close the period" }) as HTMLButtonElement).disabled).toBe(true);
    expect(pageViolations(container)).toEqual([]);
  });

  it("closes the period with the version in If-Match", async () => {
    serve(() => recon({ difference: "0.00", summary: { ...recon().summary, unmatchedCount: 0 } }));
    renderAt(`/treasury/reconciliations/${RC}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close the period" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith(":close"))).toBe(true));
    const call = server.calls.find((c) => c.url.pathname.endsWith(":close"))!;
    expect(header(call, "If-Match")).toBe('"3"');
  });

  it("books a bank fee from an unmatched line", async () => {
    serve(() => recon());
    renderAt(`/treasury/reconciliations/${RC}`);
    fireEvent.click(await screen.findByRole("button", { name: "Act on line 2" }));
    fireEvent.click(await screen.findByRole("button", { name: "Book as bank fee" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith("/lines/2:create-transaction"))).toBe(true));
    expect(server.calls.find((c) => c.url.pathname.endsWith(":create-transaction"))!.body).toEqual({ kind: "bank_fee" });
  });

  it("offers reopen with a reason on a closed period", async () => {
    serve(() => recon({ state: "closed", difference: "0.00", closedBy: "marcus.lee", closedAt: "2026-11-01T10:00:00Z" }));
    renderAt(`/treasury/reconciliations/${RC}`);
    await screen.findByRole("heading", { name: "Reopen the period" });
    expect((screen.getByRole("button", { name: "Reopen the period" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "Late bank fee" } });
    fireEvent.click(screen.getByRole("button", { name: "Reopen the period" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith(":reopen"))).toBe(true));
    expect(server.calls.find((c) => c.url.pathname.endsWith(":reopen"))!.body).toEqual({ reason: "Late bank fee" });
  });
});
