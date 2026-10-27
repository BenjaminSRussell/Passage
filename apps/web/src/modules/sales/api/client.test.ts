import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { atpCheck, confirmOrder, createOrder, describeProblem, getOrder, listCreditProfiles, listOrders, putCreditProfile, releaseHold, SalesApiError } from "./client";

const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

describe("sales API client", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  const call = (i: number) => {
    const [url, init] = fetchMock.mock.calls[i] as [string, RequestInit];
    return { url, init, headers: new Headers(init.headers) };
  };

  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("posts the ATP check to the browser path with decimals as strings", async () => {
    fetchMock.mockResolvedValueOnce(json({ promise: { promisedDate: "2027-03-04", complete: true, backorder: "0", source: "ATP", schedule: [] }, periods: [], cumulative: [] }));
    const res = await atpCheck({ requested: "2027-03-01", quantity: "45", completeOnly: true, periods: [{ date: "2027-03-01", supply: "100", demand: "60" }] });
    expect(res.promise.promisedDate).toBe("2027-03-04");
    const { url, init } = call(0);
    expect(url).toBe("/api/v1/sales/atp:check");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toMatchObject({ quantity: "45", periods: [{ supply: "100" }] });
  });

  it("lists orders with only the filters that are set", async () => {
    fetchMock.mockResolvedValueOnce(json({ items: [] }));
    await listOrders({ status: "OPEN", hold: "", cursor: "c1" });
    expect(call(0).url).toBe("/api/v1/sales/orders?status=OPEN&cursor=c1");
  });

  it("reads the order and its ETag", async () => {
    fetchMock.mockResolvedValueOnce(json({ id: "o1", orderNumber: "SO-0000001", version: 3, lines: [], holds: [] }, 200, { ETag: '"3"' }));
    const { value, etag } = await getOrder("o1");
    expect(value.orderNumber).toBe("SO-0000001");
    expect(etag).toBe('"3"');
  });

  it("sends the Idempotency-Key on create and If-Match on confirm and release", async () => {
    fetchMock.mockImplementation(async () => json({ id: "o1", version: 1, lines: [], holds: [] }, 201, { ETag: '"1"' }));
    await createOrder(
      { legalEntityId: "le", customerId: "c", invoiceAccountId: "c", orderType: "ORDER", currency: "USD", requestedShipDate: "2027-03-04", lines: [] },
      "key-1",
    );
    await confirmOrder("o1", '"1"');
    await releaseHold("o1", "h1", '"2"');
    expect(call(0).headers.get("Idempotency-Key")).toBe("key-1");
    expect(call(1).url).toBe("/api/v1/sales/orders/o1:confirm");
    expect(call(1).headers.get("If-Match")).toBe('"1"');
    expect(call(2).url).toBe("/api/v1/sales/orders/o1/holds/h1:release");
    expect(call(2).headers.get("If-Match")).toBe('"2"');
  });

  it("scopes credit profile calls to the legal entity", async () => {
    fetchMock.mockResolvedValueOnce(json({ items: [{ customerId: "c1" }] }));
    fetchMock.mockResolvedValueOnce(json({ customerId: "c1", version: 1 }, 200, { ETag: '"1"' }));
    expect(await listCreditProfiles("le-1")).toHaveLength(1);
    await putCreditProfile(
      "le-1",
      "c1",
      { limit: "10000", currency: "USD", limitType: "BALANCE", tolerancePct: "5", toleranceAmount: "1000", oldestItemDaysMax: 30, maxDunningLevel: 3, mandatoryCheck: false, exposure: {} },
      '"0"',
    );
    expect(call(0).url).toBe("/api/v1/sales/credit-profiles?legalEntityId=le-1&limit=200");
    expect(call(1).url).toBe("/api/v1/sales/credit-profiles/c1?legalEntityId=le-1");
    expect(call(1).init.method).toBe("PUT");
    expect(call(1).headers.get("If-Match")).toBe('"0"');
  });

  it("turns a problem response into a SalesApiError with its code and field errors", async () => {
    fetchMock.mockResolvedValueOnce(json({ status: 422, code: "COM-0002", detail: "request failed validation", errors: { quantity: "must be a decimal" } }, 422));
    const failure = await atpCheck({ requested: "2027-03-01", quantity: "x", completeOnly: true, periods: [] }).catch((e: unknown) => e);
    expect(failure).toBeInstanceOf(SalesApiError);
    expect(describeProblem(failure)).toEqual({ status: 422, code: "COM-0002", message: "request failed validation", fields: { quantity: "must be a decimal" } });
  });

  it("falls back to the HTTP status when the body is not a problem", async () => {
    fetchMock.mockResolvedValueOnce(new Response("gateway down", { status: 502 }));
    const failure = await getOrder("o1").catch((e: unknown) => e);
    expect(describeProblem(failure)).toMatchObject({ status: 502, code: "HTTP-502" });
  });
});
