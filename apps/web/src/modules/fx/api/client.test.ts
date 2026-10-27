import { describe, expect, it, vi } from "vitest";
import { convertAmounts, fetchHistory, FxApiError, lookupRate, type FetchLike } from "./client";

function reply(status: number, body: unknown) {
  return vi.fn<FetchLike>(async () => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
}

describe("fx client", () => {
  it("sends the lookup filters as query parameters and omits empty optionals", async () => {
    const fetchImpl = reply(200, { rateType: "CLOSING", from: "EUR", to: "USD", date: "2026-01-02", path: "direct", rate: "1.25", hops: [] });
    await lookupRate({ rateType: "CLOSING", from: "EUR", to: "USD" }, fetchImpl);
    expect(fetchImpl.mock.calls[0][0]).toBe("/api/v1/fx/rates?type=CLOSING&from=EUR&to=USD");
  });

  it("rejects a response that does not match the contract", async () => {
    await expect(fetchHistory({ rateType: "CLOSING", from: "EUR", to: "USD" }, reply(200, { entries: [{ id: 1 }] }))).rejects.toThrow();
  });

  it("raises the problem code and field errors for a 400", async () => {
    const fetchImpl = reply(400, { code: "COM-0001", detail: "request failed validation", errors: { "amounts[0].value": "bad" } });
    const error = await convertAmounts({ rateType: "CLOSING", to: "USD", amounts: [{ value: "x", currency: "EUR" }] }, fetchImpl).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(FxApiError);
    expect(error).toMatchObject({ status: 400, code: "COM-0001", fields: { "amounts[0].value": "bad" } });
  });

  it("falls back to a generic message for a non-JSON error body", async () => {
    const fetchImpl: FetchLike = async () => new Response("<html>", { status: 502 });
    await expect(lookupRate({ rateType: "CLOSING", from: "EUR", to: "USD" }, fetchImpl)).rejects.toMatchObject({ code: "COM-0500", message: "request failed with status 502" });
  });
});
