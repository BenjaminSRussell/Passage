import { describe, expect, it } from "vitest";
import { agentCatalog } from "../src/main.js";
import { createProviderFactory, loadConfig, MODEL_HAIKU, MODEL_OPUS, MODEL_SONNET } from "../src/config.js";
import { createMcpConnector } from "../src/runtime/mcp.js";
import { createRequestHandler, Metrics, routeLabel } from "../src/server.js";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { KillSwitchRegistry } from "../src/domain/killswitch.js";
import { MemoryStore, USER, T1 } from "./surface-fakes.js";

const caller = { userId: USER, tenantId: T1, duties: new Set(["ap.invoice.read", "agents.invoke"]) };
const rpcResult = (result: unknown): Response => new Response(JSON.stringify({ jsonrpc: "2.0", id: 1, result }), { status: 200 });

describe("MCP connector", () => {
  const listing = {
    tools: [
      { name: "passage.ap.invoice.get", description: "d", inputSchema: {}, annotations: { readOnlyHint: true, destructiveHint: false, idempotentHint: true }, "x-passage-duty": "ap.invoice.read", "x-passage-approval": "none" },
      { name: "bad", description: "no duty" },
    ],
  };

  it("lists the caller's tools and forwards the user's token and identity on every call, with no credentials of its own", async () => {
    const seen: Array<{ url: string; headers: Record<string, string>; body: { method: string } }> = [];
    const fetchFn: typeof fetch = async (url, init) => {
      const body = JSON.parse(String(init?.body)) as { method: string };
      seen.push({ url: String(url), headers: init?.headers as Record<string, string>, body });
      if (body.method === "tools/list") return rpcResult(listing);
      return rpcResult({ content: [{ type: "text", text: JSON.stringify({ id: "INV-7" }) }], isError: false });
    };
    const session = await createMcpConnector({ baseUrl: "http://mcp.internal/", fetchFn }).connect(caller, "tok-1", "run-9");
    expect(session.tools.map((t) => t.name)).toEqual(["passage.ap.invoice.get"]);
    expect(session.describe("passage.ap.invoice.get")).toEqual({ requiredDuty: "ap.invoice.read", approvalMode: "none" });
    expect(session.describe("bad")).toBeUndefined();
    expect(await session.call("passage.ap.invoice.get", { invoiceId: "INV-7" })).toEqual({ ok: true, output: { id: "INV-7" }, ref: "INV-7" });
    for (const s of seen) {
      expect(s.url).toBe("http://mcp.internal/mcp");
      expect(s.headers).toMatchObject({ authorization: "Bearer tok-1", "x-passage-tenant": T1, "x-passage-user": USER, "x-agent-run-id": "run-9" });
    }
  });

  it("maps a forbidden call, a tool error and an unreachable gateway to failed results", async () => {
    const answers: Response[] = [
      rpcResult(listing),
      new Response(JSON.stringify({ jsonrpc: "2.0", id: 2, error: { code: -32003, message: "no duty", data: { code: "AGT-0203" } } }), { status: 200 }),
      rpcResult({ content: [{ type: "text", text: "AGT-0214: upstream said no" }], isError: true }),
    ];
    const fetchFn: typeof fetch = async () => answers.shift() ?? Promise.reject(new Error("down"));
    const session = await createMcpConnector({ baseUrl: "http://mcp", fetchFn }).connect(caller, "t");
    expect(await session.call("passage.ap.invoice.get", {})).toEqual({ ok: false, code: "AGT-0203", message: "no duty" });
    expect(await session.call("passage.ap.invoice.get", {})).toEqual({ ok: false, code: "AGT-0214", message: "upstream said no" });
    expect(await session.call("passage.ap.invoice.get", {})).toMatchObject({ ok: false, code: "AGT-0214" });
  });

  it("fails to connect when tools/list is refused", async () => {
    const fetchFn: typeof fetch = async () => new Response("{}", { status: 401 });
    await expect(createMcpConnector({ baseUrl: "http://mcp", fetchFn }).connect(caller, "t")).rejects.toThrow(/401/);
  });
});

