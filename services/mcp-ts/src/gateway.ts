import type { Catalog } from "./catalog.js";
import { isObject, type JsonObject, type ToolDefinition } from "./tool.js";

/** FR-47.1: read tools return at most 200 rows per call. */
export const READ_PAGE_LIMIT = 200;
/** BR-47.6: 5xx and timeouts retry 3x with 1 s, 4 s, 16 s backoff. */
export const RETRY_DELAYS_MS = [1000, 4000, 16000] as const;

/** The invoking user. Agents hold no credentials of their own (FR-47.2). */
export interface Principal {
  userId: string;
  tenantId: string;
  duties: ReadonlySet<string>;
  /** Keycloak token exchanged for this user; forwarded, never stored. */
  userToken: string;
  runId?: string;
  traceparent?: string;
}

export interface UpstreamRequest {
  tool: ToolDefinition;
  args: JsonObject;
  headers: Record<string, string>;
}

export interface UpstreamResponse {
  status: number;
  body: unknown;
}

/** Calls the Passage API. The real implementation is createFetchUpstream. */
export interface Upstream {
  send(req: UpstreamRequest): Promise<UpstreamResponse>;
}

export interface AuditEntry {
  at: string;
  tenantId: string;
  userId: string;
  runId?: string;
  tool: string;
  outcome: "OK" | "DENIED" | "INVALID" | "UPSTREAM_ERROR";
  status: number;
  code?: string;
}

export interface AuditSink {
  record(entry: AuditEntry): Promise<void>;
}

export interface GatewayDeps {
  catalog: Catalog;
  upstream: Upstream;
  audit: AuditSink;
  now: () => Date;
  sleep: (ms: number) => Promise<void>;
}

/** Longest cursor the gateway forwards; upstream cursors are opaque tokens. */
export const MAX_CURSOR_LENGTH = 512;

export type CallResult =
  | { kind: "ok"; status: number; body: unknown; nextCursor?: string }
  | { kind: "error"; status: number; code: string; message: string };

async function fail(
  deps: GatewayDeps,
  p: Principal,
  tool: string,
  outcome: AuditEntry["outcome"],
  status: number,
  code: string,
  message: string,
): Promise<CallResult> {
  const entry: AuditEntry = {
    at: deps.now().toISOString(),
    tenantId: p.tenantId,
    userId: p.userId,
    tool,
    outcome,
    status,
    code,
  };
  if (p.runId !== undefined) entry.runId = p.runId;
  await deps.audit.record(entry);
  return { kind: "error", status, code, message };
}

/**
 * Runs one tool call for a principal. Order matters: the tool must exist, the
 * caller must hold the tool's duty, and only then is the user's token forwarded.
 * A denial never reaches the upstream API.
 */
