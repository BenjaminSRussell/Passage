import { describe, expect, it } from "vitest";
import { buildCatalog, CatalogError } from "../src/catalog.js";
import { compositeTools, HELP_SEARCH_NAME, SEARCH_TOOLS_NAME } from "../src/composite.js";
import { callTool } from "../src/gateway.js";
import { handleRpc, RPC } from "../src/jsonrpc.js";
import { principal, readTool, rig } from "./fixtures.js";

const COPILOT = "mcp.copilot.use";

const tools = () => [
  ...compositeTools(),
  readTool("depot.ar.invoice.list", "receivables.view", { description: "List open customer invoices. Filter by status." }),
  readTool("depot.ap.invoice.get", "ap.invoice.read", { description: "Fetch one supplier invoice by id." }),
  readTool("depot.hr.salary.list", "hr.salary.read", { description: "List salary invoice payments for payroll." }),
];

const search = (duties: string[], args: unknown) => {
  const r = rig(tools());
  return { r, result: callTool(r.deps, principal(duties), SEARCH_TOOLS_NAME, args) };
};

describe("depot.meta.search_tools", () => {
  it("ranks the caller's tools by name and description overlap", async () => {
    const { result } = search([COPILOT, "receivables.view", "ap.invoice.read"], { query: "invoice" });
    expect(await result).toMatchObject({
      kind: "ok",
      body: { tools: [{ name: "depot.ap.invoice.get" }, { name: "depot.ar.invoice.list" }] },
    });
  });

  it("does not leak tools the caller cannot see, even when the query matches them", async () => {
    const res = await search([COPILOT, "receivables.view"], { query: "salary payroll invoice" }).result;
    const names = (res as { body: { tools: { name: string }[] } }).body.tools.map((t) => t.name);
    expect(names).toEqual(["depot.ar.invoice.list"]);
    expect(JSON.stringify(res)).not.toContain("salary");
  });

  it("returns a one-sentence description", async () => {
    const res = await search([COPILOT, "receivables.view"], { query: "customer" }).result;
    expect(res).toMatchObject({ body: { tools: [{ description: "List open customer invoices." }] } });
  });

  it("is denied without the copilot duty and the audit entry says DENIED", async () => {
    const { r, result } = search(["receivables.view"], { query: "invoice" });
    expect(await result).toMatchObject({ kind: "error", status: 403, code: "AGT-0203" });
    expect(r.audit.entries).toMatchObject([{ tool: SEARCH_TOOLS_NAME, outcome: "DENIED" }]);
  });

  it.each([[{}], [{ query: null }], [{ query: "" }]])("refuses an absent or empty query %j with MCP-0422", async (args) => {
    expect(await search([COPILOT], args).result).toMatchObject({ kind: "error", status: 400, code: "MCP-0422" });
  });

  it.each([[{ query: 5 }], [{ query: {} }], [{ query: ["invoice"] }]])("refuses a mistyped query %j with MCP-0400", async (args) => {
    expect(await search([COPILOT], args).result).toMatchObject({ kind: "error", status: 400, code: "MCP-0400" });
  });

  it("refuses a whitespace-only query with MCP-0400", async () => {
    expect(await search([COPILOT], { query: "   " }).result).toMatchObject({ kind: "error", status: 400, code: "MCP-0400" });
  });

  it("refuses tenant_id in the arguments", async () => {
    expect(await search([COPILOT], { query: "invoice", tenant_id: "x" }).result).toMatchObject({ code: "MCP-0400" });
  });

  it.each([
    [0, 1],
    [-3, 1],
    [1e9, 3],
    ["5", 3],
    [Number.NaN, 3],
    [{}, 3],
  ])("clamps limit %j to %i result(s) without failing", async (limit, expected) => {
    const res = await search([COPILOT, "receivables.view", "ap.invoice.read", "hr.salary.read"], { query: "invoice", limit }).result;
    expect(res.kind).toBe("ok");
    expect((res as { body: { tools: unknown[] } }).body.tools).toHaveLength(expected);
  });

  it("caps a huge limit at ten results", async () => {
    const many = Array.from({ length: 12 }, (_, i) => readTool(`depot.ar.invoice${i}.list`, "receivables.view", { description: "List invoices." }));
    const r = rig([...compositeTools(), ...many]);
    const res = await callTool(r.deps, principal([COPILOT, "receivables.view"]), SEARCH_TOOLS_NAME, { query: "invoices", limit: 1e9 });
    expect((res as { body: { tools: unknown[] } }).body.tools).toHaveLength(10);
  });

  it("tokenises control characters instead of failing", async () => {
    const res = await search([COPILOT, "receivables.view"], { query: "customer\u0000\n" }).result;
    expect(res).toMatchObject({ kind: "ok", body: { tools: [{ name: "depot.ar.invoice.list" }] } });
  });

  it("refuses a query longer than the advertised maxLength with MCP-0400", async () => {
    expect(await search([COPILOT], { query: "x".repeat(501) }).result).toMatchObject({ kind: "error", status: 400, code: "MCP-0400" });
  });

  it("reports 500 MCP-0500 without internals when the handler throws, and never calls the upstream", async () => {
    const boom = { ...compositeTools()[0]!, local: () => { throw new Error("secret stack detail"); } };
    const r = rig([boom, ...compositeTools().slice(1)]);
    const res = await callTool(r.deps, principal([COPILOT]), SEARCH_TOOLS_NAME, { query: "x" });
    expect(res).toMatchObject({ kind: "error", status: 500, code: "MCP-0500" });
    expect(JSON.stringify(res)).not.toContain("secret");
    expect(r.audit.entries).toMatchObject([{ outcome: "UPSTREAM_ERROR", status: 500 }]);
    expect(r.upstream.calls).toHaveLength(0);
  });

  it("logs the failing local handler's error name and a stable code, never its message,, without the arguments", async () => {
    const boom = { ...compositeTools()[0]!, local: () => { throw new RangeError("handler broke"); } };
    const r = rig([boom, ...compositeTools().slice(1)]);
    const lines: Record<string, unknown>[] = [];
    await callTool({ ...r.deps, log: (rec) => lines.push(rec) }, principal([COPILOT]), SEARCH_TOOLS_NAME, { query: "private-words" });
    expect(lines).toEqual([expect.objectContaining({ level: "error", message: "mcp.local_tool_failed", tool: SEARCH_TOOLS_NAME, code: "MCP-0500", errorName: "RangeError" })]);
    expect(lines[0]).not.toHaveProperty("error");
    expect(JSON.stringify(lines)).not.toContain("handler broke");
    expect(JSON.stringify(lines)).not.toContain("private-words");
  });

  it("reports 500 MCP-0500 and logs mcp.local_tool_failed when an async handler rejects", async () => {
    const boom = { ...compositeTools()[0]!, local: () => Promise.reject(new RangeError("async broke")) };
    const r = rig([boom, ...compositeTools().slice(1)]);
    const lines: Record<string, unknown>[] = [];
    const res = await callTool({ ...r.deps, log: (rec) => lines.push(rec) }, principal([COPILOT]), SEARCH_TOOLS_NAME, { query: "private-words" });
    expect(res).toMatchObject({ kind: "error", status: 500, code: "MCP-0500" });
    expect(lines).toEqual([expect.objectContaining({ message: "mcp.local_tool_failed", errorName: "RangeError" })]);
    expect(JSON.stringify(lines)).not.toContain("async broke");
    expect(r.audit.entries).toMatchObject([{ outcome: "UPSTREAM_ERROR", status: 500 }]);
  });

  it("answers 502 MCP-0502 when a local handler's result does not match its outputSchema", async () => {
    const wrong = { ...compositeTools()[0]!, local: async () => "not an object" };
    const r = rig([wrong, ...compositeTools().slice(1)]);
    expect(await callTool(r.deps, principal([COPILOT]), SEARCH_TOOLS_NAME, { query: "x" })).toMatchObject({ kind: "error", status: 502, code: "MCP-0502" });
    expect(r.audit.entries).toMatchObject([{ outcome: "UPSTREAM_ERROR", status: 502 }]);
  });

  it("audits an OK call without the query text and forwards nothing upstream", async () => {
    const { r, result } = search([COPILOT, "receivables.view"], { query: "customer secret-query" });
    await result;
    expect(r.upstream.calls).toHaveLength(0);
    expect(r.audit.entries).toMatchObject([{ tool: SEARCH_TOOLS_NAME, outcome: "OK", status: 200 }]);
    expect(JSON.stringify(r.audit.entries)).not.toContain("secret-query");
  });

  it("is listed by tools/list only for holders of the copilot duty", async () => {
    const r = rig(tools());
    const names = async (duties: string[]) => {
      const res = (await handleRpc(r.deps, principal(duties), { jsonrpc: "2.0", id: 1, method: "tools/list" })) as { result: { tools: { name: string }[] } };
      return res.result.tools.map((t) => t.name);
    };
    expect(await names([COPILOT])).toEqual([HELP_SEARCH_NAME, SEARCH_TOOLS_NAME]);
    expect(await names([])).toEqual([]);
  });

  it("answers over JSON-RPC tools/call as text content", async () => {
    const r = rig(tools());
    const res = await handleRpc(r.deps, principal([COPILOT, "receivables.view"]), {
      jsonrpc: "2.0",
      id: 3,
      method: "tools/call",
      params: { name: SEARCH_TOOLS_NAME, arguments: { query: "customer" } },
    });
    expect(res).toMatchObject({ result: { isError: false, content: [{ type: "text" }] } });
  });

  it("refuses a duplicate name already supplied by the OpenAPI tools", () => {
    expect(() => buildCatalog([readTool(SEARCH_TOOLS_NAME, COPILOT), ...compositeTools()])).toThrow(CatalogError);
  });
});

