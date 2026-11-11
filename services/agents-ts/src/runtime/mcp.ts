import type { Principal } from "../api/principal.js";
import type { ToolDescriptor, ToolPort, ToolResult } from "./engine.js";

/** One tool of the caller's `tools/list` (the gateway already hides tools whose duty the caller lacks, AC-47.1). */
export interface McpToolInfo {
  name: string;
  description: string;
  requiredDuty: string;
  approvalMode: ToolDescriptor["approvalMode"];
  readOnly: boolean;
}

/** The runtime's view of the MCP gateway for one caller: the tool port plus the catalog it was built from. */
export interface McpSession extends ToolPort {
  readonly tools: readonly McpToolInfo[];
}

export interface McpClientConfig {
  /** The gateway's base URL; JSON-RPC goes to `POST {baseUrl}/mcp`. */
  baseUrl: string;
  fetchFn?: typeof fetch;
  timeoutMs?: number;
}

/** Opens a session for a caller. The user's bearer token travels on every call, so the gateway and OPA decide as that user. */
export interface McpConnector {
  connect(caller: Principal, token: string, runId?: string): Promise<McpSession>;
}

interface RpcOk {
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
}

const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

/**
 * Fetch-based client of services/mcp-ts. No credentials of its own: the caller's token and the
 * edge identity headers are forwarded, so an agent never exceeds the user it acts for (AC-47.2).
 */
export function createMcpConnector(cfg: McpClientConfig): McpConnector {
  const fetchFn = cfg.fetchFn ?? fetch;
  const url = `${cfg.baseUrl.replace(/\/$/, "")}/mcp`;
  const timeoutMs = cfg.timeoutMs ?? 60_000;
  return {
    async connect(caller, token, runId): Promise<McpSession> {
      let nextId = 0;
      const rpc = async (method: string, params?: Record<string, unknown>): Promise<RpcOk> => {
        const res = await fetchFn(url, {
          method: "POST",
          headers: {
            "content-type": "application/json",
            accept: "application/json",
            authorization: `Bearer ${token}`,
            "x-passage-tenant": caller.tenantId,
            "x-passage-user": caller.userId,
            "x-passage-duties": [...caller.duties].join(","),
            ...(runId !== undefined ? { "x-agent-run-id": runId } : {}),
          },
          body: JSON.stringify({ jsonrpc: "2.0", id: (nextId += 1), method, ...(params !== undefined ? { params } : {}) }),
          signal: AbortSignal.timeout(timeoutMs),
        });
        const text = await res.text();
        let body: unknown;
        try {
          body = JSON.parse(text);
        } catch {
          body = undefined;
        }
        if (!res.ok && !(isRecord(body) && isRecord(body.error))) {
          throw new Error(`the MCP gateway answered ${res.status}`);
        }
        return isRecord(body) ? (body as RpcOk) : {};
      };

      const listed = await rpc("tools/list");
      if (listed.error !== undefined) throw new Error(`the MCP gateway refused tools/list: ${listed.error.message}`);
      const raw = isRecord(listed.result) && Array.isArray(listed.result.tools) ? listed.result.tools : [];
      const tools: McpToolInfo[] = [];
      for (const t of raw) {
        if (!isRecord(t) || typeof t.name !== "string") continue;
        const duty = t["x-passage-duty"];
        const mode = t["x-passage-approval"];
        if (typeof duty !== "string" || (mode !== "none" && mode !== "suggest" && mode !== "approve_before_commit")) continue;
        const ann = isRecord(t.annotations) ? t.annotations : {};
        tools.push({
          name: t.name,
          description: typeof t.description === "string" ? t.description : "",
          requiredDuty: duty,
          approvalMode: mode,
          readOnly: ann.readOnlyHint === true,
        });
      }
      const byName = new Map(tools.map((t) => [t.name, t]));
      return {
        tools,
        describe(name): ToolDescriptor | undefined {
          const t = byName.get(name);
          return t === undefined ? undefined : { requiredDuty: t.requiredDuty, approvalMode: t.approvalMode };
        },
        async call(name, args): Promise<ToolResult> {
          let answered: RpcOk;
          try {
            answered = await rpc("tools/call", { name, arguments: args });
          } catch (err) {
            return { ok: false, code: "AGT-0214", message: err instanceof Error ? err.message : "the MCP gateway did not answer" };
          }
          if (answered.error !== undefined) {
            const data = isRecord(answered.error.data) ? answered.error.data : {};
            return { ok: false, code: typeof data.code === "string" ? data.code : "AGT-0214", message: answered.error.message };
          }
          return toToolResult(answered.result);
        },
      };
    },
  };
}

function toToolResult(result: unknown): ToolResult {
  if (!isRecord(result) || !Array.isArray(result.content)) return { ok: false, code: "AGT-0214", message: "the MCP gateway sent an unreadable result" };
  const first = result.content[0];
  const text = isRecord(first) && typeof first.text === "string" ? first.text : "";
  if (result.isError === true) {
    const m = /^([A-Z]+-[0-9]+): (.*)$/s.exec(text);
    return { ok: false, code: m?.[1] ?? "AGT-0214", message: m?.[2] ?? text };
  }
  let output: unknown = text;
  try {
    output = JSON.parse(text) as unknown;
  } catch {
    // keep the text
  }
  const ref = isRecord(output) && typeof output.id === "string" ? output.id : undefined;
  return { ok: true, output, ...(ref !== undefined ? { ref } : {}) };
}
