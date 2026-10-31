import { z } from "zod";

/**
 * Client for the MCP gateway's tool catalog (services/mcp-ts/api/openapi.yaml,
 * POST /mcp, JSON-RPC method tools/list). The browser path is /api/v1/mcp; the
 * web BFF forwards it to the gateway with the user's token, so the list holds
 * only the tools whose duty the user holds (AC-47.1).
 */
export const MCP_PATH = "/api/v1/mcp";

export const APPROVAL_MODES = ["none", "suggest", "approve_before_commit"] as const;
export type ApprovalMode = (typeof APPROVAL_MODES)[number];

export const toolSchema = z.object({
  name: z.string(),
  description: z.string(),
  inputSchema: z.record(z.string(), z.unknown()),
  annotations: z.object({
    readOnlyHint: z.boolean(),
    destructiveHint: z.boolean(),
    idempotentHint: z.boolean(),
  }),
  "x-passage-duty": z.string(),
  "x-passage-approval": z.enum(APPROVAL_MODES),
});

export type McpTool = z.infer<typeof toolSchema>;

const listResponseSchema = z.union([
  z.object({ jsonrpc: z.literal("2.0"), result: z.object({ tools: z.array(toolSchema) }) }),
  z.object({ jsonrpc: z.literal("2.0"), error: z.object({ code: z.number(), message: z.string() }) }),
]);

export class McpError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "McpError";
    this.status = status;
  }
}

let nextId = 0;

/** tools/list for the signed-in user. Throws McpError on HTTP or JSON-RPC errors. */
export async function listTools(signal?: AbortSignal, fetchFn: typeof fetch = fetch): Promise<McpTool[]> {
  nextId += 1;
  const res = await fetchFn(MCP_PATH, {
    method: "POST",
    headers: { "content-type": "application/json", accept: "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: `catalog-${nextId}`, method: "tools/list" }),
    ...(signal ? { signal } : {}),
  });
  if (!res.ok) throw new McpError(`The tool catalog is unavailable (HTTP ${res.status}).`, res.status);
  const parsed = listResponseSchema.safeParse(await res.json());
  if (!parsed.success) throw new McpError("The tool catalog sent a response we couldn't read.", res.status);
  if ("error" in parsed.data) throw new McpError(parsed.data.error.message, res.status);
  return parsed.data.result.tools;
}

/** passage.<domain>.<entity>.<verb> -> "<domain>". */
export function toolDomain(name: string): string {
  return name.split(".")[1] ?? "";
}

export interface InputField {
  name: string;
  type: string;
  required: boolean;
  description: string;
}

/** Flattens the top level of a tool's JSON Schema for display. */
export function inputFields(schema: Record<string, unknown>): InputField[] {
  const props = schema.properties;
  if (typeof props !== "object" || props === null || Array.isArray(props)) return [];
  const required = new Set(Array.isArray(schema.required) ? schema.required.filter((r): r is string => typeof r === "string") : []);
  return Object.entries(props as Record<string, unknown>).map(([name, raw]) => {
    const def = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
    const type = Array.isArray(def.type) ? def.type.join(" | ") : typeof def.type === "string" ? def.type : "any";
    return { name, type, required: required.has(name), description: typeof def.description === "string" ? def.description : "" };
  });
}
