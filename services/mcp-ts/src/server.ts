import { createServer, type IncomingHttpHeaders, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { handleRpc, type RpcRequest, type RpcResponse, RPC } from "./jsonrpc.js";
import type { GatewayDeps, Principal } from "./gateway.js";
import { isObject } from "./tool.js";
import { rateLimitHeaders, type RateLimiter } from "./ratelimit.js";
import { Metrics, routeLabel } from "./metrics.js";

/** Header carrying the build identifier on every response (kernel-standard). */
export const API_BUILD_HEADER = "x-passage-api-build";
/** Request bodies above this size are refused with 413 before parsing. */
export const MAX_BODY_BYTES = 1024 * 1024;
/**
 * Most messages in one JSON-RPC batch. The rate limiter counts one hit per
 * HTTP request, so an unbounded batch would let one request fan out into any
 * number of tool calls.
 */
export const MAX_BATCH = 50;
/** The platform tenant; server-side only and never accepted from headers (kernel tenant middleware). */
const NIL_UUID = "00000000-0000-0000-0000-000000000000";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const TRACEPARENT = /^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$/;
const BEARER = /^Bearer ([A-Za-z0-9._~+/=-]+)$/;

export interface ServerOptions {
  deps: GatewayDeps;
  /** Build identifier echoed in X-Passage-Api-Build. */
  build: string;
  /** Readiness probe for /readyz; the caller wires catalog and upstream checks. */
  ready: () => Promise<boolean>;
  maxBodyBytes?: number;
  /** Per-pod limiter keyed by tenant and user; omitted means no limit. */
  limiter?: RateLimiter;
  /** Counters this listener records into; the admin listener serves them (src/metrics.ts). Omitted means a private registry. */
  metrics?: Metrics;
}

export type PrincipalResult = { ok: true; principal: Principal } | { ok: false; message: string };

function header(h: IncomingHttpHeaders, name: string): string | undefined {
  const v = h[name];
  return Array.isArray(v) ? v[0] : v;
}

/**
 * Builds the caller's principal from the edge's verified headers. The bearer
 * token is the user's Keycloak token, verified by Envoy, which strips client
 * copies of the identity headers and sets them from the token's claims
 * (deploy/gateway). This service never decodes the JWT itself; it applies the
 * same rules as the kernel's tenant middleware: a missing, malformed or nil
 * tenant is refused (401), and ids are canonicalized to lowercase so one
 * caller maps to one rate-limit key.
 */
export function principalFromHeaders(h: IncomingHttpHeaders): PrincipalResult {
  const auth = header(h, "authorization");
  const match = auth === undefined ? null : BEARER.exec(auth);
  if (!match) return { ok: false, message: "missing bearer token" };
  const tenantId = header(h, "x-passage-tenant")?.trim().toLowerCase();
  const userId = header(h, "x-passage-user")?.trim().toLowerCase();
  if (tenantId === undefined || !UUID.test(tenantId) || tenantId === NIL_UUID) {
    return { ok: false, message: "missing or invalid tenant" };
  }
  if (userId === undefined || !UUID.test(userId)) return { ok: false, message: "missing or invalid user" };
  const duties = (header(h, "x-passage-duties") ?? "")
    .split(",")
    .map((d) => d.trim())
    .filter((d) => d.length > 0);
  const principal: Principal = {
    userId,
    tenantId,
    duties: new Set(duties),
    userToken: match[1] ?? "",
  };
  const runId = header(h, "x-agent-run-id");
  if (runId !== undefined && runId.length > 0) principal.runId = runId;
  const traceparent = header(h, "traceparent");
  if (traceparent !== undefined && TRACEPARENT.test(traceparent)) principal.traceparent = traceparent;
  return { ok: true, principal };
}

class BodyTooLarge extends Error {}

async function readBody(req: IncomingMessage, limit: number): Promise<string> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    const buf = chunk as Buffer;
    size += buf.length;
    if (size > limit) throw new BodyTooLarge();
    chunks.push(buf);
  }
  return Buffer.concat(chunks).toString("utf8");
}

function send(res: ServerResponse, build: string, status: number, body: unknown, extra: Record<string, string> = {}): void {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
    [API_BUILD_HEADER]: build,
    "content-length": Buffer.byteLength(payload).toString(),
    ...extra,
  });
  res.end(payload);
}

const TITLES: Readonly<Record<number, string>> = {
  401: "Unauthorized",
  404: "Not Found",
  405: "Method Not Allowed",
  413: "Content Too Large",
  429: "Too Many Requests",
  500: "Internal Server Error",
};

