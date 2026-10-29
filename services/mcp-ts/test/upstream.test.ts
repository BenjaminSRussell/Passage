import { describe, expect, it } from "vitest";
import { createFetchUpstream } from "../src/upstream.js";
import { readTool, writeTool } from "./fixtures.js";

function capture() {
  const seen: Array<{ url: string; init: RequestInit }> = [];
  const fetchFn = (async (url: URL | string, init?: RequestInit) => {
    seen.push({ url: String(url), init: init ?? {} });
    return new Response(JSON.stringify({ ok: true }), { status: 200 });
  }) as typeof fetch;
  return { seen, fetchFn };
}

describe("createFetchUpstream", () => {
  it("fills path parameters, sends the rest as query string on GET and forwards the headers", async () => {
    const { seen, fetchFn } = capture();
    const up = createFetchUpstream("https://api.passage.test", 1000, fetchFn);
    const tool = readTool("passage.ap.invoice.get", "APInvoiceRead", { path: "/v1/ap/invoices/{id}" });
    const res = await up.send({ tool, args: { id: "a/b", status: "open", limit: 5 }, headers: { authorization: "Bearer t" } });
    expect(res).toEqual({ status: 200, body: { ok: true } });
    expect(seen[0]?.url).toBe("https://api.passage.test/v1/ap/invoices/a%2Fb?status=open&limit=5");
    expect(new Headers(seen[0]?.init.headers).get("authorization")).toBe("Bearer t");
    expect(seen[0]?.init.body).toBeUndefined();
  });

  it("sends a JSON body on POST and moves idempotencyKey and ifMatch into headers", async () => {
    const { seen, fetchFn } = capture();
    const up = createFetchUpstream("https://api.passage.test", 1000, fetchFn);
    const tool = writeTool("passage.ap.invoice.propose_coding", "APInvoiceCode", { path: "/v1/ap/invoices/{id}:propose-coding" });
    await up.send({ tool, args: { id: "inv-1", note: "x", idempotencyKey: "k-1", ifMatch: '"3"' }, headers: {} });
    const headers = new Headers(seen[0]?.init.headers);
    expect(seen[0]?.url).toBe("https://api.passage.test/v1/ap/invoices/inv-1:propose-coding");
    expect(headers.get("idempotency-key")).toBe("k-1");
    expect(headers.get("if-match")).toBe('"3"');
    expect(JSON.parse(String(seen[0]?.init.body))).toEqual({ note: "x" });
  });

  it("refuses to send a request whose path parameter is missing", async () => {
    const { seen, fetchFn } = capture();
    const up = createFetchUpstream("https://api.passage.test", 1000, fetchFn);
    const tool = readTool("passage.ap.invoice.get", "APInvoiceRead", { path: "/v1/ap/invoices/{id}" });
    await expect(up.send({ tool, args: {}, headers: {} })).rejects.toThrow(/path parameter id/);
    expect(seen).toHaveLength(0);
  });
});