describe("depot.help.search", () => {
  const helpRig = () => rig(compositeTools());
  const call = (r: ReturnType<typeof helpRig>, duties: string[], args: unknown) => callTool(r.deps, principal(duties), HELP_SEARCH_NAME, args);

  it("forwards the bearer to the knowledge search API and returns the chunks", async () => {
    const r = helpRig();
    r.upstream.responses.push({ status: 200, body: { chunks: [{ chunkId: "c1" }] } });
    expect(await call(r, [COPILOT], { query: "how do I post an invoice" })).toMatchObject({ kind: "ok", body: { chunks: [{ chunkId: "c1" }] } });
    expect(r.upstream.calls).toHaveLength(1);
    expect(r.upstream.calls[0]).toMatchObject({
      tool: { method: "POST", path: "/api/v1/knowledge/search" },
      args: { query: "how do I post an invoice" },
      headers: { authorization: "Bearer user-token-for-test" },
    });
    expect(r.upstream.calls[0]?.args).not.toHaveProperty("limit");
  });

  it("is denied without the copilot duty and never reaches the upstream", async () => {
    const r = helpRig();
    expect(await call(r, [], { query: "x" })).toMatchObject({ kind: "error", status: 403, code: "AGT-0203" });
    expect(r.upstream.calls).toHaveLength(0);
  });

  it.each([[{}], [{ query: "" }]])("refuses a missing or empty query %j with MCP-0422", async (args) => {
    const r = helpRig();
    expect(await call(r, [COPILOT], args)).toMatchObject({ status: 400, code: "MCP-0422" });
    expect(r.upstream.calls).toHaveLength(0);
  });

  it.each([[{ query: 5 }], [{ query: {} }], [{ query: ["x"] }], [{ query: "   " }], [{ query: "\n\t" }]])("refuses a mistyped or blank query %j with MCP-0400 before the upstream", async (args) => {
    const r = helpRig();
    expect(await call(r, [COPILOT], args)).toMatchObject({ kind: "error", status: 400, code: "MCP-0400" });
    expect(r.upstream.calls).toHaveLength(0);
    expect(r.audit.entries).toMatchObject([{ outcome: "INVALID" }]);
  });

  it("answers 503 AGT-0214 after retries when the upstream is down", async () => {
    const r = helpRig();
    for (let i = 0; i < 4; i += 1) r.upstream.responses.push({ status: 503, body: null });
    expect(await call(r, [COPILOT], { query: "x" })).toMatchObject({ kind: "error", status: 503, code: "AGT-0214" });
    expect(r.upstream.calls).toHaveLength(4);
  });

  it("answers 502 MCP-0502 when chunks is not an array", async () => {
    const r = helpRig();
    r.upstream.responses.push({ status: 200, body: { chunks: "nope" } });
    expect(await call(r, [COPILOT], { query: "x" })).toMatchObject({ kind: "error", status: 502, code: "MCP-0502" });
  });

  it("refuses a query longer than the advertised 1000 characters with MCP-0400 and never reaches the upstream", async () => {
    const r = helpRig();
    expect(await call(r, [COPILOT], { query: "x".repeat(1001) })).toMatchObject({ kind: "error", status: 400, code: "MCP-0400" });
    expect(r.upstream.calls).toHaveLength(0);
    expect(r.audit.entries).toMatchObject([{ outcome: "INVALID" }]);
  });

  it("counts UTF-16 units like the knowledge API, so 501 astral characters are refused and the 400 does not name the argument", async () => {
    const r = helpRig();
    r.upstream.responses.push({ status: 200, body: { chunks: [] } });
    expect(await call(r, [COPILOT], { query: "\u{1F600}".repeat(500) })).toMatchObject({ kind: "ok" });
    expect(await call(r, [COPILOT], { query: "\u{1F600}".repeat(501) })).toMatchObject({ kind: "error", status: 400, code: "MCP-0400" });
    const refused = await call(r, [COPILOT], { query: "secret-value".padEnd(1001, "x") });
    expect(refused).toEqual({ kind: "error", status: 400, code: "MCP-0400", message: "an argument is longer than its maximum length" });
    expect(r.upstream.calls).toHaveLength(1);
  });

  it("forwards only the declared query, dropping limit and unknown keys the API would reject", async () => {
    const r = helpRig();
    r.upstream.responses.push({ status: 200, body: { chunks: [] } });
    await call(r, [COPILOT], { query: "x", limit: 500, topK: 3, extra: { a: 1 } });
    expect(r.upstream.calls[0]?.args).toEqual({ query: "x" });
  });

  it("refuses tenant_id in the arguments", async () => {
    const r = helpRig();
    expect(await call(r, [COPILOT], { query: "x", tenant_id: "t" })).toMatchObject({ code: "MCP-0400" });
    expect(r.upstream.calls).toHaveLength(0);
  });
});

it("leaves a generated upstream tool with maxLength in its schema to the upstream", async () => {
  const generated = readTool("depot.ar.invoice.list", "receivables.view", { inputSchema: { type: "object", properties: { note: { type: "string", maxLength: 5 } } } });
  const r = rig([generated]);
  r.upstream.responses.push({ status: 200, body: {} });
  const res = await callTool(r.deps, principal(["receivables.view"]), "depot.ar.invoice.list", { note: "longer than five" });
  expect(res).toMatchObject({ kind: "ok" });
  expect(r.upstream.calls).toHaveLength(1);
});

it("maps the forbidden RPC code for a search_tools denial", async () => {
  const r = rig(tools());
  const res = await handleRpc(r.deps, principal([]), { jsonrpc: "2.0", id: 9, method: "tools/call", params: { name: SEARCH_TOOLS_NAME, arguments: { query: "x" } } });
  expect(res).toMatchObject({ error: { code: RPC.forbidden } });
});