/** RFC 9457 problem details in the kernel's shape: type, title, status, code, detail. */
function problem(res: ServerResponse, build: string, status: number, code: string, detail: string, extra: Record<string, string> = {}): void {
  const payload = JSON.stringify({ type: `https://passage.dev/problems/${code}`, title: TITLES[status] ?? "Error", status, code, detail });
  res.writeHead(status, {
    "content-type": "application/problem+json",
    "cache-control": "no-store",
    "content-length": Buffer.byteLength(payload).toString(),
    "x-content-type-options": "nosniff",
    [API_BUILD_HEADER]: build,
    ...extra,
  });
  res.end(payload);
}

function rpcError(code: number, message: string): RpcResponse {
  return { jsonrpc: "2.0", id: null, error: { code, message } };
}

/** Handles a single or batched JSON-RPC body. Returns undefined when nothing needs a response. */
async function dispatch(deps: GatewayDeps, p: Principal, parsed: unknown): Promise<unknown> {
  if (Array.isArray(parsed)) {
    if (parsed.length === 0) return rpcError(RPC.invalidRequest, "empty batch");
    if (parsed.length > MAX_BATCH) return rpcError(RPC.invalidRequest, `a batch holds at most ${MAX_BATCH} messages`);
    const replies: RpcResponse[] = [];
    for (const item of parsed) {
      if (!isObject(item)) {
        replies.push(rpcError(RPC.invalidRequest, "invalid JSON-RPC request"));
        continue;
      }
      const reply = await handleRpc(deps, p, item as unknown as RpcRequest);
      if (reply !== undefined) replies.push(reply);
    }
    return replies.length === 0 ? undefined : replies;
  }
  if (!isObject(parsed)) return rpcError(RPC.invalidRequest, "invalid JSON-RPC request");
  return handleRpc(deps, p, parsed as unknown as RpcRequest);
}

/** Creates the request listener: /healthz, /readyz and POST /mcp (streamable HTTP, JSON responses). */
export function createHandler(opts: ServerOptions): (req: IncomingMessage, res: ServerResponse) => Promise<void> {
  const limit = opts.maxBodyBytes ?? MAX_BODY_BYTES;
  const metrics = opts.metrics ?? new Metrics();
  return async (req, res) => {
    const path = new URL(req.url ?? "/", "http://localhost").pathname;
    // Counted once the response is fully sent; a connection destroyed mid-answer has no status to count.
    res.on("finish", () => metrics.recordResponse(routeLabel(path), res.statusCode));
    try {
      if (path === "/healthz") {
        return send(res, opts.build, 200, { status: "ok" });
      }
      if (path === "/readyz") {
        const ok = await opts.ready().catch(() => false);
        return send(res, opts.build, ok ? 200 : 503, { status: ok ? "ready" : "not_ready" });
      }
      if (path !== "/mcp") {
        return problem(res, opts.build, 404, "MCP-0404", "not found");
      }
      if (req.method !== "POST") {
        // Server-initiated SSE streams are not offered in this slice; clients POST every message.
        return problem(res, opts.build, 405, "MCP-0405", "method not allowed", { allow: "POST" });
      }
      const auth = principalFromHeaders(req.headers);
      if (!auth.ok) {
        return problem(res, opts.build, 401, "MCP-0401", auth.message, { "www-authenticate": "Bearer" });
      }
      // Limits apply per tenant and user; every answer after auth carries RateLimit-* headers.
      const decision = opts.limiter?.hit(`${auth.principal.tenantId}:${auth.principal.userId}`);
      const rl: Record<string, string> = decision === undefined ? {} : rateLimitHeaders(decision);
      if (decision !== undefined && !decision.allowed) {
        metrics.recordRateLimited();
        return problem(res, opts.build, 429, "MCP-0429", "rate limit exceeded", rl);
      }
      let raw: string;
      try {
        raw = await readBody(req, limit);
      } catch (err) {
        if (err instanceof BodyTooLarge) return problem(res, opts.build, 413, "MCP-0413", "request body too large", rl);
        throw err;
      }
      let parsed: unknown;
      try {
        parsed = JSON.parse(raw);
      } catch {
        return send(res, opts.build, 400, rpcError(-32700, "parse error"), rl);
      }
      const reply = await dispatch(opts.deps, auth.principal, parsed);
      if (reply === undefined) {
        res.writeHead(202, { [API_BUILD_HEADER]: opts.build, ...rl });
        res.end();
        return;
      }
      return send(res, opts.build, 200, reply, rl);
    } catch {
      // Internal errors are never echoed to the caller; the trace lives in the OTel span.
      if (res.headersSent) {
        res.destroy();
        return;
      }
      return problem(res, opts.build, 500, "MCP-0500", "internal error");
    }
  };
}

/** Starts the HTTP server and resolves once it is listening. */
export function startServer(opts: ServerOptions, addr: { host: string; port: number }): Promise<Server> {
  const server = createServer(createHandler(opts));
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(addr.port, addr.host, () => {
      server.off("error", reject);
      resolve(server);
    });
  });
}
