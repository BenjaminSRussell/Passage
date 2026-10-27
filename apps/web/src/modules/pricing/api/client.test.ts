import { afterEach, describe, expect, it, vi } from "vitest";
import { activateConditionRecord, describeProblem, listConditionRecords, PricingApiError, resolvePrice, withdrawConditionRecord } from "./client";

const LE = "0190f0a0-0000-7000-8000-000000000001";
const body = {
  legalEntityId: LE,
  customerId: "C1",
  currency: "USD",
  pricingDate: "2027-03-10",
  lines: [{ productId: "BOLT", unit: "EA", quantity: "3" }],
};

function reply(status: number, json: unknown) {
  // A fresh Response per call: a body can be read only once.
  return vi.fn().mockImplementation(async () => new Response(JSON.stringify(json), { status, headers: { "Content-Type": "application/json" } }));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("pricing api client", () => {
  it("posts the resolve request to /api/v1/pricing/resolve with explain=true", async () => {
    const fetchMock = reply(200, { orderNet: "256.50" });
    vi.stubGlobal("fetch", fetchMock);
    const out = await resolvePrice(body);
    expect(out.orderNet).toBe("256.50");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/pricing/resolve?explain=true");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Content-Type")).toBe("application/json");
    expect(JSON.parse(init.body as string)).toEqual(body);
  });

  it("maps a problem response to PricingApiError with the stable code and field errors", async () => {
    vi.stubGlobal("fetch", reply(422, { code: "COM-0001", detail: "request is invalid", errors: { "lines[0].quantity": "must be a decimal greater than 0" } }));
    const err = await resolvePrice(body).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(PricingApiError);
    expect(describeProblem(err)).toEqual({ code: "COM-0001", message: "request is invalid", fields: { "lines[0].quantity": "must be a decimal greater than 0" } });
  });

  it("falls back to the HTTP status when the error body is not a problem", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("upstream down", { status: 502 })));
    const err = (await listConditionRecords().catch((e: unknown) => e)) as PricingApiError;
    expect(err.code).toBe("HTTP-502");
    expect(err.status).toBe(502);
  });

  it("sends only the filters that are set when listing condition records", async () => {
    const fetchMock = reply(200, { items: [] });
    vi.stubGlobal("fetch", fetchMock);
    await expect(listConditionRecords({ legalEntityId: LE, status: "ACTIVE", type: "", customer: "C1", limit: 50 })).resolves.toEqual([]);
    const [url] = fetchMock.mock.calls[0] as [string];
    expect(url).toBe(`/api/v1/pricing/condition-records?legalEntityId=${LE}&status=ACTIVE&customer=C1&limit=50`);
  });

  it("rejects a list response without items", async () => {
    vi.stubGlobal("fetch", reply(200, {}));
    await expect(listConditionRecords()).rejects.toThrow("The response has no items");
  });

  it("sends the version and the Idempotency-Key on activate and withdraw", async () => {
    const fetchMock = reply(200, { id: "r1" });
    vi.stubGlobal("fetch", fetchMock);
    await activateConditionRecord("r1", 1, "key-a");
    await withdrawConditionRecord("r1", 2, "superseded", "key-w");
    const [[activateUrl, activate], [withdrawUrl, withdraw]] = fetchMock.mock.calls as [string, RequestInit][];
    expect(activateUrl).toBe("/api/v1/pricing/condition-records/r1:activate");
    expect(new Headers(activate.headers).get("Idempotency-Key")).toBe("key-a");
    expect(JSON.parse(activate.body as string)).toEqual({ version: 1 });
    expect(withdrawUrl).toBe("/api/v1/pricing/condition-records/r1:withdraw");
    expect(new Headers(withdraw.headers).get("Idempotency-Key")).toBe("key-w");
    expect(JSON.parse(withdraw.body as string)).toEqual({ version: 2, reason: "superseded" });
  });
});
