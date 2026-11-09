// The integration hub HTTP API (/v1/hub, FR-35.9, FR-35.10): webhook subscriptions,
// the delivery log, the dead-letter list, resume after a 410 pause and replay.
//
// Identity is the edge's, the same contract as the agents API: the gateway
// verifies the token, strips client copies and sets x-passage-tenant,
// x-passage-user and x-passage-duties. A missing, malformed or nil tenant or a
// missing user is 401; a missing duty is 403. Errors are RFC 9457 problems with
// a stable HUB-04xx code.

import { randomBytes, randomUUID } from "node:crypto";
import { createServer, type IncomingHttpHeaders, type IncomingMessage, type Server, type ServerResponse } from "node:http";

import {
  ClientIdTakenError,
  ClientRevokedError,
  ClientStore,
  quotaDefaults,
  validateClientId,
  validateOwner,
  validateScopes,
  type ApiClientPatch,
} from "./clients.ts";
import { validateFilter, MAX_EXPRESSION_LENGTH } from "./cesql.ts";
import { SOURCE_SYSTEMS, type CoexistenceStore, type SourceSystem } from "./coexistence.ts";
import { parseEvent } from "./consumer.ts";
import {
  CATALOG_KINDS,
  CatalogStateError,
  CatalogStore,
  CatalogVersionError,
  IdempotencyKeyReuseError,
  LIFECYCLES,
  MAX_SPEC_BYTES,
  MIN_DEPRECATION_MONTHS,
  validateCatalogName,
  validateOwnerService,
  type CatalogFilter,
  type CatalogKind,
  type CatalogPatch,
  type Lifecycle,
} from "./catalog.ts";
import { REPLAY_MAX_EVENTS } from "./dispatch.ts";
import { validatePattern } from "./filter.ts";
import type { Metrics } from "./metrics.ts";
import {
  BadCursorError,
  DELIVERY_STATUSES,
  type DeliveryStatus,
  FieldKeysMissingError,
  type HubStore,
  type SubscriptionPatch,
  VersionConflictError,
  WrongKindError,
} from "./pgStore.ts";
import { parseRestInput, parseRestPatch, REST_FIELDS, RestConfigError, sampleUrl, type RestConfig, type RestPusher } from "./restPush.ts";
import { UnsafeWebhookUrlError, validateWebhookUrl, type GuardOptions } from "./ssrf.ts";

export const DUTY_SUBSCRIPTION_WRITE = "hub.subscription.write";
export const DUTY_DELIVERY_READ = "hub.delivery.read";
/** Register, change and revoke API clients (new duty; enforced here, seeded by the main session). */
export const DUTY_CLIENT_WRITE = "hub.client.write";
/** Spend a client's quota: a service-only duty (never a person or role); no caller holds it until the edge quota check is built. */
export const DUTY_CLIENT_CONSUME = "hub.client.consume";

/** Publish, change, deprecate and retire catalog entries (new duty; enforced here, seeded by the main session). */
export const DUTY_CATALOG_WRITE = "hub.catalog.write";

export const MAX_BODY_BYTES = 64 * 1024;
/** A catalog publish carries a whole OpenAPI or AsyncAPI document. */
const MAX_CATALOG_BODY_BYTES = MAX_SPEC_BYTES + 16 * 1024;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const NIL_UUID = "00000000-0000-0000-0000-000000000000";
const RFC3339 = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)$/i;

export interface Principal {
  userId: string;
  tenantId: string;
  duties: ReadonlySet<string>;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly headers: Record<string, string>;

  constructor(status: number, code: string, message: string, headers: Record<string, string> = {}) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.headers = headers;
  }
}

function header(h: IncomingHttpHeaders, name: string): string | undefined {
  const v = h[name];
  return Array.isArray(v) ? v[0] : v;
}

/** Reads the caller from the edge headers (the kernel tenant middleware's rule: bad tenant or user is 401). */
export function principalFromHeaders(h: IncomingHttpHeaders): Principal {
  const tenantId = header(h, "x-passage-tenant")?.trim().toLowerCase();
  const userId = header(h, "x-passage-user")?.trim().toLowerCase();
  if (tenantId === undefined || !UUID.test(tenantId) || tenantId === NIL_UUID || userId === undefined || !UUID.test(userId)) {
    throw new ApiError(401, "HUB-0401", "the edge did not identify a tenant and user");
  }
  const duties = new Set(
    (header(h, "x-passage-duties") ?? "")
      .split(",")
      .map((d) => d.trim())
      .filter((d) => d !== ""),
  );
  return { userId, tenantId, duties };
}

/** The one legal entity the edge scoped the caller to (X-Passage-Legal-Entity), or undefined. */
export function legalEntityFromHeaders(h: IncomingHttpHeaders): string | undefined {
  const raw = (header(h, "x-passage-legal-entity") ?? header(h, "x-passage-legalentity"))?.trim().toLowerCase();
  return raw !== undefined && UUID.test(raw) && raw !== NIL_UUID ? raw : undefined;
}

export function requireDuty(principal: Principal, duty: string): void {
  if (!principal.duties.has(duty)) {
    throw new ApiError(403, "HUB-0403", `you do not hold duty ${duty}`);
  }
}

