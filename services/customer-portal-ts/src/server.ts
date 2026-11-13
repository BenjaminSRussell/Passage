// HTTP surface: the session endpoints (/bff/login, /bff/callback, /bff/me, /bff/logout) and the /api/v1 routes of routes.ts.
// Every /api/v1 call needs a session and a rate-limit slot, and becomes one call to one owning service with a service token
// naming the customer the contact acts for, and only the duty of that call. Reads are GET; the only writes are the two of
// routes.ts, with a CSRF token, an Origin check and an Idempotency-Key. The browser never sees a token.

import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { authorizationUrl, endSessionUrl, exchangeCode, LoginRefused, pkcePair, randomToken, verifyIdToken, verifyLogoutToken, type Fetch, type Jwks, type OidcConfig } from "./oidc.ts";
import { invoiceDocument, statementDocument, type DocumentRequest, type PortalInvoice, type PortalStatement } from "./documents.ts";
import { FORBIDDEN_BODY_FIELDS, IDEMPOTENCY_KEY, matchRoute, QUERY_ALLOWLIST, type Route } from "./routes.ts";
import type { Audience, ServiceTokenSigner } from "./serviceToken.ts";
import { LOGIN_TTL_MS, parseCookies, SESSION_COOKIE, sameToken, StoreUnavailable, type Limiter, type Session, type SessionStore } from "./session.ts";

const UPSTREAM_TIMEOUT_MS = 15_000;
const MAX_BODY_BYTES = 64 * 1024;
const ACCOUNTS_TTL_MS = 60_000;
const NAMES_TTL_MS = 10 * 60_000;
const CACHE_MAX = 5000;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const DAY = /^\d{4}-\d{2}-\d{2}$/;
const MAX_PDF_BYTES = 20 * 1024 * 1024;

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
  limiter: Limiter;
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

class BodyRefused extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function readJsonBody(req: IncomingMessage): Promise<string> {
  const type = req.headers["content-type"] ?? "";
  if (!/^application\/json\s*(;|$)/i.test(type)) throw new BodyRefused(415, "send application/json");
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    size += (chunk as Buffer).length;
    if (size > MAX_BODY_BYTES) throw new BodyRefused(413, "the request is too large");
    chunks.push(chunk as Buffer);
  }
  const text = Buffer.concat(chunks).toString("utf8");
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    throw new BodyRefused(400, "the request is not valid JSON");
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) throw new BodyRefused(400, "the request must be a JSON object");
  const named = FORBIDDEN_BODY_FIELDS.filter((f) => f in (parsed as Record<string, unknown>));
  if (named.length > 0) throw new BodyRefused(400, `the request may not name ${named.join(", ")}: who it is for comes from your sign-in`);
  return text;
}

function bounded<V>(cache: Map<string, V>): void {
  if (cache.size > CACHE_MAX) cache.clear();
}

