// HTTP surface: the session endpoints (/bff/login, /bff/callback, /bff/me, /bff/logout) and the /api/v1 routes of routes.ts.
// Every /api/v1 call needs a session and a rate-limit slot, and becomes one read of one owning service with a service token
// naming the session's customer. The portal reads only: any other method is refused. The browser never sees a token.

import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { authorizationUrl, exchangeCode, LoginRefused, pkcePair, randomToken, verifyIdToken, type Fetch, type Jwks, type OidcConfig } from "./oidc.ts";
import { matchRoute, QUERY_ALLOWLIST, type Route } from "./routes.ts";
import type { Audience, ServiceTokenSigner } from "./serviceToken.ts";
import { LOGIN_TTL_MS, parseCookies, SESSION_COOKIE, sameToken, type RateLimiter, type Session, type SessionStore } from "./session.ts";

const UPSTREAM_TIMEOUT_MS = 15_000;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface ServerDeps {
  oidc: OidcConfig;
  jwks: Jwks;
  fetcher: Fetch;
  sessions: SessionStore;
  signer: ServiceTokenSigner;
  upstreams: Record<Audience, string>;
  /** The origin the app is served from; a browser request naming another origin is refused. */
  appOrigin: string;
  /** False only for local development over plain http. */
  cookieSecure: boolean;
  limiter: RateLimiter;
  now?: () => number;
  log?: (entry: Record<string, unknown>) => void;
}

function send(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}): void {
  const payload = body === undefined ? "" : JSON.stringify(body);
  res.writeHead(status, {
    "content-type": status >= 400 ? "application/problem+json" : "application/json",
    "cache-control": "no-store",
    "x-content-type-options": "nosniff",
    "referrer-policy": "no-referrer",
    ...headers,
  });
  res.end(payload);
}

function problem(res: ServerResponse, status: number, code: string, detail: string): void {
  send(res, status, { type: `https://passage.dev/problems/${code}`, title: detail, status, code, detail });
}

