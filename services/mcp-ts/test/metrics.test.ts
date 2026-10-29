import type { Server } from "node:http";
import { afterEach, describe, expect, it } from "vitest";
import { buildCatalog } from "../src/catalog.js";
import { Metrics, routeLabel, startAdminServer } from "../src/metrics.js";
import { startServer } from "../src/server.js";
import { readTool, rig } from "./fixtures.js";

const catalog = buildCatalog([readTool("passage.ap.invoice.list", "APInvoiceView")]);

let server: Server | undefined;
let admin: Server | undefined;

afterEach(async () => {
  for (const s of [server, admin]) {
    if (s) await new Promise<void>((resolve) => s.close(() => resolve()));
  }
  server = undefined;
  admin = undefined;
});

describe("Metrics registry", () => {
  it("renders counters in Prometheus text format, sorted and stable", () => {
    const m = new Metrics();
    m.recordResponse("mcp", 200);
    m.recordResponse("mcp", 200);
    m.recordResponse("healthz", 200);
    m.recordResponse("mcp", 401);
    m.recordRateLimited();
    const text = m.render();
    expect(text).toContain("# TYPE mcp_http_requests_total counter");
    expect(text).toContain('mcp_http_requests_total{route="healthz",status="200"} 1');
    expect(text).toContain('mcp_http_requests_total{route="mcp",status="200"} 2');
    expect(text).toContain('mcp_http_requests_total{route="mcp",status="401"} 1');
    expect(text).toContain("mcp_rate_limited_total 1");
    expect(text.indexOf('route="healthz"')).toBeLessThan(text.indexOf('route="mcp",status="200"'));
    expect(text.endsWith("\n")).toBe(true);
  });

  it("maps any unknown path to the bounded 'other' label", () => {
    expect(routeLabel("/mcp")).toBe("mcp");
    expect(routeLabel("/metrics")).toBe("other");
    expect(routeLabel("/v1/anything/9f8e7d")).toBe("other");
    expect(routeLabel("/")).toBe("other");
  });
});

describe("admin listener", () => {
  async function listen(srv: Server): Promise<string> {
    const addr = srv.address();
    if (addr === null || typeof addr === "string") throw new Error("no port");
    return `http://127.0.0.1:${addr.port}`;
  }

  it("serves on /metrics the counters the public listener recorded; the public listener has no /metrics", async () => {
    const r = rig(catalog);
    const metrics = new Metrics();
    server = await startServer({ deps: r.deps, build: "m-build", ready: async () => true, metrics }, { host: "127.0.0.1", port: 0 });
    admin = await startAdminServer(metrics, { host: "127.0.0.1", port: 0 });
    const base = await listen(server);
    await fetch(`${base}/healthz`);
    await fetch(`${base}/nope`);
    const pub = await fetch(`${base}/metrics`);
    expect(pub.status).toBe(404);
    expect(await pub.json()).toMatchObject({ code: "MCP-0404" });

    const res = await fetch(`${await listen(admin)}/metrics`);
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("text/plain; version=0.0.4; charset=utf-8");
    const text = await res.text();
    expect(text).toContain('mcp_http_requests_total{route="healthz",status="200"} 1');
    expect(text).toContain('mcp_http_requests_total{route="other",status="404"} 2');
    expect(text).toContain("mcp_rate_limited_total 0");
  });

  it("counts a 429 refusal from the limiter", async () => {
    const r = rig(catalog);
    const metrics = new Metrics();
    const limiter = { hit: () => ({ allowed: false, limit: 1, remaining: 0, resetSeconds: 30, retryAfterSeconds: 30 }) };
    server = await startServer({ deps: r.deps, build: "m", ready: async () => true, metrics, limiter }, { host: "127.0.0.1", port: 0 });
    const res = await fetch(`${await listen(server)}/mcp`, {
      method: "POST",
      headers: {
        authorization: "Bearer t",
        "x-passage-tenant": "00000000-0000-7000-8000-000000000001",
        "x-passage-user": "00000000-0000-7000-8000-0000000000a1",
        "content-type": "application/json",
      },
      body: "{}",
    });
    expect(res.status).toBe(429);
    await res.text();
    const text = metrics.render();
    expect(text).toContain("mcp_rate_limited_total 1");
    expect(text).toContain('mcp_http_requests_total{route="mcp",status="429"} 1');
  });

  it("refuses other methods and other paths on the admin listener", async () => {
    admin = await startAdminServer(new Metrics(), { host: "127.0.0.1", port: 0 });
    const base = await listen(admin);
    const post = await fetch(`${base}/metrics`, { method: "POST", body: "x" });
    expect(post.status).toBe(405);
    expect(post.headers.get("allow")).toBe("GET, HEAD");
    expect((await fetch(`${base}/mcp`)).status).toBe(404);
    expect(await (await fetch(`${base}/healthz`)).text()).toBe("ok\n");
  });
});