export async function callTool(deps: GatewayDeps, p: Principal, name: string, rawArgs: unknown): Promise<CallResult> {
  const tool = deps.catalog.find(name);
  if (!tool) return fail(deps, p, name, "INVALID", 404, "MCP-0404", `unknown tool ${name}`);
  const args: JsonObject = isObject(rawArgs) ? { ...rawArgs } : {};
  if ("tenant_id" in args) {
    return fail(deps, p, name, "INVALID", 400, "MCP-0400", "tenant_id comes from your token and may not be supplied");
  }
  if (!p.duties.has(tool.requiredDuty)) {
    return fail(
      deps,
      p,
      name,
      "DENIED",
      403,
      "AGT-0203",
      `you do not hold duty ${tool.requiredDuty} required by ${tool.name}`,
    );
  }
  const missing = missingRequired(tool, args);
  if (missing.length > 0) {
    // A missing path parameter would otherwise change the URL, and so the operation, upstream.
    return fail(deps, p, name, "INVALID", 400, "MCP-0422", `missing required argument ${missing.join(", ")}`);
  }
  if (tool.readOnly && hasProperty(tool, "limit")) {
    const requested = typeof args.limit === "number" && Number.isFinite(args.limit) ? Math.trunc(args.limit) : READ_PAGE_LIMIT;
    args.limit = Math.max(1, Math.min(requested, READ_PAGE_LIMIT));
  }
  if (tool.readOnly && "cursor" in args) {
    // The cursor is opaque: it is forwarded unchanged, but must be a bounded string.
    if (typeof args.cursor !== "string" || args.cursor.length === 0 || args.cursor.length > MAX_CURSOR_LENGTH) {
      return fail(deps, p, name, "INVALID", 400, "MCP-0400", `cursor must be a string of 1 to ${MAX_CURSOR_LENGTH} characters`);
    }
  }

  const headers: Record<string, string> = { authorization: `Bearer ${p.userToken}` };
  if (p.traceparent !== undefined) headers.traceparent = p.traceparent;
  if (p.runId !== undefined) headers["x-agent-run-id"] = p.runId;

  const attempt = () => deps.upstream.send({ tool, args, headers });
  let res: UpstreamResponse;
  try {
    // Non-idempotent tools are never retried: a retried commit could post twice.
    res = tool.idempotent ? await withRetry(attempt, deps.sleep) : await attempt();
  } catch {
    return fail(deps, p, name, "UPSTREAM_ERROR", 503, "AGT-0214", `tool ${name} is unavailable`);
  }
  if (res.status === 403) {
    // OPA at the API is authoritative: a 403 there is a denial, logged as such (AC-47.2).
    return fail(deps, p, name, "DENIED", 403, "AGT-0203", `the API refused ${name} for your permissions`);
  }
  if (res.status >= 400) {
    const retried = res.status >= 500 && tool.idempotent;
    const message = retried
      ? `tool ${name} failed with ${res.status} after ${RETRY_DELAYS_MS.length} retries`
      : `tool ${name} failed with ${res.status}; not retried`;
    return fail(deps, p, name, "UPSTREAM_ERROR", res.status, "AGT-0214", message);
  }
  await deps.audit.record({
    at: deps.now().toISOString(),
    tenantId: p.tenantId,
    userId: p.userId,
    tool: name,
    outcome: "OK",
    status: res.status,
    ...(p.runId !== undefined ? { runId: p.runId } : {}),
  });
  const nextCursor = tool.readOnly ? nextCursorOf(res.body) : undefined;
  return nextCursor === undefined
    ? { kind: "ok", status: res.status, body: res.body }
    : { kind: "ok", status: res.status, body: res.body, nextCursor };
}

/** Reads the upstream's continuation token, accepting the camelCase or snake_case field. */
export function nextCursorOf(body: unknown): string | undefined {
  if (!isObject(body)) return undefined;
  const candidate = body.nextCursor ?? body.next_cursor;
  return typeof candidate === "string" && candidate.length > 0 && candidate.length <= MAX_CURSOR_LENGTH ? candidate : undefined;
}

function missingRequired(tool: ToolDefinition, args: JsonObject): string[] {
  const required = Array.isArray(tool.inputSchema.required) ? tool.inputSchema.required : [];
  return required.filter(
    (r): r is string => typeof r === "string" && r !== "tenant_id" && (args[r] === undefined || args[r] === null || args[r] === ""),
  );
}

function hasProperty(tool: ToolDefinition, key: string): boolean {
  return isObject(tool.inputSchema.properties) && Object.prototype.hasOwnProperty.call(tool.inputSchema.properties, key);
}

/** Retries 5xx responses and thrown errors (timeouts); 4xx responses return at once. */
export async function withRetry(
  fn: () => Promise<UpstreamResponse>,
  sleep: (ms: number) => Promise<void>,
): Promise<UpstreamResponse> {
  let last: UpstreamResponse | undefined;
  let lastError: unknown;
  for (let attempt = 0; attempt <= RETRY_DELAYS_MS.length; attempt++) {
    if (attempt > 0) await sleep(RETRY_DELAYS_MS[attempt - 1] ?? 0);
    try {
      const res = await fn();
      if (res.status < 500) return res;
      last = res;
    } catch (err) {
      lastError = err;
    }
  }
  if (last) return last;
  throw lastError;
}
