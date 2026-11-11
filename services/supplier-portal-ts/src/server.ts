// HTTP surface: the session endpoints (/bff/login, /bff/callback, /bff/me, /bff/logout) and the /api/v1 routes of routes.ts.
// Every /api/v1 call needs a session, a CSRF token on writes, an allowed origin, a rate-limit slot, and becomes one call to
// one owning service with a service token naming the session's vendor. The browser never sees a token.

import { randomUUID } from "node:crypto";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { authorizationUrl, exchangeCode, LoginRefused, pkcePair, randomToken, verifyIdToken, type Fetch, type Jwks, type OidcConfig } from "./oidc.ts";
import { PendingProvisioner, type UserProvisioner } from "./provision.ts";
import { matchRoute, QUERY_ALLOWLIST, STRIPPED_BODY_KEYS, type Route } from "./routes.ts";
import { screenUpload, type ScanPolicy } from "./scan.ts";
import { DUTY_INVITE_ACCEPT, DUTY_PORTAL_ADMIN, type Audience, type ServiceTokenSigner } from "./serviceToken.ts";
import { LOGIN_TTL_MS, parseCookies, RateLimiter, SESSION_COOKIE, sameToken, type Session, type SessionStore } from "./session.ts";

export const MAX_BODY_BYTES = 8 * 1024 * 1024;
/** The public registration form is a few short fields; anything larger is refused before it is read. */
export const MAX_PUBLIC_BODY_BYTES = 16 * 1024;
/** The name of the form field a person never sees; a bot that fills it is answered as if it had registered. */
export const HONEYPOT_FIELD = "homepage";
const REGISTRATION_FIELDS = ["companyName", "contactName", "email", "phone", "country", "taxId", "website", "message"] as const;
const UUID_PATTERN = "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}";
const INVITE_TOKEN = new RegExp(`^inv1\\.(${UUID_PATTERN})\\.(${UUID_PATTERN})\\.(${UUID_PATTERN})\\.(${UUID_PATTERN})\\.[A-Za-z0-9_-]{32,64}$`);
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
  /** Virus scan of uploads. Absent means no scanner and no permission to skip it: uploads are refused. */
  scan?: ScanPolicy;
  /** Creates the identity-provider user of an accepted invitation. Defaults to recording that an administrator must. */
  provisioner?: UserProvisioner;
  /** The tenant and legal entity the public registration page registers prospects in. Absent: registration is closed. */
  registration?: { tenantId: string; legalEntityId: string };
  /** A limit per client address for the public routes (registration, invitation checks); default 10 an hour. */
  publicLimiter?: RateLimiter;
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

async function readBody(req: IncomingMessage, max: number): Promise<Buffer | "too-large"> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    size += (chunk as Buffer).length;
    if (size > max) return "too-large";
    chunks.push(chunk as Buffer);
  }
  return Buffer.concat(chunks);
}

