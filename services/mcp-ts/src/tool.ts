import type { Catalog } from "./catalog.js";

/** Tool model for the Depot MCP gateway (FR-47.1). Pure functions, no I/O. */

export const APPROVAL_MODES = ["none", "suggest", "approve_before_commit"] as const;
export type ApprovalMode = (typeof APPROVAL_MODES)[number];

export const HTTP_METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"] as const;
export type HttpMethod = (typeof HTTP_METHODS)[number];

export type JsonObject = Record<string, unknown>;

export interface LocalContext {
  args: JsonObject;
  duties: ReadonlySet<string>;
  catalog: Catalog;
}

export type LocalHandler = (ctx: LocalContext) => unknown | Promise<unknown>;

export class ToolInputError extends Error {}

export interface ToolDefinition {
  /** depot.<domain>.<entity>.<verb>, e.g. depot.ap.invoice.list */
  name: string;
  description: string;
  version: number;
  /** Absent for a local tool, which never reaches the Depot API. */
  method?: HttpMethod;
  path?: string;
  /** OpenAPI operationId (or report id). */
  sourceRef: string;
  inputSchema: JsonObject;
  outputSchema: JsonObject;
  /** OPA duty the caller must hold to see and call the tool (x-depot-duty). */
  requiredDuty: string;
  /** x-depot-approval */
  approvalMode: ApprovalMode;
  readOnly: boolean;
  idempotent: boolean;
  /** Answered inside the gateway instead of by the Depot API; the caller's token is never forwarded. */
  local?: LocalHandler;
  /** Composite tools only: string arguments longer than their schema maxLength (in UTF-16 code units, as the Depot API counts) are refused before the call, and arguments the schema does not declare are dropped. */
  enforceMaxLength?: boolean;
}

/**
 * Three or four segments: depot.<domain>.<entity>.<verb>, or
 * depot.<domain>.<verb> for single-resource domains such as depot.knowledge.search.
 */
export const TOOL_NAME_PATTERN = /^depot(\.[a-z][a-z0-9_-]*){2,3}$/;

export function isObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function hasTenantProperty(schema: JsonObject): boolean {
  return isObject(schema.properties) && Object.prototype.hasOwnProperty.call(schema.properties, "tenant_id");
}

/** Returns every rule violation; an empty list means the definition is publishable. */
export function validateToolDefinition(t: ToolDefinition): string[] {
  const errors: string[] = [];
  if (!TOOL_NAME_PATTERN.test(t.name)) errors.push(`${t.name}: name must match depot.<domain>.<entity>.<verb>`);
  if (!Number.isInteger(t.version) || t.version < 1) errors.push(`${t.name}: version must be a positive integer`);
  if (t.local === undefined && (t.method === undefined || t.path === undefined)) errors.push(`${t.name}: method and path are required unless the tool is local`);
  if (t.method !== undefined && !HTTP_METHODS.includes(t.method)) errors.push(`${t.name}: unsupported method ${String(t.method)}`);
  if (t.path !== undefined && !t.path.startsWith("/")) errors.push(`${t.name}: path must start with /`);
  if (!t.sourceRef) errors.push(`${t.name}: sourceRef is required`);
  if (!t.requiredDuty) errors.push(`${t.name}: x-depot-duty is required`);
  if (!APPROVAL_MODES.includes(t.approvalMode)) errors.push(`${t.name}: unknown x-depot-approval`);
  if (t.method === "GET" && !t.readOnly) errors.push(`${t.name}: GET tools must be read-only`);
  if (!t.readOnly && t.approvalMode === "none") {
    errors.push(`${t.name}: write tools must be suggest or approve_before_commit`);
  }
  if (hasTenantProperty(t.inputSchema)) {
    errors.push(`${t.name}: inputSchema must not declare tenant_id; the tenant comes from the token`);
  }
  return errors;
}

/** The tenant is injected from the caller's token, never taken from the model. */
export function stripTenantId(schema: JsonObject): JsonObject {
  const out: JsonObject = { ...schema };
  if (isObject(schema.properties)) {
    const props: JsonObject = { ...schema.properties };
    delete props.tenant_id;
    out.properties = props;
  }
  if (Array.isArray(schema.required)) {
    out.required = schema.required.filter((r) => r !== "tenant_id");
  }
  return out;
}

export interface McpTool {
  name: string;
  description: string;
  inputSchema: JsonObject;
  annotations: { readOnlyHint: boolean; destructiveHint: boolean; idempotentHint: boolean };
  "x-depot-duty": string;
  "x-depot-approval": ApprovalMode;
}

/** The wire shape returned by tools/list. */
export function toMcpTool(t: ToolDefinition): McpTool {
  return {
    name: t.name,
    description: t.description,
    inputSchema: stripTenantId(t.inputSchema),
    annotations: {
      readOnlyHint: t.readOnly,
      destructiveHint: !t.readOnly,
      idempotentHint: t.idempotent,
    },
    "x-depot-duty": t.requiredDuty,
    "x-depot-approval": t.approvalMode,
  };
}
