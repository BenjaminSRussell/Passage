// The integration hub HTTP API (/v1/hub, FR-35.9, FR-35.10): webhook subscriptions,
// the delivery log, the dead-letter list, resume after a 410 pause and replay.
//
// Identity is the edge's, the same contract as the agents API: the gateway
// verifies the token, strips client copies and sets x-passage-tenant,
// x-passage-user and x-passage-duties. A missing, malformed or nil tenant or a
// missing user is 401; a missing duty is 403. Errors are RFC 9457 problems with
// a stable HUB-04xx code.

import { randomBytes } from "node:crypto";
import { createServer, type IncomingHttpHeaders, type IncomingMessage, type Server, type ServerResponse } from "node:http";

import { REPLAY_MAX_EVENTS } from "./dispatch.ts";
import { validatePattern } from "./filter.ts";
import type { Metrics } from "./metrics.ts";
import {
  BadCursorError,
  DELIVERY_STATUSES,
  type DeliveryStatus,
  FieldKeysMissingError,
  type HubStore,
  VersionConflictError,
} from "./pgStore.ts";
import { UnsafeWebhookUrlError, validateWebhookUrl, type GuardOptions } from "./ssrf.ts";

export const DUTY_SUBSCRIPTION_WRITE = "hub.subscription.write";
export const DUTY_DELIVERY_READ = "hub.delivery.read";

export const MAX_BODY_BYTES = 64 * 1024;
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
  /** Total events one replay may enqueue (BR-35.9); defaults to 1,000,000. */
  replayCap?: number;
  clock?: () => number;
  ping?: () => Promise<void>;
}

async function readBody(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    const buf = typeof chunk === "string" ? Buffer.from(chunk) : (chunk as Buffer);
    size += buf.length;
    if (size > MAX_BODY_BYTES) {
      throw new ApiError(413, "HUB-0413", "request body exceeds 64 KiB");
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
  409: "Conflict", 413: "Content Too Large", 422: "Unprocessable Content", 500: "Internal Server Error", 501: "Not Implemented",
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

const SUBSCRIPTIONS = "/v1/hub/subscriptions";
const ONE = new RegExp(`^${SUBSCRIPTIONS}/([0-9a-fA-F-]{36})(?::(resume|replay))?$`);

export function createHubHandler(deps: HubApiDeps): (req: IncomingMessage, res: ServerResponse) => Promise<void> {
  const { store, metrics } = deps;
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
        const body = object(await readBody(req), ["name", "url", "patterns", "secret"]);
        const name = requireString(body, "name", 200);
        const target = requireString(body, "url", 2048);
        const patterns = patternsOf(body);
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
          name, url: target, secret, patterns, createdBy: p.userId, idempotencyKey: key === undefined || key === "" ? null : key,
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
          const body = object(await readBody(req), ["version", "name", "url", "patterns"]);
          const version = body["version"];
          if (typeof version !== "number" || !Number.isInteger(version) || version < 1) {
            throw new ApiError(400, "HUB-0400", "version must be the subscription's current version");
          }
          const patch: { version: number; name?: string; url?: string; patterns?: string[] } = { version };
          if (body["name"] !== undefined) patch.name = requireString(body, "name", 200);
          if (body["url"] !== undefined) {
            patch.url = requireString(body, "url", 2048);
            await checkUrl(patch.url);
          }
          if (body["patterns"] !== undefined) patch.patterns = patternsOf(body);
          const sub = await store.patchSubscription(p.tenantId, id, patch);
          if (sub === null) {
            throw new ApiError(404, "HUB-0404", "no such subscription");
          }
          return { name: "updateSubscription", status: 200, body: sub };
        }
        if (method === "DELETE") {
          const p = principalFromHeaders(req.headers);
          requireDuty(p, DUTY_SUBSCRIPTION_WRITE);
          if (!(await store.deleteSubscription(p.tenantId, id))) {
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
        const sub = await store.resumeSubscription(p.tenantId, id);
        if (sub === null) {
          throw new ApiError(404, "HUB-0404", "no such subscription");
        }
        return { name: "resumeSubscription", status: 200, body: sub };
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
      } else if (err instanceof VersionConflictError) {
        apiErr = new ApiError(409, "HUB-0409", err.message);
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
