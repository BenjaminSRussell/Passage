import type { Server } from "node:http";
import { afterEach, describe, expect, it } from "vitest";
import { buildCatalog } from "../src/catalog.js";
import { createHandler, MAX_BATCH, principalFromHeaders, startServer } from "../src/server.js";
import { createRateLimiter, type RateLimiter } from "../src/ratelimit.js";
import { RPC } from "../src/jsonrpc.js";
import { readTool, rig, writeTool } from "./fixtures.js";

const TENANT = "00000000-0000-7000-8000-0000000000aa";
const USER = "00000000-0000-7000-8000-000000000001";
const TRACE = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01";

const catalog = buildCatalog([
  readTool("passage.ap.invoice.list", "APInvoiceView"),
  writeTool("passage.ap.invoice.post", "APInvoicePost"),
]);

let server: Server | undefined;

afterEach(async () => {
  if (server) {
    await new Promise<void>((resolve) => server?.close(() => resolve()));
    server = undefined;
  }
});

async function boot(ready = true, maxBodyBytes?: number, limiter?: RateLimiter) {
  const r = rig(catalog);
  const opts = {
    deps: r.deps,
    build: "test-build",
    ready: async () => ready,
    ...(maxBodyBytes ? { maxBodyBytes } : {}),
    ...(limiter ? { limiter } : {}),
  };
  server = await startServer(opts, { host: "127.0.0.1", port: 0 });
  const addr = server.address();
  if (addr === null || typeof addr === "string") throw new Error("no port");
  return { base: `http://127.0.0.1:${addr.port}`, upstream: r.upstream };
}

function headers(duties: string[], extra: Record<string, string> = {}): Record<string, string> {
  return {
    authorization: "Bearer user-token-for-test",
    "x-passage-tenant": TENANT,
    "x-passage-user": USER,
    "x-passage-duties": duties.join(","),
    "content-type": "application/json",
    ...extra,
  };
}

async function rpc(base: string, body: unknown, h: Record<string, string>) {
  const res = await fetch(`${base}/mcp`, { method: "POST", headers: h, body: JSON.stringify(body) });
  const text = await res.text();
  return { status: res.status, headers: res.headers, json: text === "" ? undefined : (JSON.parse(text) as unknown) };
}

describe("TestServer_HealthAndReadiness", () => {
  it("liveness is always 200; readiness follows the probe", async () => {
    const { base } = await boot(false);
    const live = await fetch(`${base}/healthz`);
    expect(live.status).toBe(200);
    expect(live.headers.get("x-passage-api-build")).toBe("test-build");
    const ready = await fetch(`${base}/readyz`);
    expect(ready.status).toBe(503);
  });
});

describe("TestServer_Authentication", () => {
  it("refuses requests without the bearer token or the edge headers", async () => {
    const { base } = await boot();
    const noBearer = await rpc(base, { jsonrpc: "2.0", id: 1, method: "tools/list" }, headers(["APInvoiceView"], { authorization: "" }));
    expect(noBearer.status).toBe(401);
    expect(noBearer.headers.get("content-type")).toBe("application/problem+json");
    const badTenant = await rpc(base, { jsonrpc: "2.0", id: 1, method: "tools/list" }, headers(["APInvoiceView"], { "x-passage-tenant": "nope" }));
    expect(badTenant.status).toBe(401);
    expect(badTenant.json).toEqual({
      type: "https://passage.dev/problems/MCP-0401",
      title: "Unauthorized",
      status: 401,
      code: "MCP-0401",
      detail: "missing or invalid tenant",
    });
  });

  it("refuses the nil platform tenant, as the kernel tenant middleware does", () => {
    const nil = principalFromHeaders({
      authorization: "Bearer abc",
      "x-passage-tenant": "00000000-0000-0000-0000-000000000000",
      "x-passage-user": USER,
    });
    expect(nil).toEqual({ ok: false, message: "missing or invalid tenant" });
  });

  it("canonicalizes tenant and user ids to lowercase", () => {
    const p = principalFromHeaders({ authorization: "Bearer abc", "x-passage-tenant": TENANT.toUpperCase(), "x-passage-user": USER.toUpperCase() });
    expect(p.ok && [p.principal.tenantId, p.principal.userId]).toEqual([TENANT, USER]);
  });

  it("builds the principal from headers and ignores malformed traceparent", () => {
    const ok = principalFromHeaders({
      authorization: "Bearer abc.def",
      "x-passage-tenant": TENANT,
      "x-passage-user": USER,
      "x-passage-duties": " APInvoiceView , ,APInvoicePost",
      traceparent: TRACE,
      "x-agent-run-id": "run-9",
    });
    expect(ok.ok).toBe(true);
    if (!ok.ok) return;
    expect([...ok.principal.duties].sort()).toEqual(["APInvoicePost", "APInvoiceView"]);
    expect(ok.principal.traceparent).toBe(TRACE);
    expect(ok.principal.runId).toBe("run-9");
    const bad = principalFromHeaders({ authorization: "Bearer x", "x-passage-tenant": TENANT, "x-passage-user": USER, traceparent: "junk" });
    expect(bad.ok && bad.principal.traceparent).toBeFalsy();
  });
});