export interface HubApiDeps {
  store: HubStore;
  metrics?: Metrics;
  /** Guard options for validateWebhookUrl (resolver, http allowance); production uses the defaults. */
  guard?: GuardOptions;
  /** The API client registry; defaults to one over `store`. */
  clients?: ClientStore;
  /** The API and event catalog; defaults to one over `store`. */
  catalog?: CatalogStore;
  /** api_deprecation_months: the least notice between a deprecation and its sunset; defaults to 12, never below 12. */
  deprecationMonths?: number;
  /** The legacy coexistence adapter state; without it the coexistence routes answer 501 HUB-0502. */
  coexistence?: CoexistenceStore;
  /** Pushes rest-push subscriptions; without it creating one answers 501 HUB-0502. */
  rest?: RestPusher;
  /** Total events one replay may enqueue (BR-35.9); defaults to 1,000,000. */
  replayCap?: number;
  clock?: () => number;
  ping?: () => Promise<void>;
}

async function readBody(req: IncomingMessage, maxBytes: number = MAX_BODY_BYTES): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    const buf = typeof chunk === "string" ? Buffer.from(chunk) : (chunk as Buffer);
    size += buf.length;
    if (size > maxBytes) {
      throw new ApiError(413, "HUB-0413", `request body exceeds ${maxBytes / 1024} KiB`);
    }
    chunks.push(buf);
  }
  if (size === 0) {
    throw new ApiError(400, "HUB-0400", "a JSON body is required");
  }
  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    throw new ApiError(400, "HUB-0400", "body is not valid JSON");
  }
}

function object(raw: unknown, allowed: readonly string[]): Record<string, unknown> {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    throw new ApiError(400, "HUB-0400", "the body must be a JSON object");
  }
  const extra = Object.keys(raw).find((k) => !allowed.includes(k));
  if (extra !== undefined) {
    throw new ApiError(400, "HUB-0400", `unknown field ${extra}`);
  }
  return raw as Record<string, unknown>;
}

function send(res: ServerResponse, status: number, body: unknown, extra: Record<string, string> = {}): void {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
    "content-length": Buffer.byteLength(payload).toString(),
    ...extra,
  });
  res.end(payload);
}

const TITLES: Record<number, string> = {
  400: "Bad Request", 401: "Unauthorized", 403: "Forbidden", 404: "Not Found", 405: "Method Not Allowed",
  409: "Conflict", 429: "Too Many Requests", 413: "Content Too Large", 422: "Unprocessable Content", 500: "Internal Server Error", 501: "Not Implemented",
  503: "Service Unavailable",
};

function problem(res: ServerResponse, err: ApiError, path: string): void {
  const payload = JSON.stringify({
    type: `https://passage.dev/problems/${err.code}`,
    title: TITLES[err.status] ?? "Error",
    status: err.status,
    code: err.code,
    detail: err.message,
    instance: path,
  });
  res.writeHead(err.status, {
    "content-type": "application/problem+json",
    "cache-control": "no-store",
    "content-length": Buffer.byteLength(payload).toString(),
    "x-content-type-options": "nosniff",
    ...err.headers,
  });
  res.end(payload);
}

function limitParam(q: URLSearchParams): number {
  const raw = q.get("limit");
  if (raw === null) {
    return 50;
  }
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 1 || n > 200) {
    throw new ApiError(400, "HUB-0400", "limit must be an integer in 1..200");
  }
  return n;
}

function requireString(body: Record<string, unknown>, key: string, max: number): string {
  const v = body[key];
  if (typeof v !== "string" || v.trim() === "" || v.length > max) {
    throw new ApiError(400, "HUB-0400", `${key} must be a non-empty string of at most ${max} characters`);
  }
  return v.trim();
}

function scopesOf(v: unknown): string[] {
  if (!Array.isArray(v) || v.some((x) => typeof x !== "string")) {
    throw new ApiError(400, "HUB-0400", "scopes must be an array of 1 to 50 duty keys");
  }
  const scopes = [...new Set(v as string[])];
  validateScopes(scopes);
  return scopes;
}

function kindOf(v: unknown): CatalogKind {
  if (typeof v !== "string" || !CATALOG_KINDS.includes(v as CatalogKind)) {
    throw new ApiError(400, "HUB-0400", `kind must be one of ${CATALOG_KINDS.join(", ")}`);
  }
  return v as CatalogKind;
}

function optionalText(v: unknown, key: string, max: number): string {
  if (typeof v !== "string" || v.length > max) {
    throw new ApiError(400, "HUB-0400", `${key} must be a string of at most ${max} characters`);
  }
  return v.trim();
}

function versionOf(body: Record<string, unknown>): number {
  const v = body["version"];
  if (typeof v !== "number" || !Number.isInteger(v) || v < 1) {
    throw new ApiError(400, "HUB-0400", "version must be the entry's current version");
  }
  return v;
}

/** An optional integer field; RangeError (400) for anything else. */
function intOf(body: Record<string, unknown>, key: string): number | undefined {
  const v = body[key];
  if (v === undefined) return undefined;
  if (typeof v !== "number" || !Number.isInteger(v)) {
    throw new ApiError(400, "HUB-0400", `${key} must be an integer`);
  }
  return v;
}

/** The sandbox tenant (FR-35.8): a UUID other than the caller's own tenant; null clears it. */
function sandboxOf(v: unknown, tenantId: string): string | null | undefined {
  if (v === undefined || v === null) return v as null | undefined;
  if (typeof v !== "string" || !UUID.test(v) || v === NIL_UUID || v.toLowerCase() === tenantId) {
    throw new ApiError(400, "HUB-0400", "sandboxTenantId must be a tenant id other than your own");
  }
  return v.toLowerCase();
}

