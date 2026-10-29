import { describe, expect, it } from "vitest";
import { buildCatalog } from "../src/catalog.js";
import { handleRpc, RPC } from "../src/jsonrpc.js";
import { principal, readTool, rig } from "./fixtures.js";

const catalog = () =>
  buildCatalog([
    readTool("passage.ap.invoice.list", "APInvoiceRead"),
    readTool("passage.ledger.journal.get", "LedgerRead"),
  ]);

describe("MCP JSON-RPC handler", () => {
  it("tools/list returns only the tools the caller may use", async () => {
    const r = rig(catalog());
    const res = await handleRpc(r.deps, principal(["LedgerRead"]), { jsonrpc: "2.0", id: 1, method: "tools/list" });
    expect(res).toMatchObject({ result: { tools: [{ name: "passage.ledger.journal.get" }] } });
  });

  it("tools/call without the duty answers with the 403 mapping and the API is not called", async () => {
    const r = rig(catalog());
    const res = await handleRpc(r.deps, principal([]), {
      jsonrpc: "2.0",
      id: "a",
      method: "tools/call",
      params: { name: "passage.ap.invoice.list", arguments: {} },
    });
    expect(res).toMatchObject({ error: { code: RPC.forbidden, data: { status: 403, code: "AGT-0203" } } });
    expect(r.upstream.calls).toHaveLength(0);
  });

  it("tools/call returns text content for an allowed read", async () => {
    const r = rig(catalog());
    r.upstream.responses.push({ status: 200, body: { items: [1] } });
    const res = await handleRpc(r.deps, principal(["APInvoiceRead"]), {
      jsonrpc: "2.0",
      id: 2,
      method: "tools/call",
      params: { name: "passage.ap.invoice.list", arguments: {} },
    });
    expect(res).toMatchObject({ result: { isError: false, content: [{ type: "text", text: '{"items":[1]}' }] } });
  });

  it("answers notifications with nothing", async () => {
    const r = rig(catalog());
    const res = await handleRpc(r.deps, principal([]), { jsonrpc: "2.0", method: "notifications/initialized" });
    expect(res).toBeUndefined();
  });

  it("rejects unknown methods and missing tool names", async () => {
    const r = rig(catalog());
    expect(await handleRpc(r.deps, principal([]), { jsonrpc: "2.0", id: 3, method: "resources/list" })).toMatchObject({
      error: { code: RPC.methodNotFound },
    });
    expect(await handleRpc(r.deps, principal([]), { jsonrpc: "2.0", id: 4, method: "tools/call", params: {} })).toMatchObject({
      error: { code: RPC.invalidParams },
    });
  });

  it("initialize advertises the tools capability", async () => {
    const r = rig(catalog());
    const res = await handleRpc(r.deps, principal([]), { jsonrpc: "2.0", id: 5, method: "initialize" });
    expect(res).toMatchObject({ result: { capabilities: { tools: { listChanged: false } }, serverInfo: { name: "passage-mcp" } } });
  });
});
