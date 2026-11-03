// The hub's client for the migration service's outbound mapping API (wave 10, slice S5):
//
//   POST /v1/integration/outbound-mappings/{id}:render   {event}  ->  {body, idempotencyKey, refs}
//        422 MIG-XREF-UNRESOLVED when a referenced Passage record has no legacy key yet
//   POST /v1/integration/xref:record  {outboundMappingId, refs, legacyKey, eventId}  ->  204
//        refs: the subject, plus (migration slice 23) one ref per element of a pushed collection, each with its legacyKey
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

/** A lookup table version a render read (migration slice 20): the audit trail from a pushed body to the entries behind it. */
export interface LookupUse {
  name: string;
  table: string;
  version: number;
}

export interface Rendered {
  body: unknown;
  idempotencyKey: string;
  refs: unknown;
  /** The lookup table versions the render read; empty when the mapping has no lookups. */
  lookups: LookupUse[];
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
  /**
   * The legacy keys of the elements of a pushed collection, by position. Sent in the same call as the header's key, as
   * the keys of the render's element refs (the refs that carry an `index`) once the migration service renders them
   * (wave 10 slice 23: `refs` may then hold several refs, each element ref with `legacyKey`). Without element refs in
   * the render nothing is sent for them, so a migration service without that slice sees the single-subject call it knows.
   */
  lines?: { index: number; legacyKey: string }[];
  /** The collection (the element refs' `collection`, null when they carry none) the `lines` belong to; other element refs get no key. */
  linesCollection?: string | null;
  actor?: string;
}

/** An element ref's collection: its `collection`, or null when it carries none; undefined when the ref is not an element ref (no index). */
function elementCollection(r: unknown): string | null | undefined {
  if (typeof r !== "object" || r === null) return undefined;
  const o = r as Record<string, unknown>;
  if (typeof o["index"] !== "number") return undefined;
  return typeof o["collection"] === "string" ? o["collection"] : null;
}

/**
 * The render's refs with each element's legacy key added: the multi-ref body of xref:record. Refs without an index are
 * the subject; only the element refs of `collection` get a key, so the lines of one collection never key another's.
 */
export function withLineKeys(refs: unknown, lines: { index: number; legacyKey: string }[], collection: string | null): { refs: unknown; recorded: number } {
  if (!Array.isArray(refs)) return { refs, recorded: 0 };
  const byIndex = new Map(lines.map((l) => [l.index, l.legacyKey]));
  let recorded = 0;
  const out = refs.map((r) => {
    if (elementCollection(r) !== collection) return r;
    const legacyKey = byIndex.get((r as Record<string, number>)["index"] as number);
    if (legacyKey === undefined) return r;
    recorded++;
    return { ...(r as Record<string, unknown>), legacyKey };
  });
  return { refs: out, recorded };
}

/** The element refs the line keys are for: their collection and how many there are; a failure when that cannot be told. */
export type ElementRefs = { ok: true; collection: string | null; count: number } | { ok: false; reason: string };

/**
 * The element refs (refs with an index) of the collection the line keys belong to: null when the render named none, so
 * no line keys can be recorded. `bodyArray` is the body array the keys were matched against (lineCorrelation.bodyPath),
 * when known. With elements of several collections the keys can only be placed when `bodyArray` names one of them.
 */
export function elementRefs(refs: unknown, bodyArray?: string): ElementRefs | null {
  if (!Array.isArray(refs)) return null;
  const counts = new Map<string | null, number>();
  for (const r of refs) {
    const c = elementCollection(r);
    if (c !== undefined) counts.set(c, (counts.get(c) ?? 0) + 1);
  }
  if (counts.size === 0) return null;
  const names = [...counts.keys()].map((c) => c ?? "(unnamed)").join(", ");
  if (bodyArray !== undefined && counts.has(bodyArray)) return { ok: true, collection: bodyArray, count: counts.get(bodyArray) as number };
  if (counts.size === 1) {
    const [[collection, count]] = [...counts] as [[string | null, number]];
    if (bodyArray === undefined || collection === null) return { ok: true, collection, count };
    return { ok: false, reason: `the render's element refs are of the collection ${collection}, not ${bodyArray} (lineCorrelation.bodyPath)` };
  }
  return {
    ok: false,
    reason: bodyArray === undefined
      ? `the render names elements of several collections (${names}); set lineCorrelation so its bodyPath names the one lineKeyPath reads`
      : `the render names elements of the collections ${names}, not ${bodyArray} (lineCorrelation.bodyPath)`,
  };
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
const MAX_LOOKUPS = 200;

/** The render's `lookups`, kept to well-formed entries (a bounded audit record, never trusted as input). */
export function lookupsOf(raw: unknown): LookupUse[] {
  if (!Array.isArray(raw)) return [];
  const out: LookupUse[] = [];
  for (const e of raw) {
    if (out.length >= MAX_LOOKUPS) break;
    const o = (typeof e === "object" && e !== null ? e : {}) as Record<string, unknown>;
    if (typeof o["name"] === "string" && typeof o["table"] === "string" && typeof o["version"] === "number" && Number.isInteger(o["version"])) {
      out.push({ name: o["name"].slice(0, 200), table: o["table"].slice(0, 200), version: o["version"] });
    }
  }
  return out;
}

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
          return { ok: true, rendered: { body: r["body"], idempotencyKey: String(r["idempotencyKey"] ?? ""), refs: r["refs"] ?? null, lookups: lookupsOf(r["lookups"]) } };
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
    async recordXref({ tenantId, legalEntityId, mappingId, refs, legacyKey, eventId, lines, linesCollection, actor }) {
      const sent = lines === undefined || lines.length === 0 ? refs : withLineKeys(refs, lines, linesCollection ?? null).refs;
      const out = await post("/v1/integration/xref:record", tenantId, legalEntityId, actor, { outboundMappingId: mappingId, refs: sent, legacyKey, eventId });
      if (out === null) {
        return { ok: false, status: null, code: null, detail: "the migration service did not answer", retryable: true };
      }
      if (out.status >= 200 && out.status < 300) return { ok: true };
      const p = problem(out.json);
      return { ok: false, status: out.status, code: p.code, detail: p.detail, retryable: migrationRetryable(out.status) };
    },
  };
}