function patternsOf(body: Record<string, unknown>): string[] {
  const v = body["patterns"];
  if (!Array.isArray(v) || v.length < 1 || v.length > 50 || v.some((p) => typeof p !== "string")) {
    throw new ApiError(400, "HUB-0400", "patterns must be an array of 1 to 50 CloudEvents type patterns");
  }
  for (const p of v as string[]) {
    const bad = validatePattern(p);
    if (bad !== null) {
      throw new ApiError(400, "HUB-0400", `pattern ${JSON.stringify(p)}: ${bad}`);
    }
  }
  return [...new Set(v as string[])];
}

/** A CESQL filter: absent is undefined, null (patch only) removes it, text must parse. */
function filterOf(body: Record<string, unknown>, allowNull: boolean): string | null | undefined {
  const v = body["filter"];
  if (v === undefined) return undefined;
  if (v === null && allowNull) return null;
  if (typeof v !== "string") {
    throw new ApiError(400, "HUB-0400", `filter must be a CESQL expression of at most ${MAX_EXPRESSION_LENGTH} characters${allowNull ? " or null" : ""}`);
  }
  const bad = validateFilter(v);
  if (bad !== null) {
    throw new ApiError(400, "HUB-0400", `filter: ${bad}`);
  }
  return v;
}

/** A caller may grant a client only duties they hold themselves (no escalation through a client's scopes). */
function requireHeld(p: Principal, scopes: readonly string[]): void {
  const missing = scopes.filter((s) => !p.duties.has(s));
  if (missing.length > 0) {
    throw new ApiError(403, "HUB-0403", `you cannot grant duties you do not hold: ${missing.join(", ")}`);
  }
}

/** Seconds the previous signing secret keeps signing after a rotation: a day unless the caller says otherwise, at most a week. */
const DEFAULT_GRACE_SECONDS = 86_400;
const MAX_GRACE_SECONDS = 7 * 86_400;

const SUBSCRIPTIONS = "/v1/hub/subscriptions";
const ONE = new RegExp(`^${SUBSCRIPTIONS}/([0-9a-fA-F-]{36})(?::(resume|replay|test|rotate))?$`);
const COEXISTENCE = "/v1/hub/coexistence";
const CLIENTS = "/v1/hub/clients";
const CLIENT_ONE = new RegExp(`^${CLIENTS}/([0-9a-fA-F-]{36})(?::(revoke)|/(usage))?$`);
const CATALOG = "/v1/hub/catalog";
const CATALOG_ONE = new RegExp(`^${CATALOG}/([0-9a-fA-F-]{36})(?::(deprecate|retire)|/(spec))?$`);
const DELIVERY_REPLAY = /^\/v1\/hub\/deliveries\/([0-9a-fA-F-]{36}):replay$/;