export function createHttpServer(deps: ServerDeps): Server {
  const now = deps.now ?? Date.now;
  const log = deps.log ?? ((entry) => console.log(JSON.stringify(entry)));
  const cookieAttrs = `Path=/; HttpOnly; SameSite=Lax${deps.cookieSecure ? "; Secure" : ""}`;
  const setSession = (id: string, maxAgeSeconds: number): string => `${SESSION_COOKIE}=${id}; ${cookieAttrs}; Max-Age=${maxAgeSeconds}`;

  async function login(res: ServerResponse): Promise<void> {
    const state = randomToken();
    const nonce = randomToken();
    const { verifier, challenge } = pkcePair();
    deps.sessions.putLogin(state, { nonce, verifier, expiresAt: now() + LOGIN_TTL_MS });
    res.writeHead(302, { location: authorizationUrl(deps.oidc, state, nonce, challenge), "cache-control": "no-store" });
    res.end();
  }

  async function callback(url: URL, res: ServerResponse): Promise<void> {
    const code = url.searchParams.get("code");
    const state = url.searchParams.get("state");
    const pending = state ? deps.sessions.takeLogin(state, now()) : undefined;
    if (!code || !pending) return problem(res, 400, "CUS-BFF-400", "the sign-in is not valid or has expired; start again");
    try {
      const idToken = await exchangeCode(deps.oidc, code, pending.verifier, deps.fetcher);
      const identity = await verifyIdToken(idToken, deps.oidc, deps.jwks, pending.nonce, now());
      const session = deps.sessions.create(identity, now());
      log({ msg: "sign-in", userId: identity.userId, customerId: identity.customerId });
      res.writeHead(302, { location: "/", "set-cookie": setSession(session.id, 8 * 3600), "cache-control": "no-store" });
      res.end();
    } catch (err) {
      if (err instanceof LoginRefused) {
        log({ msg: "sign-in refused", reason: err.message });
        return problem(res, 403, "CUS-BFF-403", "this account cannot use the customer portal");
      }
      log({ msg: "sign-in failed", err: String(err) });
      problem(res, 502, "CUS-BFF-502", "the identity provider could not be reached");
    }
  }

  /** The one legal entity a call is for: the app's choice when the user has several, else the only one. */
  function legalEntity(session: Session, req: IncomingMessage): string | undefined {
    const wanted = req.headers["x-legal-entity"];
    const entities = session.identity.legalEntityIds;
    if (typeof wanted === "string" && wanted !== "") {
      const le = wanted.trim().toLowerCase();
      return UUID.test(le) && entities.includes(le) ? le : undefined;
    }
    return entities.length === 1 ? entities[0] : undefined;
  }

  async function api(req: IncomingMessage, res: ServerResponse, url: URL, session: Session): Promise<void> {
    const method = req.method ?? "GET";
    const matched = matchRoute(method, url.pathname);
    if (!matched) return problem(res, 404, "CUS-BFF-404", "no such route");
    const { route, upstreamPath } = matched;
    const le = legalEntity(session, req);
    if (!le) return problem(res, 400, "CUS-BFF-401", "name one of your legal entities in X-Legal-Entity");
    if (!deps.limiter.allow(session.id, now())) return problem(res, 429, "CUS-BFF-429", "too many requests");
    await forward(res, url, session, route, upstreamPath, le);
  }

  async function forward(res: ServerResponse, url: URL, session: Session, route: Route, upstreamPath: string, le: string): Promise<void> {
    const target = new URL(upstreamPath, deps.upstreams[route.audience]);
    for (const name of QUERY_ALLOWLIST) {
      const v = url.searchParams.get(name);
      if (v !== null) target.searchParams.set(name, v);
    }
    const token = deps.signer.mint(route.audience, { tenantId: session.identity.tenantId, legalEntityId: le, userId: session.identity.userId, customerId: session.identity.customerId });
    const headers: Record<string, string> = { "x-passage-service-token": token, accept: "application/json" };
    const started = now();
    let upstream: Response;
    try {
      upstream = await deps.fetcher(target.toString(), { method: route.method, headers, signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS) });
    } catch (err) {
      log({ msg: "upstream unreachable", audience: route.audience, path: route.path, err: String(err) });
      return problem(res, 502, "CUS-BFF-502", "the service did not answer");
    }
    log({ msg: "api", userId: session.identity.userId, customerId: session.identity.customerId, method: route.method, route: route.path, status: upstream.status, ms: now() - started });
    // A rejected service token is this service's fault, never the user's: do not tell the browser to sign in again.
    const status = upstream.status === 401 ? 502 : upstream.status;
    const text = await upstream.text();
    res.writeHead(status, {
      "content-type": upstream.headers.get("content-type")?.includes("json") ? (upstream.headers.get("content-type") as string) : "application/json",
      "cache-control": "no-store",
      "x-content-type-options": "nosniff",
      "referrer-policy": "no-referrer",
    });
    res.end(status === 502 && upstream.status === 401 ? JSON.stringify({ code: "CUS-BFF-502", status: 502, detail: "the service refused this portal" }) : text);
  }

  async function handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = new URL(req.url ?? "/", "http://bff.invalid");
    const method = req.method ?? "GET";
    if (url.pathname === "/healthz" || url.pathname === "/readyz") return send(res, 200, { status: "ok" });
    if (method === "GET" && url.pathname === "/bff/login") return login(res);
    if (method === "GET" && url.pathname === "/bff/callback") return callback(url, res);

    const session = deps.sessions.get(parseCookies(req.headers.cookie).get(SESSION_COOKIE), now());
    if (!session) return problem(res, 401, "CUS-BFF-401", "sign in first");
    if (method !== "GET" && method !== "HEAD") {
      const origin = req.headers.origin;
      if (origin !== undefined && origin !== deps.appOrigin) return problem(res, 403, "CUS-BFF-403", "the request does not come from the portal");
      const csrf = req.headers["x-csrf-token"];
      if (!sameToken(typeof csrf === "string" ? csrf : undefined, session.csrf)) return problem(res, 403, "CUS-BFF-403", "the request has no valid CSRF token");
    }
    if (method === "GET" && url.pathname === "/bff/me") {
      const i = session.identity;
      return send(res, 200, { userId: i.userId, name: i.name, legalEntityIds: i.legalEntityIds, csrfToken: session.csrf });
    }
    if (method === "POST" && url.pathname === "/bff/logout") {
      deps.sessions.delete(session.id);
      res.writeHead(204, { "set-cookie": setSession("", 0), "cache-control": "no-store" });
      res.end();
      return;
    }
    if (url.pathname.startsWith("/api/v1/")) return api(req, res, url, session);
    problem(res, 404, "CUS-BFF-404", "no such route");
  }

  return createServer((req, res) => {
    handle(req, res).catch((err: unknown) => {
      log({ msg: "unhandled", err: String(err) });
      if (!res.headersSent) problem(res, 500, "CUS-BFF-500", "internal error");
      else res.end();
    });
  });
}
