import { describe, expect, it, vi } from "vitest";
import { convertAmounts, createRateType, fetchHistory, FxApiError, lookupRate, publishRate, putRateType, type FetchLike } from "./client";

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

  it("puts a rate type at its encoded code", async () => {
    const fetchImpl = reply(200, { id: "t1", code: "A/B", name: "Closing", allowInverse: true });
    await putRateType("A/B", { name: "Closing", allowInverse: true }, fetchImpl);
    expect(fetchImpl.mock.calls[0][0]).toBe("/api/v1/fx/rate-types/A%2FB");
    expect(fetchImpl.mock.calls[0][1]).toMatchObject({ method: "PUT", body: JSON.stringify({ name: "Closing", allowInverse: true }) });
  });

  it("creates a rate type with a POST carrying its code", async () => {
    const fetchImpl = reply(201, { id: "t1", code: "CLOSING", name: "Closing", allowInverse: true });
    await createRateType("CLOSING", { name: "Closing", allowInverse: true }, fetchImpl);
    expect(fetchImpl.mock.calls[0][0]).toBe("/api/v1/fx/rate-types");
    expect(fetchImpl.mock.calls[0][1]).toMatchObject({ method: "POST", body: JSON.stringify({ code: "CLOSING", name: "Closing", allowInverse: true }) });
  });

  it("publishes one rate with the idempotency key", async () => {
    const fetchImpl = reply(200, { rates: [{ id: "r1", version: 1, superseded: false, unchanged: false }] });
    const input = { rateType: "CLOSING", from: "EUR", to: "USD", validFrom: "2026-02-01", rate: "1.08" };
    await publishRate(input, "key-1", fetchImpl);
    expect(fetchImpl.mock.calls[0][1]).toMatchObject({ method: "POST", headers: { "Idempotency-Key": "key-1" }, body: JSON.stringify({ rates: [input] }) });
  });

  it("falls back to a generic message for a non-JSON error body", async () => {
    const fetchImpl: FetchLike = async () => new Response("<html>", { status: 502 });
    await expect(lookupRate({ rateType: "CLOSING", from: "EUR", to: "USD" }, fetchImpl)).rejects.toMatchObject({ code: "COM-0500", message: "request failed with status 502" });
  });
});
