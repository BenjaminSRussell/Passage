/** Tool model for the Passage MCP gateway (FR-47.1). Pure functions, no I/O. */

export const APPROVAL_MODES = ["none", "suggest", "approve_before_commit"] as const;
export type ApprovalMode = (typeof APPROVAL_MODES)[number];

export const HTTP_METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"] as const;
export type HttpMethod = (typeof HTTP_METHODS)[number];

export type JsonObject = Record<string, unknown>;

export interface ToolDefinition {
  /** passage.<domain>.<entity>.<verb>, e.g. passage.ap.invoice.list */
  name: string;
  description: string;
  version: number;
  method: HttpMethod;
  path: string;
  /** OpenAPI operationId (or report id). */
  sourceRef: string;
  inputSchema: JsonObject;
  outputSchema: JsonObject;
  /** OPA duty the caller must hold to see and call the tool (x-passage-duty). */
  requiredDuty: string;
  /** x-passage-approval */
  approvalMode: ApprovalMode;
  readOnly: boolean;
  idempotent: boolean;
}

/**
 * Three or four segments: passage.<domain>.<entity>.<verb>, or
 * passage.<domain>.<verb> for single-resource domains such as passage.knowledge.search.
 */
export const TOOL_NAME_PATTERN = /^passage(\.[a-z][a-z0-9_-]*){2,3}$/;

export function isObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function hasTenantProperty(schema: JsonObject): boolean {
  return isObject(schema.properties) && Object.prototype.hasOwnProperty.call(schema.properties, "tenant_id");
}

/** Returns every rule violation; an empty list means the definition is publishable. */
export function validateToolDefinition(t: ToolDefinition): string[] {
  const errors: string[] = [];
  if (!TOOL_NAME_PATTERN.test(t.name)) errors.push(`${t.name}: name must match passage.<domain>.<entity>.<verb>`);
  if (!Number.isInteger(t.version) || t.version < 1) errors.push(`${t.name}: version must be a positive integer`);
  if (!HTTP_METHODS.includes(t.method)) errors.push(`${t.name}: unsupported method ${String(t.method)}`);
  if (!t.path.startsWith("/")) errors.push(`${t.name}: path must start with /`);
  if (!t.sourceRef) errors.push(`${t.name}: sourceRef is required`);
  if (!t.requiredDuty) errors.push(`${t.name}: x-passage-duty is required`);
  if (!APPROVAL_MODES.includes(t.approvalMode)) errors.push(`${t.name}: unknown x-passage-approval`);
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
  "x-passage-duty": string;
  "x-passage-approval": ApprovalMode;
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
    "x-passage-duty": t.requiredDuty,
    "x-passage-approval": t.approvalMode,
  };
}
