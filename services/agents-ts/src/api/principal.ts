import type { IncomingHttpHeaders } from "node:http";
import { AgentError } from "../errors.js";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
/** The platform tenant; server-side only and never accepted from headers (kernel tenant middleware). */
const NIL_UUID = "00000000-0000-0000-0000-000000000000";

/**
 * The caller as the edge described it after the OPA decision (FR-47.2). The
 * agents API trusts `x-passage-tenant`, `x-passage-user` and `x-passage-duties`
 * only because the edge (Envoy) strips client copies and sets them from the
 * verified token, the same contract as the MCP gateway.
 */
export interface Principal {
  userId: string;
  tenantId: string;
  duties: ReadonlySet<string>;
}

/**
 * HTTP-level codes of this API, in the style of the gateway's MCP-04xx codes.
 * Business refusals use the PRD 9.47.18 codes (AGT-02xx) instead.
 */
export type ApiCode =
  | "AGT-0400" // malformed request
  | "AGT-0401" // the edge did not identify a tenant and user
  | "AGT-0403" // a tenant administrator acting on another tenant
  | "AGT-0404" // no such route or resource
  | "AGT-0405" // method not allowed on the route
  | "AGT-0409" // the action was already decided differently
  | "AGT-0413" // request body too large
  | "AGT-0500"; // unexpected error, never echoed

/** An error the HTTP layer maps to a problem response (RFC 9457). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: ApiCode | AgentError["code"], message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export function isUuid(s: string): boolean {
  return UUID.test(s);
}

function header(h: IncomingHttpHeaders, name: string): string | undefined {
  const v = h[name];
  return Array.isArray(v) ? v[0] : v;
}

/**
 * Reads the caller from the edge headers. A missing, malformed or nil tenant,
 * or a missing user, is 401 (the kernel tenant middleware's rule). Ids are
 * lowercased so one caller always compares equal to itself.
 */
export function principalFromHeaders(h: IncomingHttpHeaders): Principal {
  const tenantId = header(h, "x-passage-tenant")?.trim().toLowerCase();
  const userId = header(h, "x-passage-user")?.trim().toLowerCase();
  if (tenantId === undefined || !isUuid(tenantId) || tenantId === NIL_UUID || userId === undefined || !isUuid(userId)) {
    throw new ApiError(401, "AGT-0401", "the edge did not identify a tenant and user");
  }
  const duties = new Set(
    (header(h, "x-passage-duties") ?? "")
      .split(",")
      .map((d) => d.trim())
      .filter((d) => d !== ""),
  );
  return { userId, tenantId, duties };
}

/** 403 AGT-0203 (9.47.18) when the caller lacks the duty. */
export function requireDuty(principal: Principal, duty: string, subject: string): void {
  if (!principal.duties.has(duty)) {
    throw new ApiError(403, "AGT-0203", `I cannot do that: you do not hold duty ${duty} required by ${subject}.`);
  }
}

/** Maps domain errors to HTTP semantics; anything unknown is a 500 that reveals nothing. */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  if (err instanceof AgentError) {
    const status = err.code === "AGT-0203" || err.code === "AGT-0209" ? 403 : 422;
    return new ApiError(status, err.code, err.message);
  }
  return new ApiError(500, "AGT-0500", "unexpected error");
}

/** The body as a JSON object whose keys are all in `allowed` (the schemas set additionalProperties: false). */
export function asObject(raw: unknown, allowed: readonly string[]): Record<string, unknown> {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    throw new ApiError(400, "AGT-0400", "the body must be a JSON object");
  }
  const extra = Object.keys(raw).filter((k) => !allowed.includes(k));
  if (extra.length > 0) throw new ApiError(400, "AGT-0400", `unknown field ${extra[0]}`);
  return raw as Record<string, unknown>;
}
