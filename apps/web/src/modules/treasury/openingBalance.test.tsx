import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BankAccount, BankReconciliation, CashPosition, Statement } from "./api/client";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const ACCT = "0190f0a0-0000-7000-8000-000000000101";
const STMT = "0190f0a0-0000-7000-8000-000000000201";
const RECON = "0190f0a0-0000-7000-8000-000000000301";
const accounts: BankAccount[] = [{ id: ACCT, legalEntityId: LE, accountCode: "USD-OPS", ibanMasked: "US** 4821", ibanLast4: "4821", currency: "USD", mainAccountId: ACCT, status: "active", version: 1 }];

describe("the migrated opening balance (#49)", () => {
  it("shows a cash position that starts from the opening balance", async () => {
    const position: CashPosition = {
      asOf: "2026-12-31T18:00:00Z",
      staleHours: 24,
      accounts: [{ bankAccountId: ACCT, accountCode: "USD-OPS", ibanMasked: "US** 4821", currency: "USD", booked: "12500.75", available: "12500.75", source: "opening", asOf: "2027-01-01T00:00:00Z", stale: false }],
      totals: [{ currency: "USD", booked: "12500.75", available: "12500.75" }],
    };
    server.handler = (url) => {
      if (url.pathname === "/api/v1/treasury/cash-position") return json(200, position);
      if (url.pathname === "/api/v1/treasury/bank-accounts") return json(200, { items: accounts });
      return undefined;
    };
    const { container } = renderAt(`/treasury/cash-position?legalEntityId=${LE}`);
    expect(await screen.findByText("Opening balance from the migration")).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("opens the first reconciliation from the opening balance without a stated book balance", async () => {
    const statement: Statement = { id: STMT, bankAccountId: ACCT, legalEntityId: LE, statementId: "ST-0001", format: "camt.053", currency: "USD", openingDate: "2027-01-01", openingBalance: "12500.75", closingDate: "2027-01-31", closingBalance: "13100.00", lineCount: 3, createdAt: "2027-02-01T08:00:00Z" };
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/treasury/bank-accounts") return json(200, { items: accounts });
      if (url.pathname === `/api/v1/treasury/bank-accounts/${ACCT}/statements`) return json(200, { items: [statement] });
      if (url.pathname === "/api/v1/treasury/bank-reconciliations" && init.method === "POST") return json(201, { id: RECON } as BankReconciliation);
      return undefined;
    };
    renderAt(`/treasury/reconciliations/new?legalEntityId=${LE}`);
    fireEvent.change(await screen.findByLabelText("Bank account"), { target: { value: ACCT } });
    await screen.findByRole("option", { name: /ST-0001/ });
    fireEvent.change(screen.getByLabelText("Statement"), { target: { value: STMT } });
    fireEvent.change(screen.getByLabelText("Book balance from"), { target: { value: "opening" } });
    expect(screen.queryByLabelText("Book balance")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Open reconciliation" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST")).toBe(true));
    expect(server.calls.find((c) => c.method === "POST")!.body).toEqual({ statementId: STMT, bookBalanceSource: "opening" });
  });
});
