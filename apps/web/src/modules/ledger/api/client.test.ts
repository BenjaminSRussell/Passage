import { describe, expect, it, vi } from "vitest";
import { fetchEntry, fetchTrialBalance, LedgerApiError, type FetchLike } from "./client";

function jsonResponse(status: number, body: unknown): Response {
  return { ok: status >= 200 && status < 300, status, json: async () => body } as unknown as Response;
}

const balance = {
  ledgerId: "0190a000-0000-7000-8000-000000000001",
  currency: "USD",
  rows: [{ account: "1000", name: "Cash", debit: "100.00", credit: "0.00", net: "100.00", lineCount: 1 }],
  debit: "100.00",
  credit: "100.00",
  net: "0.00",
  balanced: true,
};

describe("fetchTrialBalance", () => {
  it("sends snake_case query parameters for the balances endpoint", async () => {
    const fetchImpl = vi.fn<FetchLike>(async () => jsonResponse(200, balance));
    const out = await fetchTrialBalance(
      { ledgerId: balance.ledgerId, periodFrom: "2026-01", periodTo: "2026-03", accountFrom: "1000" },
      fetchImpl,
    );
    expect(out.balanced).toBe(true);
    const url = fetchImpl.mock.calls[0][0];
    expect(url).toBe(
      `/api/v1/ledger/balances?ledger=${balance.ledgerId}&period_from=2026-01&period_to=2026-03&account_from=1000`,
    );
  });

  it("maps a 422 rule rejection to LedgerApiError with its violations", async () => {
    const fetchImpl: FetchLike = async () =>
      jsonResponse(422, {
        code: "LEDGER_UNBALANCED",
        detail: "entry does not balance",
        violations: [{ code: "LEDGER_UNBALANCED", message: "debits 100, credits 99.99", field: "lines" }],
      });
    const err = await fetchTrialBalance({ ledgerId: balance.ledgerId }, fetchImpl).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(LedgerApiError);
    expect((err as LedgerApiError).status).toBe(422);
    expect((err as LedgerApiError).violations[0].code).toBe("LEDGER_UNBALANCED");
  });

  it("falls back to COM-0500 when the error body is not JSON", async () => {
    const fetchImpl: FetchLike = async () =>
      ({ ok: false, status: 502, json: async () => { throw new SyntaxError("not json"); } }) as unknown as Response;
    const err = (await fetchTrialBalance({ ledgerId: balance.ledgerId }, fetchImpl).catch((e: unknown) => e)) as LedgerApiError;
    expect(err.code).toBe("COM-0500");
    expect(err.status).toBe(502);
  });

  it("rejects a response that does not match the contract", async () => {
    const fetchImpl: FetchLike = async () => jsonResponse(200, { ledgerId: 7 });
    await expect(fetchTrialBalance({ ledgerId: balance.ledgerId }, fetchImpl)).rejects.toThrow();
  });
});

describe("fetchEntry", () => {
  it("reads one entry by id and parses its lines", async () => {
    const entry = {
      id: "0190a000-0000-7000-8000-0000000000aa",
      ledgerId: balance.ledgerId,
      voucher: "GL-000001",
      postingSequence: 1,
      postingDate: "2026-03-31",
      periodKey: "2026-03",
      traceId: "0123456789abcdef0123456789abcdef",
      totals: { currency: "USD", debit: "5.00", credit: "5.00" },
      lines: [
        { id: "l1", lineNumber: 1, account: "1000", displayName: "1000", debitCredit: "D", amount: "5.00" },
      ],
    };
    const fetchImpl = vi.fn<FetchLike>(async () => jsonResponse(200, entry));
    const out = await fetchEntry(entry.id, fetchImpl);
    expect(out.voucher).toBe("GL-000001");
    expect(fetchImpl.mock.calls[0][0]).toBe(`/api/v1/ledger/journal-entries/${entry.id}`);
  });
});
