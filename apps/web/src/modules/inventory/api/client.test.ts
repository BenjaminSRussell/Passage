import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { addLines, createJournal, fetchOnHand, InventoryApiError, listJournals, onHandUrl, postJournal } from "./client";

type Call = [string, RequestInit];

function stubFetch(status: number, body: unknown, headers: Record<string, string> = {}) {
  const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function call(mock: ReturnType<typeof stubFetch>, i = 0): Call {
  return mock.mock.calls[i] as unknown as Call;
}

beforeEach(() => {
  let n = 0;
  vi.stubGlobal("crypto", { randomUUID: () => `key-${++n}` });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("inventory client", () => {
  it("builds the on-hand URL with grouping and only the filters that are set", () => {
    expect(onHandUrl({ groupBy: ["product", "batch"], site: " S1 ", batch: "" })).toBe("/onhand?groupBy=product%2Cbatch&site=S1");
    expect(onHandUrl({ groupBy: [] })).toBe("/onhand");
  });

  it("reads on-hand rows and the truncation flag", async () => {
    const fetchMock = stubFetch(200, { items: [{ group: { site: "S1" }, physicalInvent: "3", negative: true }], truncated: true });
    const result = await fetchOnHand({ groupBy: ["site"] });
    expect(call(fetchMock)[0]).toBe("/api/v1/inventory/onhand?groupBy=site");
    expect(result.truncated).toBe(true);
    expect(result.items[0].negative).toBe(true);
  });

  it("rejects an on-hand body without items", async () => {
    stubFetch(200, {});
    await expect(fetchOnHand({ groupBy: [] })).rejects.toThrow("no items");
  });

  it("turns a problem response into an error with code and field errors", async () => {
    stubFetch(400, { code: "COM-0001", detail: "one or more fields are invalid", errors: { currency: "must be an ISO 4217 alpha-3 code" } });
    const failure = await listJournals().catch((e: unknown) => e);
    expect(failure).toBeInstanceOf(InventoryApiError);
    expect(failure).toMatchObject({ status: 400, code: "COM-0001", fields: { currency: "must be an ISO 4217 alpha-3 code" } });
  });

  it("falls back to the HTTP status when the error body is not a problem", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("bad gateway", { status: 502 })));
    await expect(listJournals()).rejects.toMatchObject({ code: "HTTP-502", message: "Request failed (HTTP 502)" });
  });

  it("lists journals with filters and cursor", async () => {
    const fetchMock = stubFetch(200, { items: [], nextCursor: "c2" });
    const page = await listJournals({ status: "POSTED", type: "ADJUSTMENT", cursor: "c1" });
    expect(call(fetchMock)[0]).toBe("/api/v1/inventory/journals?status=POSTED&type=ADJUSTMENT&cursor=c1");
    expect(page.nextCursor).toBe("c2");
  });

  it("creates a journal with an Idempotency-Key and leaves an empty offset account out", async () => {
    const fetchMock = stubFetch(201, { id: "j1" });
    await createJournal({ journalType: "ADJUSTMENT", legalEntityId: "le", postingDate: "2026-09-01", currency: "USD", offsetAccountId: "" }, "form-key");
    const [url, init] = call(fetchMock);
    expect(url).toBe("/api/v1/inventory/journals");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("form-key");
    expect(JSON.parse(init.body as string)).toEqual({ journalType: "ADJUSTMENT", legalEntityId: "le", postingDate: "2026-09-01", currency: "USD" });
  });

  it("adds lines with a client key each, trimmed dimensions and no blank ones", async () => {
    const fetchMock = stubFetch(201, { id: "j1", lines: [] });
    await addLines("j1", [{ productId: "p", qty: "-2", enteredUnit: "EA", costPrice: " ", dimensions: { site: " S1 ", warehouse: "", batch: "B1" } }]);
    const [url, init] = call(fetchMock);
    expect(url).toBe("/api/v1/inventory/journals/j1/lines:bulk");
    expect(JSON.parse(init.body as string)).toEqual({
      lines: [{ clientKey: "key-1", productId: "p", qty: "-2", enteredUnit: "EA", dimensions: { site: "S1", batch: "B1" } }],
    });
  });

  it("posts with the shown version in If-Match", async () => {
    const fetchMock = stubFetch(200, { id: "j1", status: "POSTED" });
    await postJournal("j1", 3);
    const [url, init] = call(fetchMock);
    expect(url).toBe("/api/v1/inventory/journals/j1:post");
    expect(new Headers(init.headers).get("If-Match")).toBe('"3"');
  });
});
