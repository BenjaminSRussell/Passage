import { visibleTools } from "./catalog.js";
import { callTool, type GatewayDeps, type Principal } from "./gateway.js";
import type { JsonObject } from "./tool.js";

export const PROTOCOL_VERSION = "2025-03-26";

export const RPC = {
  invalidRequest: -32600,
  methodNotFound: -32601,
  invalidParams: -32602,
  /** Server-defined: the caller lacks the tool's duty (the gateway's 403). */
  forbidden: -32003,
} as const;

export interface RpcRequest {
  jsonrpc?: string;
  id?: string | number | null;
  method: string;
  params?: JsonObject;
}

export type RpcResponse =
  | { jsonrpc: "2.0"; id: string | number | null; result: unknown }
  | { jsonrpc: "2.0"; id: string | number | null; error: { code: number; message: string; data?: unknown } };

function ok(id: string | number | null, result: unknown): RpcResponse {
  return { jsonrpc: "2.0", id, result };
}

function error(id: string | number | null, code: number, message: string, data?: unknown): RpcResponse {
  return data === undefined
    ? { jsonrpc: "2.0", id, error: { code, message } }
    : { jsonrpc: "2.0", id, error: { code, message, data } };
}

/**
 * Handles one MCP JSON-RPC message. Messages without an id are notifications
 * and get no response (JSON-RPC 2.0 section 4.1).
 */
export async function handleRpc(deps: GatewayDeps, p: Principal, req: RpcRequest): Promise<RpcResponse | undefined> {
  if (req.jsonrpc !== "2.0" || typeof req.method !== "string") {
    return error(req.id ?? null, RPC.invalidRequest, "invalid JSON-RPC request");
  }
  if (req.id === undefined) return undefined;
  const id = req.id;

  switch (req.method) {
    case "initialize":
      return ok(id, {
        protocolVersion: PROTOCOL_VERSION,
        capabilities: { tools: { listChanged: false } },
        serverInfo: { name: "passage-mcp", version: "0.1.0" },
      });
    case "tools/list":
      return ok(id, { tools: visibleTools(deps.catalog, p.duties) });
    case "tools/call": {
      const name = req.params?.name;
      if (typeof name !== "string") return error(id, RPC.invalidParams, "params.name is required");
      const result = await callTool(deps, p, name, req.params?.arguments);
      if (result.kind === "ok") {
        // The continuation token travels in the MCP reserved _meta object (FR-47.1 read pagination).
        const meta = result.nextCursor === undefined ? {} : { _meta: { nextCursor: result.nextCursor } };
        return ok(id, { content: [{ type: "text", text: JSON.stringify(result.body) ?? "null" }], isError: false, ...meta });
      }
      if (result.status === 403) {
        return error(id, RPC.forbidden, result.message, { status: result.status, code: result.code });
      }
      return ok(id, { content: [{ type: "text", text: `${result.code}: ${result.message}` }], isError: true });
    }
    default:
      return error(id, RPC.methodNotFound, `method ${req.method} not found`);
  }
}