export function createHubHandler(deps: HubApiDeps): (req: IncomingMessage, res: ServerResponse) => Promise<void> {
  const { store, metrics } = deps;
  const clients = deps.clients ?? new ClientStore(store);
  const catalog = deps.catalog ?? new CatalogStore(store);
  const configuredMonths = deps.deprecationMonths ?? MIN_DEPRECATION_MONTHS;
  const deprecationMonths = Number.isInteger(configuredMonths) ? Math.max(configuredMonths, MIN_DEPRECATION_MONTHS) : MIN_DEPRECATION_MONTHS;
  const clock = deps.clock ?? (() => Date.now() / 1000);

  const checkUrl = async (url: string): Promise<void> => {
    try {
      await validateWebhookUrl(url, deps.guard ?? {});
    } catch (err) {
      if (err instanceof UnsafeWebhookUrlError) {
        throw new ApiError(422, "HUB-0422", err.message);
      }
      throw err;
    }
  };

  async function route(req: IncomingMessage, url: URL): Promise<{ name: string; status: number; body: unknown; headers?: Record<string, string> }> {
    const path = url.pathname;
    const method = req.method ?? "GET";
    const allow = (methods: string): never => {
      throw new ApiError(405, "HUB-0405", "method not allowed", { allow: methods });
    };

    if (path === "/healthz") {
      return { name: "healthz", status: 200, body: { status: "ok" } };
    }
    if (path === "/readyz") {
      try {
        await deps.ping?.();
      } catch {
        throw new ApiError(503, "HUB-0503", "the database is not reachable");
      }
      return { name: "readyz", status: 200, body: { status: "ready" } };
    }

    if (path === COEXISTENCE || path === `${COEXISTENCE}:retry-parked`) {
      const retry = path !== COEXISTENCE;
      if (method !== (retry ? "POST" : "GET")) {
        return allow(retry ? "POST" : "GET");
      }
      const p = principalFromHeaders(req.headers);
      requireDuty(p, retry ? DUTY_SUBSCRIPTION_WRITE : DUTY_DELIVERY_READ);
      if (deps.coexistence === undefined) {
        throw new ApiError(501, "HUB-0502", "legacy coexistence is not configured on this hub");
      }
      if (retry) {
        const retried = await deps.coexistence.retryParked(p.tenantId, new Date(clock() * 1000));
        return { name: "retryParkedCoexistence", status: 200, body: { retried } };
      }
      const system = url.searchParams.get("sourceSystem");
      const company = url.searchParams.get("sourceCompany");
      if (system === null || !(SOURCE_SYSTEMS as readonly string[]).includes(system)) {
        throw new ApiError(400, "HUB-0400", `sourceSystem must be one of ${SOURCE_SYSTEMS.join(", ")}`);
      }
      if (company === null || company.length < 1 || company.length > 64) {
        throw new ApiError(400, "HUB-0400", "sourceCompany must be 1 to 64 characters");
      }
      return { name: "listCoexistence", status: 200, body: { items: await deps.coexistence.list(p.tenantId, system as SourceSystem, company) } };
    }

    if (path === `${CLIENTS}:consume`) {
      if (method !== "POST") {
        return allow("POST");
      }
      const p = principalFromHeaders(req.headers);
      requireDuty(p, DUTY_CLIENT_CONSUME);
      const body = object(await readBody(req), ["clientId", "cost"]);
      const clientId = requireString(body, "clientId", 64);
      const cost = body["cost"] === undefined ? 1 : body["cost"];
      if (typeof cost !== "number" || !Number.isInteger(cost) || cost < 1 || cost > 100_000) {
        throw new ApiError(400, "HUB-0400", "cost must be an integer in 1..100000");
      }
      const out = await clients.consume(p.tenantId, clientId, cost, clock() * 1000);
      switch (out.outcome) {
        case "unknown":
          throw new ApiError(404, "HUB-0404", "no such API client");
        case "revoked":
          throw new ApiError(403, "HUB-0403", "the API client is revoked");
        case "too_costly":
          throw new ApiError(422, "HUB-0422", `the request costs more than the client's burst of ${out.burst}`);
        case "throttled":
          throw new ApiError(429, "HUB-0113", `Quota exceeded; retry after ${out.retryAfterSeconds} s.`, {
            "retry-after": String(out.retryAfterSeconds), "ratelimit-limit": String(out.limitPerMin), "ratelimit-remaining": "0",
          });
        case "allowed":
          return {
            name: "consumeClientQuota", status: 200, body: { allowed: true, remaining: out.remaining, limitPerMin: out.limitPerMin, burst: out.burst },
            headers: { "ratelimit-limit": String(out.limitPerMin), "ratelimit-remaining": String(out.remaining) },
          };
      }
    }

    if (path === CLIENTS) {
      if (method === "POST") {
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_CLIENT_WRITE);
        const key = header(req.headers, "idempotency-key")?.trim();
        if (key !== undefined && key.length > 200) {
          throw new ApiError(400, "HUB-0400", "Idempotency-Key must be at most 200 characters");
        }
        const body = object(await readBody(req), ["clientId", "name", "scopes", "quotaPerMin", "burst", "sandboxTenantId", "ownerContact"]);
        const clientId = requireString(body, "clientId", 64);
        validateClientId(clientId);
        const scopes = scopesOf(body["scopes"]);
        requireHeld(p, scopes);
        const ownerContact = requireString(body, "ownerContact", 320);
        validateOwner(ownerContact);
        const { created, client } = await clients.create(p.tenantId, {
          clientId, name: requireString(body, "name", 200), scopes, ownerContact,
          quotaPerMin: intOf(body, "quotaPerMin") ?? quotaDefaults.quotaPerMin,
          burst: intOf(body, "burst") ?? quotaDefaults.burst,
          sandboxTenantId: sandboxOf(body["sandboxTenantId"], p.tenantId) ?? null,
          createdBy: p.userId, idempotencyKey: key === undefined || key === "" ? null : key,
        });
        return { name: "registerClient", status: created ? 201 : 200, body: client };
      }
      if (method === "GET") {
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_DELIVERY_READ);
        return { name: "listClients", status: 200, body: await clients.list(p.tenantId, limitParam(url.searchParams), url.searchParams.get("cursor")) };
      }
      return allow("GET, POST");
    }

    const client = CLIENT_ONE.exec(path);
    if (client !== null) {
      const id = (client[1] as string).toLowerCase();
      if (client[3] !== undefined) {
        if (method !== "GET") {
          return allow("GET");
        }
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_DELIVERY_READ);
        const raw = url.searchParams.get("minutes");
        const minutes = raw === null ? 60 : Number(raw);
        if (!Number.isInteger(minutes) || minutes < 1 || minutes > 1440) {
          throw new ApiError(400, "HUB-0400", "minutes must be an integer in 1..1440");
        }
        const rows = await clients.usage(p.tenantId, id, minutes, clock() * 1000);
        if (rows === null) {
          throw new ApiError(404, "HUB-0404", "no such API client");
        }
        return { name: "getClientUsage", status: 200, body: { minutes: rows } };
      }
      if (client[2] !== undefined) {
        if (method !== "POST") {
          return allow("POST");
        }
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_CLIENT_WRITE);
        const revoked = await clients.revoke(p.tenantId, id, p.userId);
        if (revoked === null) {
          throw new ApiError(404, "HUB-0404", "no such API client");
        }
        return { name: "revokeClient", status: 200, body: revoked };
      }
      if (method === "GET") {
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_DELIVERY_READ);
        const found = await clients.get(p.tenantId, id);
        if (found === null) {
          throw new ApiError(404, "HUB-0404", "no such API client");
        }
        return { name: "getClient", status: 200, body: found };
      }
      if (method === "PATCH") {
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_CLIENT_WRITE);
        const body = object(await readBody(req), ["version", "name", "scopes", "quotaPerMin", "burst", "sandboxTenantId", "ownerContact"]);
        const version = body["version"];
        if (typeof version !== "number" || !Number.isInteger(version) || version < 1) {
          throw new ApiError(400, "HUB-0400", "version must be the client's current version");
        }
        const patch: ApiClientPatch = { version };
        if (body["name"] !== undefined) patch.name = requireString(body, "name", 200);
        if (body["scopes"] !== undefined) {
          patch.scopes = scopesOf(body["scopes"]);
          // Scopes the client already has stay whoever holds them; only an addition is a grant.
          const current = await clients.get(p.tenantId, id);
          if (current === null) {
            throw new ApiError(404, "HUB-0404", "no such API client");
          }
          requireHeld(p, patch.scopes.filter((s) => !current.scopes.includes(s)));
        }
        const rate = intOf(body, "quotaPerMin");
        if (rate !== undefined) patch.quotaPerMin = rate;
        const burst = intOf(body, "burst");
        if (burst !== undefined) patch.burst = burst;
        if (body["sandboxTenantId"] !== undefined) patch.sandboxTenantId = sandboxOf(body["sandboxTenantId"], p.tenantId) ?? null;
        if (body["ownerContact"] !== undefined) {
          patch.ownerContact = requireString(body, "ownerContact", 320);
          validateOwner(patch.ownerContact);
        }
        const updated = await clients.patch(p.tenantId, id, patch, p.userId);
        if (updated === null) {
          throw new ApiError(404, "HUB-0404", "no such API client");
        }
        return { name: "updateClient", status: 200, body: updated };
      }
      return allow("GET, PATCH");
    }

    if (path === CATALOG) {
      if (method === "POST") {
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_CATALOG_WRITE);
        const key = header(req.headers, "idempotency-key")?.trim();
        if (key !== undefined && key.length > 200) {
          throw new ApiError(400, "HUB-0400", "Idempotency-Key must be at most 200 characters");
        }
        const body = object(await readBody(req, MAX_CATALOG_BODY_BYTES), ["kind", "name", "specVersion", "title", "summary", "ownerService", "spec"]);
        const kind = kindOf(body["kind"]);
        const name = requireString(body, "name", 100);
        validateCatalogName(name);
        const ownerService = requireString(body, "ownerService", 64);
        validateOwnerService(ownerService);
        const spec = body["spec"];
        if (typeof spec !== "object" || spec === null || Array.isArray(spec)) {
          throw new ApiError(400, "HUB-0400", "spec must be an OpenAPI or AsyncAPI document as a JSON object");
        }
        const { created, entry } = await catalog.publish(p.tenantId, {
          kind, name, ownerService, specVersion: requireString(body, "specVersion", 20), title: requireString(body, "title", 200),
          summary: body["summary"] === undefined ? "" : optionalText(body["summary"], "summary", 2000),
          spec: spec as Record<string, unknown>, createdBy: p.userId, idempotencyKey: key === undefined || key === "" ? null : key,
        });
        return { name: "publishCatalogEntry", status: created ? 201 : 200, body: entry };
      }
      if (method === "GET") {
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_DELIVERY_READ);
        const q = url.searchParams;
        const filter: CatalogFilter = {};
        const kind = q.get("kind");
        if (kind !== null) filter.kind = kindOf(kind);
        const lifecycle = q.get("lifecycle");
        if (lifecycle !== null) {
          if (!LIFECYCLES.includes(lifecycle as Lifecycle)) {
            throw new ApiError(400, "HUB-0400", `lifecycle must be one of ${LIFECYCLES.join(", ")}`);
          }
          filter.lifecycle = lifecycle as Lifecycle;
        }
        const name = q.get("name");
        if (name !== null && name !== "") filter.name = name;
        const text = q.get("q")?.trim();
        if (text !== undefined && text !== "") {
          if (text.length > 100) {
            throw new ApiError(400, "HUB-0400", "q must be at most 100 characters");
          }
          filter.q = text;
        }
        return { name: "listCatalog", status: 200, body: await catalog.list(p.tenantId, filter, limitParam(q), q.get("cursor")) };
      }
      return allow("GET, POST");
    }

    const entryMatch = CATALOG_ONE.exec(path);
    if (entryMatch !== null) {
      const id = (entryMatch[1] as string).toLowerCase();
      const missing = (): never => {
        throw new ApiError(404, "HUB-0404", "no such catalog entry");
      };
      if (entryMatch[3] !== undefined) {
        if (method !== "GET") {
          return allow("GET");
        }
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_DELIVERY_READ);
        const doc = await catalog.spec(p.tenantId, id);
        return doc === null ? missing() : { name: "getCatalogSpec", status: 200, body: doc };
      }
      if (entryMatch[2] !== undefined) {
        if (method !== "POST") {
          return allow("POST");
        }
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_CATALOG_WRITE);
        const nowDate = new Date(clock() * 1000);
        if (entryMatch[2] === "deprecate") {
          const body = object(await readBody(req), ["version", "sunsetAt", "replacedBy"]);
          const sunset = body["sunsetAt"];
          if (typeof sunset !== "string" || !RFC3339.test(sunset)) {
            throw new ApiError(400, "HUB-0400", "sunsetAt must be an RFC 3339 timestamp");
          }
          const replacedBy = body["replacedBy"];
          if (replacedBy !== undefined && replacedBy !== null && (typeof replacedBy !== "string" || !UUID.test(replacedBy))) {
            throw new ApiError(400, "HUB-0400", "replacedBy must be the id of a catalog entry");
          }
          const out = await catalog.deprecate(
            p.tenantId, id, { version: versionOf(body), sunsetAt: new Date(sunset), replacedBy: typeof replacedBy === "string" ? replacedBy.toLowerCase() : null },
            deprecationMonths, nowDate, p.userId,
          );
          return out === null ? missing() : { name: "deprecateCatalogEntry", status: 200, body: out };
        }
        const body = object(await readBody(req), ["version"]);
        const out = await catalog.retire(p.tenantId, id, versionOf(body), nowDate, p.userId);
        return out === null ? missing() : { name: "retireCatalogEntry", status: 200, body: out };
      }
      if (method === "GET") {
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_DELIVERY_READ);
        const found = await catalog.get(p.tenantId, id);
        return found === null ? missing() : { name: "getCatalogEntry", status: 200, body: found };
      }
      if (method === "PATCH") {
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_CATALOG_WRITE);
        const body = object(await readBody(req), ["version", "title", "summary", "ownerService"]);
        const patch: CatalogPatch = { version: versionOf(body) };
        if (body["title"] !== undefined) patch.title = requireString(body, "title", 200);
        if (body["summary"] !== undefined) patch.summary = optionalText(body["summary"], "summary", 2000);
        if (body["ownerService"] !== undefined) {
          patch.ownerService = requireString(body, "ownerService", 64);
          validateOwnerService(patch.ownerService);
        }
        const out = await catalog.patch(p.tenantId, id, patch, p.userId);
        return out === null ? missing() : { name: "updateCatalogEntry", status: 200, body: out };
      }
      return allow("GET, PATCH");
    }

    if (path === SUBSCRIPTIONS) {
      if (method === "POST") {
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_SUBSCRIPTION_WRITE);
        if (!store.canSeal) {
          throw new FieldKeysMissingError();
        }
        const key = header(req.headers, "idempotency-key")?.trim();
        if (key !== undefined && key.length > 200) {
          throw new ApiError(400, "HUB-0400", "Idempotency-Key must be at most 200 characters");
        }
        const body = object(await readBody(req), ["kind", "name", "url", "patterns", "filter", "secret", ...REST_FIELDS]);
        const kind = body["kind"] ?? "webhook";
        if (kind !== "webhook" && kind !== "rest-push") {
          throw new ApiError(400, "HUB-0400", "kind must be webhook or rest-push");
        }
        const name = requireString(body, "name", 200);
        const target = requireString(body, "url", 2048);
        const patterns = patternsOf(body);
        const filter = filterOf(body, false) ?? null;
        if (kind === "rest-push") {
          if (body["secret"] !== undefined) {
            throw new ApiError(400, "HUB-0400", "a rest-push subscription has no signing secret; send auth instead");
          }
          if (deps.rest === undefined) {
            throw new ApiError(501, "HUB-0502", "rest-push is not configured on this hub (MIGRATION_URL and the service signing key)");
          }
          const input = parseRestInput(body, target);
          await checkUrl(sampleUrl(target));
          if (input.auth.tokenUrl !== undefined) {
            await checkUrl(input.auth.tokenUrl);
          }
          const { credentials, ...config } = input;
          const { subscription, created } = await store.createSubscription(p.tenantId, {
            name, url: target, secret: "", patterns, filter, createdBy: p.userId, idempotencyKey: key === undefined || key === "" ? null : key,
            rest: { config, credentials },
          });
          return { name: "createSubscription", status: created ? 201 : 200, body: subscription };
        }
        for (const f of REST_FIELDS) {
          if (body[f] !== undefined) {
            throw new ApiError(400, "HUB-0400", `${f} applies to rest-push subscriptions only`);
          }
        }
        let secret: string;
        if (body["secret"] === undefined) {
          secret = `whsec_${randomBytes(24).toString("base64url")}`;
        } else {
          secret = requireString(body, "secret", 256);
          if (secret.length < 16) {
            throw new ApiError(400, "HUB-0400", "secret must be at least 16 characters");
          }
        }
        await checkUrl(target);
        const { subscription, created } = await store.createSubscription(p.tenantId, {
          name, url: target, secret, patterns, filter, createdBy: p.userId, idempotencyKey: key === undefined || key === "" ? null : key,
        });
        // The secret is shown once, on the response that created the subscription.
        return { name: "createSubscription", status: created ? 201 : 200, body: created ? { ...subscription, secret } : subscription };
      }
      if (method === "GET") {
        const p = principalFromHeaders(req.headers);
        requireDuty(p, DUTY_DELIVERY_READ);
        return { name: "listSubscriptions", status: 200, body: await store.listSubscriptions(p.tenantId, limitParam(url.searchParams), url.searchParams.get("cursor")) };
      }
      return allow("GET, POST");
    }

    const one = ONE.exec(path);
    if (one !== null) {
      const id = (one[1] as string).toLowerCase();
      const action = one[2];
      if (!UUID.test(id)) {
        throw new ApiError(404, "HUB-0404", "no such subscription");
      }
      if (action === undefined) {
        if (method === "GET") {
          const p = principalFromHeaders(req.headers);
          requireDuty(p, DUTY_DELIVERY_READ);
          const sub = await store.getSubscription(p.tenantId, id);
          if (sub === null) {
            throw new ApiError(404, "HUB-0404", "no such subscription");
          }
          return { name: "getSubscription", status: 200, body: sub };
        }
        if (method === "PATCH") {
          const p = principalFromHeaders(req.headers);
          requireDuty(p, DUTY_SUBSCRIPTION_WRITE);
          const body = object(await readBody(req), ["version", "name", "url", "patterns", "filter", ...REST_FIELDS]);
          const version = body["version"];
          if (typeof version !== "number" || !Number.isInteger(version) || version < 1) {
            throw new ApiError(400, "HUB-0400", "version must be the subscription's current version");
          }
          const patch: SubscriptionPatch = { version };
          if (body["name"] !== undefined) patch.name = requireString(body, "name", 200);
          const current = await store.getTarget(p.tenantId, id);
          if (current === null) {
            throw new ApiError(404, "HUB-0404", "no such subscription");
          }
          if (body["url"] !== undefined) {
            patch.url = requireString(body, "url", 2048);
          }
          if (current.kind === "rest-push") {
            const template = patch.url ?? current.url;
            const rest = parseRestPatch(current.restConfig as RestConfig, template, body);
            if (rest !== null) {
              patch.rest = rest;
              if (rest.config.auth.tokenUrl !== undefined && body["auth"] !== undefined) await checkUrl(rest.config.auth.tokenUrl);
            }
            if (patch.url !== undefined) await checkUrl(sampleUrl(patch.url));
          } else {
            for (const f of REST_FIELDS) {
              if (body[f] !== undefined) {
                throw new ApiError(400, "HUB-0400", `${f} applies to rest-push subscriptions only`);
              }
            }
            if (patch.url !== undefined) await checkUrl(patch.url);
          }
          if (body["patterns"] !== undefined) patch.patterns = patternsOf(body);
          const filter = filterOf(body, true);
          if (filter !== undefined) patch.filter = filter;
          const sub = await store.patchSubscription(p.tenantId, id, patch, p.userId);
          if (sub === null) {
            throw new ApiError(404, "HUB-0404", "no such subscription");
          }
          return { name: "updateSubscription", status: 200, body: sub };
        }
        if (method === "DELETE") {
          const p = principalFromHeaders(req.headers);
          requireDuty(p, DUTY_SUBSCRIPTION_WRITE);
          if (!(await store.deleteSubscription(p.tenantId, id, p.userId))) {
            throw new ApiError(404, "HUB-0404", "no such subscription");
          }
          return { name: "deleteSubscription", status: 204, body: null };
        }
        return allow("GET, PATCH, DELETE");
      }

      if (method !== "POST") {
        return allow("POST");
      }
      const p = principalFromHeaders(req.headers);
      requireDuty(p, DUTY_SUBSCRIPTION_WRITE);
      if (action === "resume") {
        const sub = await store.resumeSubscription(p.tenantId, id, p.userId);
        if (sub === null) {
          throw new ApiError(404, "HUB-0404", "no such subscription");
        }
        return { name: "resumeSubscription", status: 200, body: sub };
      }
      if (action === "rotate") {
        const body = object(await readBody(req), ["version", "secret", "graceSeconds"]);
        const version = body["version"];
        if (typeof version !== "number" || !Number.isInteger(version) || version < 1) {
          throw new ApiError(400, "HUB-0400", "version must be the subscription's current version");
        }
        const grace = intOf(body, "graceSeconds") ?? DEFAULT_GRACE_SECONDS;
        if (grace < 0 || grace > MAX_GRACE_SECONDS) {
          throw new ApiError(400, "HUB-0400", `graceSeconds must be an integer in 0..${MAX_GRACE_SECONDS}`);
        }
        let secret: string;
        if (body["secret"] === undefined) {
          secret = `whsec_${randomBytes(24).toString("base64url")}`;
        } else {
          secret = requireString(body, "secret", 256);
          if (secret.length < 16) {
            throw new ApiError(400, "HUB-0400", "secret must be at least 16 characters");
          }
        }
        const rotated = await store.rotateSecret(p.tenantId, id, { version, secret, graceSeconds: grace }, p.userId);
        if (rotated === null) {
          throw new ApiError(404, "HUB-0404", "no such subscription");
        }
        // The new secret is shown once, on this response.
        return { name: "rotateSubscriptionSecret", status: 200, body: { ...rotated, secret } };
      }
      if (action === "test") {
        const body = object(await readBody(req), ["event", "send"]);
        const target = await store.getTarget(p.tenantId, id);
        if (target === null) {
          throw new ApiError(404, "HUB-0404", "no such subscription");
        }
        if (target.kind !== "rest-push" || deps.rest === undefined) {
          throw new ApiError(409, "HUB-0409", "test send applies to rest-push subscriptions on a hub with rest-push configured");
        }
        if (typeof body["event"] !== "object" || body["event"] === null || Array.isArray(body["event"])) {
          throw new ApiError(400, "HUB-0400", "event must be a CloudEvents JSON object");
        }
        // The render runs for the caller: their tenant, the legal entity the edge scoped them to, and their user id
        // as the token's actor. A sample naming another legal entity is refused rather than rendered under the hub's grant.
        const sample = body["event"] as Record<string, unknown>;
        const scoped = legalEntityFromHeaders(req.headers);
        if (sample["legalentityid"] !== undefined && (typeof sample["legalentityid"] !== "string" || sample["legalentityid"].toLowerCase() !== scoped)) {
          throw new ApiError(403, "HUB-0403", "the sample event's legalentityid must be the legal entity you are working in");
        }
        const parsed = parseEvent({
          topic: "test", partition: 0, offset: "0", key: null,
          value: Buffer.from(JSON.stringify({
            specversion: "1.0", id: randomUUID(), ...sample, tenantid: p.tenantId, actor: p.userId,
            ...(scoped === undefined ? {} : { legalentityid: scoped }),
          })),
        });
        if ("reason" in parsed) {
          throw new ApiError(400, "HUB-0400", `event: ${parsed.reason}`);
        }
        const out = await deps.rest.preview(store.openRest(p.tenantId, target), parsed.event, body["send"] === true);
        return { name: "testSubscription", status: 200, body: out };
      }
      const body = object(await readBody(req), ["from", "cap"]);
      const from = typeof body["from"] === "string" && RFC3339.test(body["from"]) ? Date.parse(body["from"]) : Number.NaN;
      if (Number.isNaN(from)) {
        throw new ApiError(400, "HUB-0400", "from must be an RFC 3339 timestamp");
      }
      const ceiling = deps.replayCap ?? REPLAY_MAX_EVENTS;
      const cap = body["cap"] === undefined ? ceiling : body["cap"];
      if (typeof cap !== "number" || !Number.isInteger(cap) || cap < 1 || cap > ceiling) {
        throw new ApiError(400, "HUB-0400", `cap must be an integer in 1..${ceiling}`);
      }
      const out = await store.replay(p.tenantId, id, from / 1000, cap, clock());
      if (out === null) {
        throw new ApiError(404, "HUB-0404", "no such subscription");
      }
      return { name: "replaySubscription", status: 202, body: out };
    }

    const dl = DELIVERY_REPLAY.exec(path);
    if (dl !== null) {
      if (method !== "POST") {
        return allow("POST");
      }
      const p = principalFromHeaders(req.headers);
      requireDuty(p, DUTY_SUBSCRIPTION_WRITE);
      const id = (dl[1] as string).toLowerCase();
      const out = UUID.test(id) ? await store.replayDelivery(p.tenantId, id, clock()) : "missing";
      if (out === "missing") {
        throw new ApiError(404, "HUB-0404", "no such delivery");
      }
      if (out === "not_dead") {
        throw new ApiError(409, "HUB-0409", "only a dead-lettered delivery can be replayed");
      }
      if (out === "not_replayable") {
        throw new ApiError(
          409,
          "HUB-0409",
          "HUB-0118: the legacy ERP already accepted this record and the migration service holds another legacy key for the event; a replay would push a duplicate. Reconcile the record in the legacy ERP instead",
        );
      }
      return { name: "replayDelivery", status: 202, body: { status: "pending" } };
    }

    if (path === "/v1/hub/deliveries" || path === "/v1/hub/dead-letters") {
      if (method !== "GET") {
        return allow("GET");
      }
      const p = principalFromHeaders(req.headers);
      requireDuty(p, DUTY_DELIVERY_READ);
      const q = url.searchParams;
      let status: DeliveryStatus | undefined;
      if (path === "/v1/hub/dead-letters") {
        status = "dead_letter";
      } else if (q.get("status") !== null) {
        status = q.get("status") as DeliveryStatus;
        if (!DELIVERY_STATUSES.includes(status)) {
          throw new ApiError(400, "HUB-0400", `status must be one of ${DELIVERY_STATUSES.join(", ")}`);
        }
      }
      const sub = q.get("subscriptionId");
      if (sub !== null && !UUID.test(sub)) {
        throw new ApiError(400, "HUB-0400", "subscriptionId must be a UUID");
      }
      const filter: { status?: DeliveryStatus; subscriptionId?: string } = {};
      if (status !== undefined) filter.status = status;
      if (sub !== null) filter.subscriptionId = sub.toLowerCase();
      const page = await store.listDeliveries(p.tenantId, filter, limitParam(q), q.get("cursor"));
      return { name: path === "/v1/hub/dead-letters" ? "listDeadLetters" : "listDeliveries", status: 200, body: page };
    }

    throw new ApiError(404, "HUB-0404", "no such route");
  }

  return async (req, res) => {
    const url = new URL(req.url ?? "/", "http://hub.internal");
    let name = "unknown";
    let status = 500;
    try {
      const out = await route(req, url);
      name = out.name;
      status = out.status;
      if (out.status === 204) {
        res.writeHead(204, { "cache-control": "no-store" });
        res.end();
      } else {
        send(res, out.status, out.body, out.headers);
      }
    } catch (err) {
      if (res.headersSent) {
        res.destroy();
        return;
      }
      let apiErr: ApiError;
      if (err instanceof ApiError) {
        apiErr = err;
      } else if (err instanceof VersionConflictError || err instanceof ClientIdTakenError || err instanceof ClientRevokedError || err instanceof CatalogVersionError || err instanceof CatalogStateError) {
        apiErr = new ApiError(409, "HUB-0409", err.message);
      } else if (err instanceof IdempotencyKeyReuseError) {
        apiErr = new ApiError(422, "HUB-0422", err.message);
      } else if (err instanceof RestConfigError || err instanceof WrongKindError) {
        apiErr = new ApiError(err instanceof WrongKindError ? 409 : 400, err instanceof WrongKindError ? "HUB-0409" : "HUB-0400", err.message);
      } else if (err instanceof FieldKeysMissingError) {
        apiErr = new ApiError(501, "HUB-0501", err.message);
      } else if (err instanceof BadCursorError) {
        apiErr = new ApiError(400, "HUB-0400", err.message);
      } else if (err instanceof RangeError) {
        apiErr = new ApiError(400, "HUB-0400", err.message);
      } else {
        apiErr = new ApiError(500, "HUB-0500", "unexpected error");
        console.error(JSON.stringify({ level: "error", msg: "unhandled error", path: url.pathname, error: (err as Error).message }));
      }
      status = apiErr.status;
      problem(res, apiErr, url.pathname);
    } finally {
      metrics?.inc("hub_http_requests_total", "HTTP requests", { route: name, status: String(status) });
    }
  };
}

/** The admin listener: /healthz, /readyz and /metrics (Prometheus). */
export function createAdminServer(metrics: Metrics, ping: () => Promise<void>): Server {
  return createServer((req, res) => {
    const path = new URL(req.url ?? "/", "http://hub.internal").pathname;
    if (path === "/metrics") {
      res.writeHead(200, { "content-type": "text/plain; version=0.0.4" }).end(metrics.render());
    } else if (path === "/healthz") {
      res.writeHead(200).end("ok");
    } else if (path === "/readyz") {
      ping().then(
        () => res.writeHead(200).end("ready"),
        () => res.writeHead(503).end("database unreachable"),
      );
    } else {
      res.writeHead(404).end();
    }
  });
}
