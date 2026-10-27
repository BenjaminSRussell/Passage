import { afterEach, describe, expect, it, vi } from "vitest";
import { AssetsApiError, describeProblem, disposeAsset, getAsset, listAssets, schedulePath } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
});

function stub(status: number, body: unknown, headers: Record<string, string> = {}) {
  const fn = vi.fn(async () => new Response(typeof body === "string" ? body : JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } }));
  vi.stubGlobal("fetch", fn);
  return fn;
}

describe("assets client", () => {
  it("encodes ids in the schedule path", () => {
    expect(schedulePath("a/b", "c d")).toBe("/api/v1/assets/a%2Fb/schedule?book=c%20d");
  });

  it("sends the status filter and cursor only when set", async () => {
    const fn = stub(200, { items: [] });
    await listAssets({ status: "Open", after: "abc" });
    await listAssets({ status: "" });
    const urls = fn.mock.calls.map((c) => (c as unknown as [string])[0]);
    expect(urls).toEqual(["/api/v1/assets?status=Open&after=abc", "/api/v1/assets"]);
  });

  it("returns the ETag with the asset", async () => {
    stub(200, { id: "x", books: [] }, { ETag: '"4"' });
    const res = await getAsset("x");
    expect(res.etag).toBe('"4"');
  });

  it("posts a disposal with Idempotency-Key and If-Match", async () => {
    const fn = stub(201, { asset: {}, books: [], transactions: [] });
    await disposeAsset("x", '"4"', { kind: "scrap", percentage: "100" }, "key-1");
    const [url, init] = fn.mock.calls[0] as unknown as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe("/api/v1/assets/x/dispose");
    expect(init.method).toBe("POST");
    expect(headers.get("Idempotency-Key")).toBe("key-1");
    expect(headers.get("If-Match")).toBe('"4"');
    expect(JSON.parse(init.body as string)).toEqual({ kind: "scrap", percentage: "100" });
  });

  it("raises AssetsApiError with the stable code and field errors", async () => {
    stub(400, { code: "COM-0001", detail: "request is invalid", errors: { cost: "must be a non-negative decimal" } });
    const err = await listAssets().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(AssetsApiError);
    expect(describeProblem(err)).toEqual({ status: 400, code: "COM-0001", message: "request is invalid", fields: { cost: "must be a non-negative decimal" } });
  });

  it("falls back to the HTTP status when the error body is not JSON", async () => {
    stub(502, "gateway down");
    const problem = describeProblem(await listAssets().catch((e: unknown) => e));
    expect(problem.code).toBe("HTTP-502");
    expect(problem.message).toContain("502");
  });
});
