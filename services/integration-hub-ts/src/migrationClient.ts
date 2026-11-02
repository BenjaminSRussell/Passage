// The hub's client for the migration service's outbound mapping API (wave 10, slice S5):
//
//   POST /v1/integration/outbound-mappings/{id}:render   {event}  ->  {body, idempotencyKey, refs}
//        422 MIG-XREF-UNRESOLVED when a referenced Passage record has no legacy key yet
//   POST /v1/integration/xref:record  {outboundMappingId, refs, legacyKey, eventId}  ->  204
//
// The client is an interface so the rest-push flow is tested against a fake. The HTTP
// implementation authenticates with a service token (signer `integration-hub`) and keeps
// transport failures apart from refusals: a refusal (4xx) is a configuration or data problem
// that retrying does not fix; a 5xx or no answer is retried.

import type { HubEvent } from "./dispatcher.ts";
import { HUB_ACTOR, type ServiceTokenSigner } from "./serviceToken.ts";

export interface RenderInput {
  tenantId: string;
  legalEntityId: string | null;
  mappingId: string;
  /** The CloudEvent exactly as the hub received it. */
  event: Record<string, unknown>;
  /** Who the call is made for: the user of a test send. Absent for background deliveries, which sign as service:integration-hub
   *  (the producing service's actor is never forwarded: the verifier refuses an unregistered service: actor). */
  actor?: string;
}

export interface Rendered {
  body: unknown;
  idempotencyKey: string;
  refs: unknown;
}

export type RenderOutcome =
  | { ok: true; rendered: Rendered }
  /** `retryable` is true for a 5xx or no answer; `code` is the problem code (for example MIG-XREF-UNRESOLVED). */
  | { ok: false; status: number | null; code: string | null; detail: string; retryable: boolean };

export interface XrefInput {
  tenantId: string;
  legalEntityId: string | null;
  mappingId: string;
  refs: unknown;
  legacyKey: string;
  eventId: string;
  actor?: string;
}

/** `code` is the problem code (MIG-XREF-EVENT-CONFLICT, MIG-XREF-CONFLICT, MIG-XREF-REF-MISMATCH, ...) when the service sent one. */
export type XrefOutcome = { ok: true } | { ok: false; status: number | null; code: string | null; detail: string; retryable: boolean };

/** The migration service's own codes the hub reacts to. */
export const CODE_XREF_EVENT_CONFLICT = "MIG-XREF-EVENT-CONFLICT";

/**
 * Whether a refusal from the migration service is worth another attempt: no answer, a 5xx, 429 and 408 are
 * transient; 401 is a signing-key or clock problem an operator fixes while the 8-step schedule is still running.
 * Every other 4xx (403 MIG-OUTBOUND-LEGAL-ENTITY, 404, 409, 422) is a data or configuration answer that repeating does not change.
 */
export function migrationRetryable(status: number | null): boolean {
  return status === null || status >= 500 || status === 429 || status === 408 || status === 401;
}

export interface MigrationClient {
  render(input: RenderInput): Promise<RenderOutcome>;
  recordXref(input: XrefInput): Promise<XrefOutcome>;
}

/** The CloudEvent the hub renders: the same envelope the webhook body carries, plus the Passage extensions it kept. */
export function cloudEventOf(event: HubEvent, source = "/passage/integration-hub"): Record<string, unknown> {
  return {
    specversion: "1.0",
    id: event.eventId,
    source,
    type: event.type,
    time: new Date(event.occurredAt * 1000).toISOString(),
    subject: event.aggregateId,
    passagesequence: event.sequence,
    ...(event.legalEntityId ? { legalentityid: event.legalEntityId } : {}),
    data: event.data,
  };
}

const MAX_DETAIL = 500;

export const RENDER_ROUTE_MISSING =
  "the migration service has no outbound render route (POST /v1/integration/outbound-mappings/{id}:render, wave 10 slice S5); nothing was pushed";

export function httpMigrationClient(options: {
  baseUrl: string;
  signer: ServiceTokenSigner;
  fetch?: typeof fetch;
  timeoutMs?: number;
}): MigrationClient {
  const doFetch = options.fetch ?? fetch;
  const base = options.baseUrl.replace(/\/+$/, "");
  const timeoutMs = options.timeoutMs ?? 15_000;

  async function post(path: string, tenantId: string, legalEntityId: string | null, actor: string | undefined, body: unknown): Promise<{ status: number; json: unknown } | null> {
    try {
      const res = await doFetch(base + path, {
        method: "POST",
        redirect: "manual",
        headers: {
          "content-type": "application/json",
          "x-passage-service-token": options.signer.mintForMigration({ tenantId, legalEntityId, actor: actor ?? HUB_ACTOR }),
        },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(timeoutMs),
      });
      const text = await res.text();
      let json: unknown = null;
      try {
        json = text === "" ? null : JSON.parse(text);
      } catch {
        json = { detail: text.slice(0, MAX_DETAIL) };
      }
      return { status: res.status, json };
    } catch {
      return null;
    }
  }

  const problem = (json: unknown): { code: string | null; detail: string } => {
    const p = (typeof json === "object" && json !== null ? json : {}) as Record<string, unknown>;
    return {
      code: typeof p["code"] === "string" ? p["code"] : null,
      detail: String(p["detail"] ?? p["title"] ?? "").slice(0, MAX_DETAIL),
    };
  };

  return {
    async render({ tenantId, legalEntityId, mappingId, event, actor }) {
      const out = await post(`/v1/integration/outbound-mappings/${encodeURIComponent(mappingId)}:render`, tenantId, legalEntityId, actor, { event });
      if (out === null) {
        return { ok: false, status: null, code: null, detail: "the migration service did not answer", retryable: true };
      }
      if (out.status === 200) {
        const r = out.json as Record<string, unknown>;
        if (typeof r === "object" && r !== null && "body" in r) {
          return { ok: true, rendered: { body: r["body"], idempotencyKey: String(r["idempotencyKey"] ?? ""), refs: r["refs"] ?? null } };
        }
        return { ok: false, status: 200, code: null, detail: "the render answer has no body", retryable: false };
      }
      const p = problem(out.json);
      if (out.status === 404 && p.code === null) {
        // No problem code: the route itself is missing (the outbound mapping API of wave 10 slice S5 is not deployed).
        return { ok: false, status: 404, code: null, detail: RENDER_ROUTE_MISSING, retryable: false };
      }
      return { ok: false, status: out.status, ...p, retryable: migrationRetryable(out.status) };
    },
    async recordXref({ tenantId, legalEntityId, mappingId, refs, legacyKey, eventId, actor }) {
      const out = await post("/v1/integration/xref:record", tenantId, legalEntityId, actor, { outboundMappingId: mappingId, refs, legacyKey, eventId });
      if (out === null) {
        return { ok: false, status: null, code: null, detail: "the migration service did not answer", retryable: true };
      }
      if (out.status >= 200 && out.status < 300) return { ok: true };
      const p = problem(out.json);
      return { ok: false, status: out.status, code: p.code, detail: p.detail, retryable: migrationRetryable(out.status) };
    },
  };
}
