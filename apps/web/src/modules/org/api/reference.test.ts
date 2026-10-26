import { afterEach, describe, expect, it, vi } from "vitest";
import { OrgApiError } from "./client";
import {
  convertUnit,
  createConversion,
  createUnit,
  listCountries,
  listRegions,
  listUnits,
  setReferenceStatus,
  setUnitStatus,
} from "./reference";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

function stubFetch(reply: () => Response = () => json({ items: [] })) {
  const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => reply());
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const headersOf = (fetchMock: ReturnType<typeof stubFetch>, call = 0) => new Headers(fetchMock.mock.calls[call][1]?.headers);

afterEach(() => vi.unstubAllGlobals());

describe("referenceFetch", () => {
  it("sends no legal-entity header on reads or writes", async () => {
    const fetchMock = stubFetch(() => json({ code: "US", name: "United States", status: "inactive" }));
    await listCountries();
    await setReferenceStatus("countries", "US", "inactive");
    await createUnit({ code: "box", name: "Box", class: "each", decimalPrecision: 0 });
    for (let call = 0; call < 3; call += 1) {
      const names = [...headersOf(fetchMock, call).keys()];
      expect(names.filter((name) => name.startsWith("x-passage-legalentity"))).toEqual([]);
    }
  });

  it("adds Idempotency-Key to POST only", async () => {
    vi.stubGlobal("crypto", { randomUUID: () => "key-1" });
    const fetchMock = stubFetch(() => json({}));
    await listUnits();
    await setUnitStatus("kg", "inactive");
    await createConversion({ fromCode: "kg", toCode: "g", factor: "1000" });
    expect(headersOf(fetchMock, 0).has("Idempotency-Key")).toBe(false);
    expect(headersOf(fetchMock, 1).has("Idempotency-Key")).toBe(false);
    expect(headersOf(fetchMock, 2).get("Idempotency-Key")).toBe("key-1");
    expect(fetchMock.mock.calls[2][1]?.method).toBe("POST");
  });

  it("builds status and country query strings and drops empty filters", async () => {
    const fetchMock = stubFetch();
    await listCountries("inactive");
    await listCountries();
    await listRegions("active", { country: "US" });
    await listUnits({ class: "mass", status: "active" });
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/org/countries?status=inactive",
      "/api/v1/org/countries",
      "/api/v1/org/regions?status=active&country=US",
      "/api/v1/org/uom/units?class=mass&status=active",
    ]);
  });

  it("URL-encodes unit codes and sends the status body", async () => {
    const fetchMock = stubFetch(() => json({}));
    await setUnitStatus("m/s", "inactive");
    await setUnitStatus("%", "active");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/org/uom/units/m%2Fs");
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/org/uom/units/%25");
    expect(fetchMock.mock.calls[0][1]?.body).toBe(JSON.stringify({ status: "inactive" }));
  });

  it("passes the quantity through as the original string", async () => {
    const fetchMock = stubFetch(() => json({ from: "kg", to: "g", quantity: "1000.000000000001", displayQuantity: "1000" }));
    const result = await convertUnit("kg", "g", "1.000000000001");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/org/uom/convert?from=kg&to=g&qty=1.000000000001");
    expect(result.quantity).toBe("1000.000000000001");
  });

  it("maps problem+json to OrgApiError with its code", async () => {
    stubFetch(() => json({ code: "ORG-UOM-NO-PATH", detail: "no path", status: 422 }, 422));
    await expect(convertUnit("kg", "m", "1")).rejects.toMatchObject({ status: 422, code: "ORG-UOM-NO-PATH", message: "no path" });
  });

  it("falls back to COM-0001 for a non-problem body", async () => {
    stubFetch(() => new Response("boom", { status: 502 }));
    const failure = await listCountries().catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(OrgApiError);
    expect(failure).toMatchObject({ status: 502, code: "COM-0001" });
  });

  it("treats a missing items array as an empty list", async () => {
    stubFetch(() => json({}));
    expect(await listCountries()).toEqual([]);
  });
});