export function createHttpServer(deps: ServerDeps): Server {
  const now = deps.now ?? Date.now;
  const log = deps.log ?? ((entry) => console.log(JSON.stringify(entry)));
  const cookieAttrs = `Path=/; HttpOnly; SameSite=Lax${deps.cookieSecure ? "; Secure" : ""}`;
  const setSession = (id: string, maxAgeSeconds: number): string => `${SESSION_COOKIE}=${id}; ${cookieAttrs}; Max-Age=${maxAgeSeconds}`;
  /** The accounts a contact may act for, per session and legal entity, kept for a minute; derived data, so a pod's own copy is enough. */
  const accountCache = new Map<string, { ids: Set<string>; expires: number }>();
  /** Legal entity display names, per tenant and entity, kept for ten minutes. */
  const nameCache = new Map<string, { name: string | null; expires: number }>();

  async function login(res: ServerResponse): Promise<void> {
    const state = randomToken();
    const nonce = randomToken();
    const { verifier, challenge } = pkcePair();
    await deps.sessions.putLogin(state, { nonce, verifier, expiresAt: now() + LOGIN_TTL_MS });
    res.writeHead(302, { location: authorizationUrl(deps.oidc, state, nonce, challenge), "cache-control": "no-store" });
    res.end();
  }

  async function callback(url: URL, res: ServerResponse): Promise<void> {
    const code = url.searchParams.get("code");
    const state = url.searchParams.get("state");
    const pending = state ? await deps.sessions.takeLogin(state, now()) : undefined;
    if (!code || !pending) return problem(res, 400, "CUS-BFF-400", "the sign-in is not valid or has expired; start again");
    try {
      const idToken = await exchangeCode(deps.oidc, code, pending.verifier, deps.fetcher);
      const identity = await verifyIdToken(idToken, deps.oidc, deps.jwks, pending.nonce, now());
      const session = await deps.sessions.create(identity, idToken, now());
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

  function mint(session: Session, audience: Audience, duty: string, le: string, customerId: string): string {
    return deps.signer.mint(audience, { tenantId: session.identity.tenantId, legalEntityId: le, userId: session.identity.userId, customerId }, [duty]);
  }

  async function call(token: string, method: "GET" | "POST", target: URL, extra: Record<string, string>, body?: string): Promise<Response> {
    const headers: Record<string, string> = { "x-passage-service-token": token, accept: "application/json", ...extra };
    return deps.fetcher(target.toString(), { method, headers, ...(body !== undefined ? { body } : {}), signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS) });
  }

  /**
   * The customer a call is for: the contact's own, or, when X-Customer names another, one of the accounts party lists for the
   * contact's customer (its bill-to and ship-to accounts of the same customer group). Anything else is refused, never signed.
   */
  async function customerFor(session: Session, req: IncomingMessage, le: string): Promise<{ customerId: string } | { refused: string }> {
    const wanted = req.headers["x-customer"];
    const home = session.identity.customerId;
    if (typeof wanted !== "string" || wanted.trim() === "") return { customerId: home };
    const id = wanted.trim().toLowerCase();
    if (id === home) return { customerId: home };
    if (!UUID.test(id)) return { refused: "name one of your accounts in X-Customer" };
    const key = `${session.id}:${le}`;
    let entry = accountCache.get(key);
    if (!entry || entry.expires < now()) {
      const target = new URL("/v1/party/customer-portal/accounts", deps.upstreams.party);
      const res = await call(mint(session, "party", "party.customer-portal.read", le, home), "GET", target, {});
      if (!res.ok) return { refused: "your accounts could not be read" };
      const body = (await res.json()) as { items?: { id?: unknown }[] };
      entry = { ids: new Set((body.items ?? []).map((a) => String(a.id ?? "").toLowerCase())), expires: now() + ACCOUNTS_TTL_MS };
      bounded(accountCache);
      accountCache.set(key, entry);
    }
    return entry.ids.has(id) ? { customerId: id } : { refused: "that is not one of your accounts" };
  }

  async function api(req: IncomingMessage, res: ServerResponse, url: URL, session: Session): Promise<void> {
    const method = req.method ?? "GET";
    if (method === "GET" && url.pathname === "/api/v1/companies") return companies(res, session);
    const invoicePdf = method === "GET" ? /^\/api\/v1\/invoices\/([^/]+)\/pdf$/.exec(url.pathname) : null;
    if (invoicePdf || (method === "GET" && url.pathname === "/api/v1/statement/pdf")) return pdf(req, res, url, session, invoicePdf?.[1]);
    const matched = matchRoute(method, url.pathname);
    if (!matched) return problem(res, 404, "CUS-BFF-404", "no such route");
    const { route, upstreamPath } = matched;
    const le = legalEntity(session, req);
    if (!le) return problem(res, 400, "CUS-BFF-401", "name one of your legal entities in X-Legal-Entity");
    if (!(await deps.limiter.allow(session.id, now()))) return problem(res, 429, "CUS-BFF-429", "too many requests");
    let body: string | undefined;
    const extra: Record<string, string> = {};
    if (route.method === "POST") {
      const key = req.headers["idempotency-key"];
      if (typeof key !== "string" || !IDEMPOTENCY_KEY.test(key)) return problem(res, 400, "CUS-BFF-405", "send an Idempotency-Key of 8 to 80 letters, digits, - or _, one per submit");
      extra["idempotency-key"] = key;
      try {
        body = await readJsonBody(req);
      } catch (err) {
        if (err instanceof BodyRefused) return problem(res, err.status, "CUS-BFF-406", err.message);
        throw err;
      }
    }
    let customerId = session.identity.customerId;
    if (!route.homeOnly) {
      const chosen = await customerFor(session, req, le);
      if ("refused" in chosen) return problem(res, 403, "CUS-BFF-407", chosen.refused);
      customerId = chosen.customerId;
    }
    await forward(res, url, session, route, upstreamPath, le, customerId, extra, body);
  }

  async function forward(res: ServerResponse, url: URL, session: Session, route: Route, upstreamPath: string, le: string, customerId: string, extra: Record<string, string>, body: string | undefined): Promise<void> {
    const target = new URL(upstreamPath, deps.upstreams[route.audience]);
    for (const name of QUERY_ALLOWLIST) {
      const v = url.searchParams.get(name);
      if (v !== null) target.searchParams.set(name, v);
    }
    const token = mint(session, route.audience, route.duty, le, customerId);
    const started = now();
    let upstream: Response;
    try {
      upstream = await call(token, route.method, target, extra, body);
    } catch (err) {
      log({ msg: "upstream unreachable", audience: route.audience, path: route.path, err: String(err) });
      return problem(res, 502, "CUS-BFF-502", "the service did not answer");
    }
    log({ msg: "api", userId: session.identity.userId, customerId, method: route.method, route: route.path, status: upstream.status, ms: now() - started });
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

  /** A legal entity's customer-facing name from identity, cached per tenant and entity; null when identity has none or failed. */
  async function entityName(session: Session, le: string): Promise<string | null> {
    const key = `${session.identity.tenantId}:${le}`;
    const hit = nameCache.get(key);
    if (hit && hit.expires >= now()) return hit.name;
    let name: string | null = null;
    try {
      const target = new URL("/v1/identity/customer-portal/legal-entity", deps.upstreams.identity);
      const r = await call(mint(session, "identity", "identity.customer-portal.read", le, session.identity.customerId), "GET", target, {});
      if (r.ok) {
        const shown = ((await r.json()) as { displayName?: unknown }).displayName;
        name = typeof shown === "string" && shown !== "" ? shown : null;
      } else log({ msg: "legal entity name unavailable", status: r.status });
    } catch (err) {
      log({ msg: "legal entity name unreachable", err: String(err) });
    }
    // A failed read is not cached for long: the selector falls back to a numbered name and the next load tries again.
    bounded(nameCache);
    nameCache.set(key, { name, expires: now() + (name === null ? 15_000 : NAMES_TTL_MS) });
    return name;
  }

  /** The contact's companies with their customer-facing names, one small read of identity per legal entity (cached). */
  async function companies(res: ServerResponse, session: Session): Promise<void> {
    if (!(await deps.limiter.allow(session.id, now()))) return problem(res, 429, "CUS-BFF-429", "too many requests");
    const items = await Promise.all(session.identity.legalEntityIds.map(async (le) => ({ legalEntityId: le, name: await entityName(session, le) })));
    send(res, 200, { items });
  }

  /**
   * An invoice or the statement as a PDF (FR-30.26): the same customer-scoped read the app makes (billing's invoice, receivables'
   * statement, for the account in X-Customer), turned into print's document request and rendered by print with the portal's own
   * render duty. Nothing is stored; a refusal of the read (404, 403) is passed on as it came.
   */
  async function pdf(req: IncomingMessage, res: ServerResponse, url: URL, session: Session, invoiceId: string | undefined): Promise<void> {
    const le = legalEntity(session, req);
    if (!le) return problem(res, 400, "CUS-BFF-401", "name one of your legal entities in X-Legal-Entity");
    if (!(await deps.limiter.allow(session.id, now()))) return problem(res, 429, "CUS-BFF-429", "too many requests");
    if (invoiceId !== undefined && !UUID.test(invoiceId)) return problem(res, 404, "CUS-BFF-404", "no such invoice");
    const asOf = url.searchParams.get("asOf");
    if (asOf !== null && !DAY.test(asOf)) return problem(res, 400, "CUS-BFF-408", "asOf must be a YYYY-MM-DD date");
    const chosen = await customerFor(session, req, le);
    if ("refused" in chosen) return problem(res, 403, "CUS-BFF-407", chosen.refused);
    const read =
      invoiceId !== undefined
        ? { audience: "billing" as const, duty: "billing.customer-portal.read", path: `/v1/billing/customer-portal/invoices/${invoiceId}` }
        : { audience: "receivables" as const, duty: "receivables.customer-portal.read", path: "/v1/receivables/customer-portal/statement" };
    const source = new URL(read.path, deps.upstreams[read.audience]);
    if (asOf !== null && invoiceId === undefined) source.searchParams.set("asOf", asOf);
    let document: DocumentRequest;
    let fileName: string;
    try {
      const r = await call(mint(session, read.audience, read.duty, le, chosen.customerId), "GET", source, {});
      if (!r.ok) {
        // As in forward: a rejected service token is the portal's fault, never the user's, and says nothing of the read.
        log({ msg: "pdf read refused", audience: read.audience, status: r.status });
        if (r.status === 401) return problem(res, 502, "CUS-BFF-502", "the service refused this portal");
        const type = r.headers.get("content-type") ?? "";
        res.writeHead(r.status, { "content-type": type.includes("json") ? type : "application/json", "cache-control": "no-store", "x-content-type-options": "nosniff" });
        res.end(await r.text());
        return;
      }
      const seller = await entityName(session, le);
      if (invoiceId !== undefined) {
        const invoice = (await r.json()) as PortalInvoice;
        document = invoiceDocument(invoice, seller);
        fileName = `${invoice.number || "invoice"}.pdf`;
      } else {
        const statement = (await r.json()) as PortalStatement;
        document = statementDocument(statement, seller);
        fileName = `statement-${statement.asOf}.pdf`;
      }
    } catch (err) {
      log({ msg: "upstream unreachable", audience: read.audience, path: url.pathname, err: String(err) });
      return problem(res, 502, "CUS-BFF-502", "the service did not answer");
    }
    let rendered: Response;
    try {
      const target = new URL("/print/v1/documents:render", deps.upstreams.print);
      rendered = await call(mint(session, "print", "print.customer-portal.render", le, chosen.customerId), "POST", target, { "content-type": "application/json", accept: "application/pdf" }, JSON.stringify(document));
    } catch (err) {
      log({ msg: "upstream unreachable", audience: "print", err: String(err) });
      return problem(res, 502, "CUS-BFF-502", "the document could not be rendered; try again");
    }
    log({ msg: "pdf", userId: session.identity.userId, customerId: chosen.customerId, kind: invoiceId !== undefined ? "invoice" : "statement", status: rendered.status });
    if (!rendered.ok) return problem(res, 502, "CUS-BFF-409", "the document could not be rendered; try again later");
    const bytes = Buffer.from(await rendered.arrayBuffer());
    if (bytes.length > MAX_PDF_BYTES) return problem(res, 502, "CUS-BFF-409", "the document could not be rendered; try again later");
    res.writeHead(200, {
      "content-type": "application/pdf",
      "content-disposition": `attachment; filename="${fileName.replace(/[^A-Za-z0-9._-]/g, "_")}"`,
      "cache-control": "no-store",
      "x-content-type-options": "nosniff",
      "referrer-policy": "no-referrer",
    });
    res.end(bytes);
  }

  /**
   * OIDC back-channel logout: Keycloak calls this (no cookie, no CSRF) when a contact's single sign-on session ends, including when
   * identity disables or remaps the contact. The logout token is verified (signature, issuer, audience, event) and every portal
   * session of that Keycloak session, or of that user when none is named, is deleted, so a disabled or moved contact is signed out of
   * the portal at once instead of keeping its old customer for the rest of the 8 hours.
   */
  async function backchannelLogout(req: IncomingMessage, res: ServerResponse): Promise<void> {
    let token = "";
    try {
      const chunks: Buffer[] = [];
      let size = 0;
      for await (const chunk of req) {
        size += (chunk as Buffer).length;
        if (size > 16 * 1024) return send(res, 400, { error: "invalid_request" });
        chunks.push(chunk as Buffer);
      }
      token = new URLSearchParams(Buffer.concat(chunks).toString("utf8")).get("logout_token") ?? "";
      const { sid, sub } = await verifyLogoutToken(token, deps.oidc, deps.jwks, now());
      const ended = await deps.sessions.deleteByKeycloak(sid, sub);
      log({ msg: "back-channel logout", sessions: ended });
      send(res, 200, {});
    } catch (err) {
      if (err instanceof LoginRefused) {
        log({ msg: "back-channel logout refused", reason: err.message });
        return send(res, 400, { error: "invalid_request" });
      }
      throw err;
    }
  }

  async function handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = new URL(req.url ?? "/", "http://bff.invalid");
    const method = req.method ?? "GET";
    if (url.pathname === "/healthz" || url.pathname === "/readyz") return send(res, 200, { status: "ok" });
    if (method === "GET" && url.pathname === "/bff/login") return login(res);
    if (method === "GET" && url.pathname === "/bff/callback") return callback(url, res);
    if (method === "POST" && url.pathname === "/bff/backchannel-logout") return backchannelLogout(req, res);

    const session = await deps.sessions.get(parseCookies(req.headers.cookie).get(SESSION_COOKIE), now());
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
      await deps.sessions.delete(session.id);
      // The browser is sent on to Keycloak's end-session endpoint so the single sign-on session ends with this one; without that
      // the next "sign in" would walk straight back in.
      send(res, 200, { logoutUrl: endSessionUrl(deps.oidc, session.idToken) }, { "set-cookie": setSession("", 0) });
      return;
    }
    if (url.pathname.startsWith("/api/v1/")) return api(req, res, url, session);
    problem(res, 404, "CUS-BFF-404", "no such route");
  }

  return createServer((req, res) => {
    handle(req, res).catch((err: unknown) => {
      log({ msg: "unhandled", err: String(err) });
      // A store that cannot be reached is the service being unavailable, not the contact being signed out.
      const status = err instanceof StoreUnavailable ? 503 : 500;
      if (!res.headersSent) problem(res, status, status === 503 ? "CUS-BFF-503" : "CUS-BFF-500", status === 503 ? "the portal cannot reach its session store; retry" : "internal error");
      else res.end();
    });
  });
}