describe("TestServer_McpEndpoint", () => {
  it("lists only the tools the caller's duties allow (AC-47.1)", async () => {
    const { base } = await boot();
    const res = await rpc(base, { jsonrpc: "2.0", id: 1, method: "tools/list" }, headers(["APInvoiceView"]));
    expect(res.status).toBe(200);
    expect(res.headers.get("x-passage-api-build")).toBe("test-build");
    const names = (res.json as { result: { tools: Array<{ name: string }> } }).result.tools.map((t) => t.name);
    expect(names).toEqual(["passage.ap.invoice.list"]);
  });

  it("returns a JSON-RPC 403-class error and never calls upstream without the duty (AC-47.2)", async () => {
    const { base, upstream } = await boot();
    const res = await rpc(
      base,
      { jsonrpc: "2.0", id: 2, method: "tools/call", params: { name: "passage.ap.invoice.post", arguments: { invoiceId: "INV-1" } } },
      headers(["APInvoiceView"]),
    );
    expect(res.status).toBe(200);
    const err = (res.json as { error: { code: number } }).error;
    expect(err.code).toBe(RPC.forbidden);
    expect(upstream.calls).toHaveLength(0);
  });

  it("answers notifications with 202 and no body", async () => {
    const { base } = await boot();
    const res = await rpc(base, { jsonrpc: "2.0", method: "notifications/initialized" }, headers([]));
    expect(res.status).toBe(202);
    expect(res.json).toBeUndefined();
  });

  it("answers a batch in order and drops notifications from the reply", async () => {
    const { base } = await boot();
    const res = await rpc(
      base,
      [
        { jsonrpc: "2.0", id: "a", method: "initialize" },
        { jsonrpc: "2.0", method: "notifications/initialized" },
        { jsonrpc: "2.0", id: "b", method: "tools/list" },
      ],
      headers(["APInvoiceView"]),
    );
    const replies = res.json as Array<{ id: string }>;
    expect(replies.map((r) => r.id)).toEqual(["a", "b"]);
  });

  it("rejects malformed JSON with -32700 and unknown paths with 404", async () => {
    const { base } = await boot();
    const bad = await fetch(`${base}/mcp`, { method: "POST", headers: headers([]), body: "{not json" });
    expect(bad.status).toBe(400);
    expect(((await bad.json()) as { error: { code: number } }).error.code).toBe(-32700);
    const missing = await fetch(`${base}/nope`);
    expect(missing.status).toBe(404);
    const get = await fetch(`${base}/mcp`, { headers: headers([]) });
    expect(get.status).toBe(405);
  });

  it("refuses bodies over the limit with 413 before parsing", async () => {
    const { base } = await boot(true, 32);
    const big = JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list", params: { pad: "x".repeat(200) } });
    const res = await fetch(`${base}/mcp`, { method: "POST", headers: headers(["APInvoiceView"]), body: big });
    expect(res.status).toBe(413);
  });
});

describe("TestServer_RateLimit", () => {
  it("sends RateLimit-* on allowed calls and 429 MCP-0429 with Retry-After once the caller is over the limit", async () => {
    const { base, upstream } = await boot(true, undefined, createRateLimiter({ limit: 2, nowMs: () => 0 }));
    const call = { jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: "passage.ap.invoice.list", arguments: {} } };
    upstream.responses.push({ status: 200, body: { items: [] } }, { status: 200, body: { items: [] } });
    const first = await rpc(base, call, headers(["APInvoiceView"]));
    expect(first.status).toBe(200);
    expect(first.headers.get("ratelimit-limit")).toBe("2");
    expect(first.headers.get("ratelimit-remaining")).toBe("1");
    await rpc(base, call, headers(["APInvoiceView"]));
    const refused = await rpc(base, call, headers(["APInvoiceView"]));
    expect(refused.status).toBe(429);
    expect(refused.headers.get("retry-after")).toBe("60");
    expect(refused.json).toMatchObject({ code: "MCP-0429", status: 429 });
    expect(upstream.calls).toHaveLength(2);
    // Another user of the same tenant has their own window.
    const other = await rpc(base, call, headers(["APInvoiceView"], { "x-passage-user": "00000000-0000-7000-8000-000000000002" }));
    expect(other.status).toBe(200);
  });

  it("refuses a batch over the size cap without running any of it", async () => {
    const { base, upstream } = await boot();
    const batch = Array.from({ length: MAX_BATCH + 1 }, (_, i) => ({
      jsonrpc: "2.0",
      id: i,
      method: "tools/call",
      params: { name: "passage.ap.invoice.list", arguments: {} },
    }));
    const res = await rpc(base, batch, headers(["APInvoiceView"]));
    expect(res.status).toBe(200);
    expect((res.json as { error: { code: number } }).error.code).toBe(RPC.invalidRequest);
    expect(upstream.calls).toHaveLength(0);
  });
});

describe("createHandler", () => {
  it("is exported for mounting behind another server", () => {
    const r = rig(catalog);
    expect(typeof createHandler({ deps: r.deps, build: "b", ready: async () => true })).toBe("function");
  });
});