export function createHttpServer(deps: ServerDeps): Server {
  const now = deps.now ?? Date.now;
  const log = deps.log ?? ((entry) => console.log(JSON.stringify(entry)));
  const scanPolicy: ScanPolicy = deps.scan ?? { allowUnscanned: false };
  const provisioner = deps.provisioner ?? new PendingProvisioner(log);
  const publicLimiter = deps.publicLimiter ?? new RateLimiter(10, 3_600_000);
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
    if (!code || !pending) return problem(res, 400, "SUP-BFF-400", "the sign-in is not valid or has expired; start again");
    try {
      const idToken = await exchangeCode(deps.oidc, code, pending.verifier, deps.fetcher);
      const identity = await verifyIdToken(idToken, deps.oidc, deps.jwks, pending.nonce, now());
      const session = deps.sessions.create(identity, now());
      log({ msg: "sign-in", userId: identity.userId, vendorId: identity.vendorId });
      res.writeHead(302, { location: "/", "set-cookie": setSession(session.id, 8 * 3600), "cache-control": "no-store" });
      res.end();
    } catch (err) {
      if (err instanceof LoginRefused) {
        log({ msg: "sign-in refused", reason: err.message });
        return problem(res, 403, "SUP-BFF-403", "this account cannot use the supplier portal");
      }
      log({ msg: "sign-in failed", err: String(err) });
      problem(res, 502, "SUP-BFF-502", "the identity provider could not be reached");
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
    if (!matched) return problem(res, 404, "SUP-BFF-404", "no such route");
    const { route, upstreamPath } = matched;
    if (route.adminOnly && !session.identity.isAdmin) return problem(res, 403, "SUP-BFF-403", "only a vendor administrator can do this");
    const le = legalEntity(session, req);
    if (!le) return problem(res, 400, "SUP-BFF-401", "name one of your legal entities in X-Legal-Entity");
    if (!deps.limiter.allow(session.id, now())) return problem(res, 429, "SUP-BFF-429", "too many requests");

    let body: string | undefined;
    if (route.body === "json") {
      const raw = await readBody(req, MAX_BODY_BYTES);
      if (raw === "too-large") return problem(res, 413, "SUP-BFF-413", "the request is too large");
      let parsed: unknown = {};
      if (raw.length > 0) {
        try {
          parsed = JSON.parse(raw.toString("utf8"));
        } catch {
          return problem(res, 400, "SUP-BFF-400", "the body is not JSON");
        }
      }
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return problem(res, 400, "SUP-BFF-400", "the body must be a JSON object");
      const obj = parsed as Record<string, unknown>;
      for (const key of STRIPPED_BODY_KEYS) delete obj[key];
      if (route.injectLegalEntity) obj["legalEntityId"] = le;
      if (route.scan) {
        const upload = obj[route.scan];
        // A body with no upload in it is the owning service's to refuse; an upload is never forwarded before it is screened.
        // The upload must be a base64 string: anything else (a JSON byte array, an object) could still decode upstream and
        // reach a parser without having been scanned, so it is refused here.
        if (upload !== undefined && typeof upload !== "string") return problem(res, 400, "SUP-BFF-400", `${route.scan} must be a base64 string`);
        if (typeof upload === "string" && upload !== "") {
          const outcome = await screenUpload(scanPolicy, Buffer.from(upload, "base64"), log);
          if (!outcome.accept && outcome.reason === "infected") return problem(res, 422, "SUP-BFF-422", "the file was refused because it contains a virus");
          if (!outcome.accept) return problem(res, 503, "SUP-BFF-503", "documents cannot be checked for viruses right now; try again later");
        }
      }
      body = JSON.stringify(obj);
    }
    await forward(req, res, url, session, route, upstreamPath, le, body);
  }

  async function forward(req: IncomingMessage, res: ServerResponse, url: URL, session: Session, route: Route, upstreamPath: string, le: string, body: string | undefined): Promise<void> {
    const target = new URL(upstreamPath, deps.upstreams[route.audience]);
    for (const name of QUERY_ALLOWLIST) {
      const v = url.searchParams.get(name);
      if (v !== null) target.searchParams.set(name, v);
    }
    // An administrator route is signed with the vendor-admin duty alone, and only for a session holding the supplier-admin role
    // (checked in api() as well): party requires that duty on the invitation routes, so a supplier user's token cannot reach them.
    const caller = { tenantId: session.identity.tenantId, legalEntityId: le, userId: session.identity.userId, vendorId: session.identity.vendorId, isAdmin: session.identity.isAdmin };
    const token = deps.signer.mint(route.audience, caller, route.adminOnly ? [DUTY_PORTAL_ADMIN] : undefined);
    const headers: Record<string, string> = { "x-passage-service-token": token, accept: route.binary ? "*/*" : "application/json" };
    if (body !== undefined) headers["content-type"] = "application/json";
    if (route.method === "POST") {
      const key = req.headers["idempotency-key"];
      headers["idempotency-key"] = typeof key === "string" && key !== "" ? key : randomUUID();
    }
    const started = now();
    let upstream: Response;
    try {
      upstream = await deps.fetcher(target.toString(), { method: route.method, headers, ...(body !== undefined ? { body } : {}), signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS) });
    } catch (err) {
      log({ msg: "upstream unreachable", audience: route.audience, path: route.path, err: String(err) });
      return problem(res, 502, "SUP-BFF-502", "the service did not answer");
    }
    log({ msg: "api", userId: session.identity.userId, vendorId: session.identity.vendorId, method: route.method, route: route.path, status: upstream.status, ms: now() - started });
    // A rejected service token is this service's fault, never the user's: do not tell the browser to sign in again.
    const status = upstream.status === 401 ? 502 : upstream.status;
    if (route.binary && upstream.ok) {
      const out: Record<string, string> = { "cache-control": "no-store", "x-content-type-options": "nosniff", "referrer-policy": "no-referrer" };
      for (const h of ["content-type", "content-disposition", "x-content-sha256"]) {
        const v = upstream.headers.get(h);
        if (v) out[h] = v;
      }
      res.writeHead(status, out);
      res.end(Buffer.from(await upstream.arrayBuffer()));
      return;
    }
    let text = await upstream.text();
    if (route.inviteLink && upstream.ok) text = withAcceptLink(text, deps.appOrigin);
    res.writeHead(status, {
      "content-type": upstream.headers.get("content-type")?.includes("json") ? (upstream.headers.get("content-type") as string) : "application/json",
      "cache-control": "no-store",
      "x-content-type-options": "nosniff",
      "referrer-policy": "no-referrer",
    });
    res.end(status === 502 && upstream.status === 401 ? JSON.stringify({ code: "SUP-BFF-502", status: 502, detail: "the service refused this portal" }) : text);
  }

  /** The address the edge saw: the rightmost X-Forwarded-For entry (the one the trusted proxy appended), else the socket's. */
  function clientAddress(req: IncomingMessage): string {
    const xff = req.headers["x-forwarded-for"];
    const last = typeof xff === "string" ? xff.split(",").pop()?.trim() : undefined;
    return last || req.socket.remoteAddress || "unknown";
  }

  /** What every public POST needs: the portal's own origin, a small JSON object, and a slot in the per-address limit. */
  async function publicBody(req: IncomingMessage, res: ServerResponse): Promise<Record<string, unknown> | undefined> {
    const origin = req.headers.origin;
    if (origin !== undefined && origin !== deps.appOrigin) return void problem(res, 403, "SUP-BFF-403", "the request does not come from the portal");
    if (!deps.limiter.allow("public", now()) || !publicLimiter.allow(clientAddress(req), now())) return void problem(res, 429, "SUP-BFF-429", "too many requests; try again later");
    const raw = await readBody(req, MAX_PUBLIC_BODY_BYTES);
    if (raw === "too-large") return void problem(res, 413, "SUP-BFF-413", "the request is too large");
    try {
      const parsed: unknown = JSON.parse(raw.toString("utf8"));
      if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) return parsed as Record<string, unknown>;
    } catch {
      // falls through to the refusal
    }
    return void problem(res, 400, "SUP-BFF-400", "the body must be a JSON object");
  }

  async function callParty(token: string, path: string, body: unknown): Promise<Response | undefined> {
    try {
      return await deps.fetcher(new URL(path, deps.upstreams.party).toString(), {
        method: "POST",
        headers: { "x-passage-service-token": token, accept: "application/json", "content-type": "application/json", "idempotency-key": randomUUID() },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS),
      });
    } catch (err) {
      log({ msg: "upstream unreachable", audience: "party", err: String(err) });
      return undefined;
    }
  }

  /** The registration page asks whether registering is open, so it can show the form or say it is closed. */
  function registrationStatus(res: ServerResponse): void {
    send(res, 200, { open: deps.registration !== undefined });
  }

  /** Public prospect registration: no session, a honeypot, a size limit and a per-address rate limit; party records it for a buyer. */
  async function registerProspect(req: IncomingMessage, res: ServerResponse): Promise<void> {
    if (!deps.registration) return problem(res, 404, "SUP-BFF-404", "registration is not open");
    const body = await publicBody(req, res);
    if (!body) return;
    const honeypot = body[HONEYPOT_FIELD];
    if (typeof honeypot === "string" && honeypot.trim() !== "") {
      // The same answer a real registration gets, and nothing is recorded.
      log({ msg: "registration honeypot filled" });
      return send(res, 202, { status: "RECEIVED" });
    }
    const forward: Record<string, string> = {};
    for (const key of REGISTRATION_FIELDS) {
      const v = body[key];
      if (v === undefined || v === null) continue;
      if (typeof v !== "string") return problem(res, 400, "SUP-BFF-400", `${key} must be text`);
      forward[key] = v;
    }
    const token = deps.signer.mintRegistration(deps.registration.tenantId, deps.registration.legalEntityId);
    const upstream = await callParty(token, "/v1/party/vendor-portal/prospects", forward);
    if (!upstream) return problem(res, 502, "SUP-BFF-502", "the service did not answer");
    log({ msg: "registration", status: upstream.status });
    if (upstream.status === 202) return send(res, 202, { status: "RECEIVED" });
    if (upstream.status === 401) return problem(res, 502, "SUP-BFF-502", "the service refused this portal");
    const text = await upstream.text();
    res.writeHead(upstream.status, { "content-type": "application/problem+json", "cache-control": "no-store", "x-content-type-options": "nosniff" });
    res.end(text);
  }

  /** The vendor an invitation names, read from its token. Nothing in the token is trusted: the owning service checks the secret. */
  function parseInvite(token: unknown): { tenantId: string; legalEntityId: string; vendorId: string; inviteId: string; token: string } | undefined {
    const m = typeof token === "string" ? INVITE_TOKEN.exec(token) : null;
    return m ? { tenantId: m[1] as string, legalEntityId: m[2] as string, vendorId: m[3] as string, inviteId: m[4] as string, token: token as string } : undefined;
  }

  async function inviteCall(invite: NonNullable<ReturnType<typeof parseInvite>>, action: "verify" | "accept"): Promise<Response | undefined> {
    const signed = deps.signer.mint("party", { tenantId: invite.tenantId, legalEntityId: invite.legalEntityId, userId: "invitee", vendorId: invite.vendorId }, [DUTY_INVITE_ACCEPT]);
    return callParty(signed, `/v1/party/vendor-portal/invites/${invite.inviteId}:${action}`, { token: invite.token });
  }

  /** Public: checks an invitation (action verify) or spends it after the identity-provider user is created (accept). */
  async function invitation(req: IncomingMessage, res: ServerResponse, action: "verify" | "accept"): Promise<void> {
    const body = await publicBody(req, res);
    if (!body) return;
    const invite = parseInvite(body["token"]);
    const invalid = (): void => problem(res, 404, "SUP-BFF-404", "the invitation is not valid, has expired or was already used");
    if (!invite) return invalid();
    const verified = await inviteCall(invite, "verify");
    if (!verified) return problem(res, 502, "SUP-BFF-502", "the service did not answer");
    if (verified.status === 404) return invalid();
    if (!verified.ok) return problem(res, 502, "SUP-BFF-502", "the service could not check the invitation");
    const details = (await verified.json()) as { email: string; displayName: string; vendorName: string };
    if (action === "verify") return send(res, 200, { email: details.email, displayName: details.displayName, vendorName: details.vendorName });
    const given = typeof body["displayName"] === "string" ? body["displayName"].trim().slice(0, 140) : "";
    let provisioned = false;
    try {
      const result = await provisioner.createUser({ email: details.email, displayName: given || details.displayName, tenantId: invite.tenantId, legalEntityId: invite.legalEntityId, vendorId: invite.vendorId, role: "supplier-user" });
      provisioned = result.created;
    } catch (err) {
      log({ msg: "creating the invited user failed", vendorId: invite.vendorId, err: String(err) });
      return problem(res, 502, "SUP-BFF-502", "the account could not be created; the invitation is still valid, try again later");
    }
    const accepted = await inviteCall(invite, "accept");
    if (!accepted) return problem(res, 502, "SUP-BFF-502", "the service did not answer");
    if (accepted.status === 404) return invalid();
    if (!accepted.ok) return problem(res, 502, "SUP-BFF-502", "the service could not record the invitation");
    log({ msg: "invitation accepted", vendorId: invite.vendorId, provisioned });
    send(res, 200, { status: "ACCEPTED", provisioned, email: details.email });
  }

  async function handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = new URL(req.url ?? "/", "http://bff.invalid");
    const method = req.method ?? "GET";
    if (url.pathname === "/healthz" || url.pathname === "/readyz") return send(res, 200, { status: "ok" });
    if (method === "GET" && url.pathname === "/bff/login") return login(res);
    if (method === "GET" && url.pathname === "/bff/callback") return callback(url, res);
    if (method === "GET" && url.pathname === "/bff/registration") return registrationStatus(res);
    if (method === "POST" && url.pathname === "/bff/prospects") return registerProspect(req, res);
    if (method === "POST" && url.pathname === "/bff/invites/verify") return invitation(req, res, "verify");
    if (method === "POST" && url.pathname === "/bff/invites/accept") return invitation(req, res, "accept");

    const session = deps.sessions.get(parseCookies(req.headers.cookie).get(SESSION_COOKIE), now());
    if (!session) return problem(res, 401, "SUP-BFF-401", "sign in first");
    if (method !== "GET" && method !== "HEAD") {
      const origin = req.headers.origin;
      if (origin !== undefined && origin !== deps.appOrigin) return problem(res, 403, "SUP-BFF-403", "the request does not come from the portal");
      const csrf = req.headers["x-csrf-token"];
      if (!sameToken(typeof csrf === "string" ? csrf : undefined, session.csrf)) return problem(res, 403, "SUP-BFF-403", "the request has no valid CSRF token");
    }
    if (method === "GET" && url.pathname === "/bff/me") {
      const i = session.identity;
      return send(res, 200, { userId: i.userId, name: i.name, vendorId: i.vendorId, legalEntityIds: i.legalEntityIds, isAdmin: i.isAdmin, csrfToken: session.csrf });
    }
    if (method === "POST" && url.pathname === "/bff/logout") {
      deps.sessions.delete(session.id);
      res.writeHead(204, { "set-cookie": setSession("", 0), "cache-control": "no-store" });
      res.end();
      return;
    }
    if (url.pathname.startsWith("/api/v1/")) return api(req, res, url, session);
    problem(res, 404, "SUP-BFF-404", "no such route");
  }

  return createServer((req, res) => {
    handle(req, res).catch((err: unknown) => {
      log({ msg: "unhandled", err: String(err) });
      if (!res.headersSent) problem(res, 500, "SUP-BFF-500", "internal error");
      else res.end();
    });
  });
}

/** Adds the link a vendor administrator hands to the invited colleague. The token is in this one answer and nowhere else. */
function withAcceptLink(text: string, appOrigin: string): string {
  try {
    const body = JSON.parse(text) as { token?: unknown };
    if (typeof body.token === "string") return JSON.stringify({ ...body, acceptUrl: `${appOrigin}/accept-invite#token=${encodeURIComponent(body.token)}` });
  } catch {
    // not JSON: forwarded as it came
  }
  return text;
}
