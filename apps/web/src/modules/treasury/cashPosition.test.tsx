import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BankAccount, CashPosition } from "./api/client";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const ACCT = "0190f0a0-0000-7000-8000-000000000101";
const ACCT2 = "0190f0a0-0000-7000-8000-000000000102";

const position: CashPosition = {
  asOf: "2026-10-22T15:00:00Z",
  staleHours: 24,
  accounts: [
    { bankAccountId: ACCT, accountCode: "EUR-OPS", ibanMasked: "DE** 3000", currency: "EUR", booked: "1250.5", available: "51250.5", source: "camt.052", asOf: "2026-10-22T14:00:00Z", stale: false },
    { bankAccountId: ACCT2, accountCode: "USD-OPS", ibanMasked: "US** 4821", currency: "USD", booked: "90", available: "90", source: "camt.053", asOf: "2026-10-11T00:00:00Z", stale: true },
  ],
  totals: [
    { currency: "EUR", booked: "1250.5", available: "51250.5" },
    { currency: "USD", booked: "90", available: "90" },
  ],
};
const accounts: BankAccount[] = [{ id: ACCT, legalEntityId: LE, accountCode: "EUR-OPS", ibanMasked: "DE** 3000", ibanLast4: "3000", currency: "EUR", mainAccountId: ACCT, status: "active", version: 1 }];

function serve() {
  server.handler = (url, init) => {
    if (url.pathname === "/api/v1/treasury/cash-position") return json(200, position);
    if (url.pathname === "/api/v1/treasury/bank-accounts") return json(200, { items: accounts });
    if (url.pathname === "/api/v1/treasury/intraday-reports/import" && init.method === "POST")
      return json(201, { id: "r1", bankAccountId: ACCT, reportId: "RPT-1", currency: "EUR", createdAtBank: "2026-10-22T14:00:00Z", balances: [] });
    return undefined;
  };
}

describe("cash position screen", () => {
  it("shows each account's freshest figure with its source and freshness, and totals per currency", async () => {
    serve();
    const { container } = renderAt(`/treasury/cash-position?legalEntityId=${LE}`);
    await screen.findByText("EUR-OPS (DE** 3000)");
    expect(screen.getByText("Intraday report")).toBeTruthy();
    expect(screen.getByText("Bank statement")).toBeTruthy();
    expect(screen.getByText("Out of date")).toBeTruthy();
    expect(screen.getByText("Current")).toBeTruthy();
    expect(screen.getAllByText("1250.5 EUR").length).toBeGreaterThan(0);
    expect(screen.getByRole("table", { name: "Totals by currency" })).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("asks for a legal entity first", async () => {
    serve();
    renderAt("/treasury/cash-position");
    await screen.findByText("Choose a legal entity", { selector: "h2, h3, p, div" });
    expect(server.calls.some((c) => c.url.pathname.endsWith("/cash-position"))).toBe(false);
  });

  it("uploads a camt.052 file for the chosen account", async () => {
    serve();
    renderAt(`/treasury/cash-position?legalEntityId=${LE}`);
    await screen.findByText("EUR-OPS (DE** 3000)");
    fireEvent.click(screen.getByRole("button", { name: "Import report" }));
    await screen.findByText("Choose the bank account the report is for.");
    fireEvent.change(await screen.findByLabelText("Bank account"), { target: { value: ACCT } });
    fireEvent.change(screen.getByLabelText("Report file", { exact: false }), { target: { files: [new File(["<xml/>"], "report.xml", { type: "text/xml" })] } });
    fireEvent.click(screen.getByRole("button", { name: "Import report" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/intraday-reports/import"))).toBe(true));
    const form = server.calls.find((c) => c.method === "POST")!.body as FormData;
    expect(form.get("bankAccountId")).toBe(ACCT);
    await screen.findByText("Report RPT-1 imported.");
  });
});