describe("config", () => {
  const base = { MCP_URL: "http://mcp:8080" };

  it("defaults to the Claude Sonnet model on the Anthropic provider and keeps every model configurable", () => {
    const cfg = loadConfig(base);
    expect([MODEL_OPUS, MODEL_SONNET, MODEL_HAIKU]).toEqual(["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"]);
    expect(cfg).toMatchObject({ provider: "anthropic", model: "claude-sonnet-5-5", copilotModel: "claude-sonnet-5-5", port: 8080, adminPort: 9090 });
    const tuned = loadConfig({ ...base, AGENTS_MODEL: MODEL_OPUS, AGENTS_COPILOT_MODEL: MODEL_HAIKU, AGENTS_MODEL_OVERRIDES: `{"anomaly":"${MODEL_HAIKU}"}` });
    expect(tuned.copilotModel).toBe("claude-haiku-5-5");
    const models = Object.fromEntries(agentCatalog(tuned).map((a) => [a.agentId, a.modelId]));
    expect(models).toMatchObject({ ap_coding: "claude-opus-5-5", anomaly: "claude-haiku-5-5" });
  });

  it.each([
    [{}, /MCP_URL/],
    [{ ...base, AGENTS_PROVIDER: "gemini" }, /AGENTS_PROVIDER/],
    [{ ...base, HTTP_ADDR: ":9090" }, /different port/],
    [{ ...base, AGENTS_RUN_TOKEN_CAP: "0" }, /AGENTS_RUN_TOKEN_CAP/],
    [{ ...base, AGENTS_MODEL_OVERRIDES: "[1]" }, /AGENTS_MODEL_OVERRIDES/],
    [{ ...base, AGENTS_APPROVAL_EXPIRY_H: "500" }, /1 to 240/],
    [{ ...base, AGENTS_SELF_APPROVAL_LIMIT: "ten" }, /decimal/],
  ])("refuses invalid settings %#", (env, message) => {
    expect(() => loadConfig(env)).toThrow(message);
  });

  it("builds each provider through the factory against an injected fetch, and names a missing credential without its value", async () => {
    const calls: string[] = [];
    const fetchFn: typeof fetch = async (url) => {
      calls.push(String(url));
      return new Response(JSON.stringify({ content: [{ type: "tool_use", name: "propose_action", input: { text: "hi" } }], usage: { input_tokens: 1, output_tokens: 1 } }), { status: 200 });
    };
    const req = { system: "s", user: "u", allowedTools: ["passage.ap.invoice.get"] };
    const anthropic = createProviderFactory(loadConfig({ ...base, ANTHROPIC_API_KEY: "sk-test-not-real" }), fetchFn);
    expect((await anthropic("claude-haiku-5-5").complete(req)).text).toBe("hi");
    expect(calls).toEqual(["https://api.anthropic.com/v1/messages"]);
    expect(() => createProviderFactory(loadConfig(base))(MODEL_SONNET)).toThrow("ANTHROPIC_API_KEY is not set");
    expect(() => createProviderFactory(loadConfig({ ...base, AGENTS_PROVIDER: "openai" }))("m")).toThrow(/OPENAI_API_KEY/);
    expect(() => createProviderFactory(loadConfig({ ...base, AGENTS_PROVIDER: "bedrock" }))("m")).toThrow(/AWS_REGION/);
    const bedrock = createProviderFactory(loadConfig({ ...base, AGENTS_PROVIDER: "bedrock", AWS_REGION: "us-east-1", AWS_ACCESS_KEY_ID: "AKIATEST", AWS_SECRET_ACCESS_KEY: "secret" }));
    expect(bedrock("anthropic.claude-sonnet-5-5-v1:0")).toBe(bedrock("anthropic.claude-sonnet-5-5-v1:0"));
  });
});

describe("server wiring", () => {
  it("serves probes, labels routes with a bounded set and counts responses for the admin listener", async () => {
    const store = new MemoryStore();
    const metrics = new Metrics();
    let ready = true;
    const handler = createRequestHandler({
      api: { registry: new KillSwitchRegistry(() => Date.now()), versions: { next: () => 1 }, approvals: store, publish: async () => undefined },
      metrics,
      ready: async () => ready,
    });
    const server = createServer(handler);
    await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
    const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
    expect((await fetch(`${base}/healthz`)).status).toBe(200);
    expect((await fetch(`${base}/readyz`)).status).toBe(200);
    ready = false;
    expect((await fetch(`${base}/readyz`)).status).toBe(503);
    expect((await fetch(`${base}/api/v1/unknown/${"x".repeat(50)}`)).status).toBe(404);
    await new Promise<void>((r) => server.close(() => r()));
    const text = metrics.render();
    expect(text).toContain('agents_http_requests_total{route="healthz",status="200"} 1');
    expect(text).toContain('agents_http_requests_total{route="readyz",status="503"} 1');
    expect(text).toContain('agents_http_requests_total{route="other",status="404"} 1');
    expect(routeLabel("/api/v1/agents/ap_coding/runs")).toBe("start-run");
    expect(routeLabel("/api/v1/agents/runs/abc/actions")).toBe("run");
  });
});
