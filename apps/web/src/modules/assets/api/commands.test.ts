import { afterEach, describe, expect, it, vi } from "vitest";
import { addAddition, adjustBook, decideCapex, getMidQuarterTest, listAdditions, postRun, putIndexSeries, submitCapex } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
});

function stub(status: number, body: unknown, headers: Record<string, string> = {}) {
  const fn = vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } }));
  vi.stubGlobal("fetch", fn);
  return fn;
}

function sent(fn: ReturnType<typeof stub>, i = 0) {
  const [url, init] = fn.mock.calls[i] as unknown as [string, RequestInit];
  return { url, init, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined };
}

describe("assets client, slice 2 commands", () => {
  it("posts an adjustment with an Idempotency-Key to the transactions path", async () => {
    const fn = stub(201, { book: {}, transaction: {}, limited: false, requested: "-5", warnings: [] });
    await adjustBook("a/1", { bookId: "b", type: "Impairment", transactionDate: "2026-06-30", amount: "-5" }, "k-1");
    const { url, init, headers, body } = sent(fn);
    expect(url).toBe("/api/v1/assets/a%2F1/transactions");
    expect(init.method).toBe("POST");
    expect(headers.get("Idempotency-Key")).toBe("k-1");
    expect(body).toEqual({ bookId: "b", type: "Impairment", transactionDate: "2026-06-30", amount: "-5" });
  });

  it("posts a run with If-Match and no body, and returns the new ETag", async () => {
    const fn = stub(200, { id: "r", status: "Posted" }, { ETag: '"3"' });
    const res = await postRun("r", '"2"');
    const { url, init, headers } = sent(fn);
    expect(url).toBe("/api/v1/assets/depreciation-runs/r/post");
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    expect(headers.get("If-Match")).toBe('"2"');
    expect(headers.get("Idempotency-Key")).toBeNull();
    expect(res).toEqual({ value: { id: "r", status: "Posted" }, etag: '"3"' });
  });

  it("loads an index series with PUT on its code", async () => {
    const fn = stub(200, { code: "US-MACH", values: [] });
    await putIndexSeries("US-MACH", { country: "US", classCode: "MACHINE", values: [{ year: 2026, value: "118.5" }] });
    const { url, init, body } = sent(fn);
    expect(url).toBe("/api/v1/assets/index-series/US-MACH");
    expect(init.method).toBe("PUT");
    expect(body).toEqual({ country: "US", classCode: "MACHINE", values: [{ year: 2026, value: "118.5" }] });
  });

  it("submits a capex request with the variant and If-Match", async () => {
    const fn = stub(200, { id: "c", status: "InApproval" }, { ETag: '"2"' });
    const res = await submitCapex("c", '"1"', "v-1");
    const { url, headers, body } = sent(fn);
    expect(url).toBe("/api/v1/assets/capex-requests/c/submit");
    expect(headers.get("If-Match")).toBe('"1"');
    expect(body).toEqual({ variantId: "v-1" });
    expect(res.etag).toBe('"2"');
  });

  it("reads list envelopes and tolerates a missing items array", async () => {
    stub(200, {});
    expect(await listAdditions("a")).toEqual([]);
    const fn = stub(201, { id: "x" });
    await addAddition("a", { type: "Accessory", description: "Cover", addedDate: "2026-01-01", financial: false }, "k-2");
    expect(sent(fn).headers.get("Idempotency-Key")).toBe("k-2");
  });
});

describe("assets client, slice 4 commands", () => {
  it("posts a decision to its own route with If-Match and the trimmed comment", async () => {
    const fn = stub(200, { id: "c", status: "Postponed" }, { ETag: '"3"' });
    const res = await decideCapex("c", '"2"', "postpone", "  Next budget year ");
    const { url, init, headers, body } = sent(fn);
    expect(url).toBe("/api/v1/assets/capex-requests/c/postpone");
    expect(init.method).toBe("POST");
    expect(headers.get("If-Match")).toBe('"2"');
    expect(headers.get("Idempotency-Key")).toBeNull();
    expect(body).toEqual({ comment: "Next budget year" });
    expect(res).toEqual({ value: { id: "c", status: "Postponed" }, etag: '"3"' });
  });

  it("sends no body for a decision without a comment", async () => {
    const fn = stub(200, { id: "c", status: "Approved" }, { ETag: '"3"' });
    await decideCapex("c", '"2"', "approve", "   ");
    const { url, init, headers } = sent(fn);
    expect(url).toBe("/api/v1/assets/capex-requests/c/approve");
    expect(init.body).toBeUndefined();
    expect(headers.get("Content-Type")).toBeNull();
  });

  it("raises the service problem of a refused decision", async () => {
    stub(403, { code: "AST-0028", detail: "you created or requested this capex request" });
    await expect(decideCapex("c", '"2"', "approve")).rejects.toMatchObject({ code: "AST-0028", status: 403 });
  });

  it("queries the mid-quarter test by entity, book and year", async () => {
    const fn = stub(200, { applies: false });
    await getMidQuarterTest({ legalEntityId: "le", bookCode: "TAX", year: 2026 });
    expect(sent(fn).url).toBe("/api/v1/assets/mid-quarter-test?legalEntityId=le&bookCode=TAX&year=2026");
  });
});
