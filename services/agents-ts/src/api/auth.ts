// Caller authentication for the agents API. The caller's identity comes from the bearer token it sends (the same token
// the runtime forwards to the MCP gateway), verified here: RS256 against the realm's signing keys, issuer, audience and
// expiry. The X-Passage-* edge headers are never the source of identity; when present they must agree with the token,
// so a request that reaches the pod without passing the edge (or with headers changed on the way) is refused.

import { createPublicKey, verify, type JsonWebKey, type KeyObject } from "node:crypto";
import type { IncomingHttpHeaders } from "node:http";
import { ApiError, isUuid, type Principal } from "./principal.js";

const NIL_UUID = "00000000-0000-0000-0000-000000000000";
const CLOCK_SKEW_SECONDS = 30;
/** An unknown key id refetches the key set at most this often, so forged kids cannot hammer the identity provider. */
const MIN_REFRESH_MS = 30_000;

export type Fetch = (url: string, init?: RequestInit) => Promise<Response>;

export interface JwtConfig {
  issuer: string;
  audience: string;
  /** Where the realm publishes its signing keys (JWKS), or a fixed key set (the gateway's local JWKS). */
  jwksUrl?: string | undefined;
  jwks?: { keys: JsonWebKey[] } | undefined;
}

export type Authenticate = (headers: IncomingHttpHeaders) => Promise<Principal>;

function unauthorized(detail: string): ApiError {
  return new ApiError(401, "AGT-0401", detail);
}

function header(h: IncomingHttpHeaders, name: string): string | undefined {
  const v = h[name];
  return Array.isArray(v) ? v[0] : v;
}

function decodePart(part: string): Record<string, unknown> {
  try {
    const value: unknown = JSON.parse(Buffer.from(part, "base64url").toString("utf8"));
    if (typeof value === "object" && value !== null && !Array.isArray(value)) return value as Record<string, unknown>;
  } catch {
    /* falls through */
  }
  throw unauthorized("the bearer token is malformed");
}

/** The realm's RS256 signing keys by key id, refetched when a token names a key not held (rotation). */
export class KeySet {
  private keys = new Map<string, KeyObject>();
  private lastFetch = -Infinity;
  private readonly cfg: JwtConfig;
  private readonly fetcher: Fetch;
  private readonly now: () => number;

  constructor(cfg: JwtConfig, fetcher: Fetch, now: () => number = Date.now) {
    this.cfg = cfg;
    this.fetcher = fetcher;
    this.now = now;
    if (cfg.jwks !== undefined) this.load(cfg.jwks.keys);
  }

  async key(kid: string): Promise<KeyObject | undefined> {
    if (!this.keys.has(kid) && this.cfg.jwksUrl && this.now() - this.lastFetch >= MIN_REFRESH_MS) {
      this.lastFetch = this.now();
      const res = await this.fetcher(this.cfg.jwksUrl, { signal: AbortSignal.timeout(10_000) });
      if (!res.ok) throw new Error(`signing keys could not be read (${res.status})`);
      const body = (await res.json()) as { keys?: JsonWebKey[] };
      this.load(body.keys ?? []);
    }
    return this.keys.get(kid);
  }

  private load(keys: JsonWebKey[]): void {
    const next = new Map<string, KeyObject>();
    for (const k of keys as Array<JsonWebKey & { kid?: string; use?: string }>) {
      if (k.kty !== "RSA" || !k.kid || (k.use ?? "sig") !== "sig") continue;
      next.set(k.kid, createPublicKey({ key: k, format: "jwk" }));
    }
    this.keys = next;
  }
}

function dutiesOf(claim: unknown): Set<string> {
  const raw = Array.isArray(claim) ? claim.filter((d): d is string => typeof d === "string") : typeof claim === "string" ? claim.split(",") : [];
  return new Set(raw.map((d) => d.trim()).filter((d) => d !== ""));
}

function sameSet(a: ReadonlySet<string>, b: ReadonlySet<string>): boolean {
  return a.size === b.size && [...a].every((x) => b.has(x));
}

/**
 * Verifies a bearer token and returns the caller it names. A missing, malformed, unsigned or expired token, a token for
 * another issuer or audience, or one without a tenant and user is 401 AGT-0401.
 */
export async function verifyBearer(token: string, cfg: JwtConfig, keys: KeySet, nowMs: number): Promise<Principal> {
  const parts = token.split(".");
  if (parts.length !== 3) throw unauthorized("the bearer token is malformed");
  const [h, p, s] = parts as [string, string, string];
  const head = decodePart(h);
  if (head["alg"] !== "RS256" || typeof head["kid"] !== "string") throw unauthorized("the bearer token is not RS256 with a key id");
  let key: KeyObject | undefined;
  try {
    key = await keys.key(head["kid"]);
  } catch {
    throw new ApiError(503, "AGT-0503", "the signing keys could not be read; retry");
  }
  if (key === undefined) throw unauthorized("the bearer token names an unknown signing key");
  if (!verify("RSA-SHA256", Buffer.from(`${h}.${p}`), key, Buffer.from(s, "base64url"))) {
    throw unauthorized("the bearer token signature is invalid");
  }
  const claims = decodePart(p);
  const now = Math.floor(nowMs / 1000);
  const aud = claims["aud"];
  const audiences = Array.isArray(aud) ? aud : [aud];
  if (claims["iss"] !== cfg.issuer || !audiences.includes(cfg.audience)) throw unauthorized("the bearer token is for another issuer or audience");
  if (typeof claims["exp"] !== "number" || claims["exp"] + CLOCK_SKEW_SECONDS < now) throw unauthorized("the bearer token has expired");
  if (typeof claims["nbf"] === "number" && claims["nbf"] - CLOCK_SKEW_SECONDS > now) throw unauthorized("the bearer token is not valid yet");
  const tenantId = typeof claims["tid"] === "string" ? claims["tid"].trim().toLowerCase() : "";
  const userId = typeof claims["sub"] === "string" ? claims["sub"].trim().toLowerCase() : "";
  if (!isUuid(tenantId) || tenantId === NIL_UUID || !isUuid(userId)) throw unauthorized("the bearer token names no tenant and user");
  return { tenantId, userId, duties: dutiesOf(claims["duties"]) };
}

/**
 * The authenticator the router uses: the caller is the verified bearer token. Edge headers that are present must match it.
 */
export function createAuthenticator(cfg: JwtConfig, fetcher: Fetch = fetch, now: () => number = Date.now): Authenticate {
  if (!cfg.issuer || !cfg.audience || (!cfg.jwksUrl && cfg.jwks === undefined)) {
    throw new TypeError("caller authentication needs an issuer, an audience and the signing keys (a JWKS URL or key set)");
  }
  const keys = new KeySet(cfg, fetcher, now);
  return async (headers) => {
    const raw = header(headers, "authorization");
    const m = raw === undefined ? null : /^Bearer ([A-Za-z0-9._~+/=-]+)$/.exec(raw.trim());
    if (m === null || m[1] === undefined) throw unauthorized("a bearer token is required");
    const principal = await verifyBearer(m[1], cfg, keys, now());
    const tenant = header(headers, "x-passage-tenant");
    const user = header(headers, "x-passage-user");
    const duties = header(headers, "x-passage-duties");
    if (
      (tenant !== undefined && tenant.trim().toLowerCase() !== principal.tenantId) ||
      (user !== undefined && user.trim().toLowerCase() !== principal.userId) ||
      (duties !== undefined && !sameSet(dutiesOf(duties), principal.duties))
    ) {
      throw unauthorized("the edge headers do not match the bearer token");
    }
    return principal;
  };
}
