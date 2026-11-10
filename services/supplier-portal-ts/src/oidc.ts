// OpenID Connect code flow with PKCE against the Keycloak client `passage-portal-supplier` (confidential, secret from the
// environment). The ID token is verified here (RS256, issuer, audience, expiry, nonce) and mapped to a supplier identity;
// the access token is never used or stored. A sign-in without the supplier-user role, a tenant and a vendor is refused.

import { createHash, createPublicKey, randomBytes, verify, type JsonWebKey } from "node:crypto";

export interface OidcConfig {
  issuer: string;
  clientId: string;
  clientSecret: string;
  redirectUri: string;
  authorizationEndpoint: string;
  tokenEndpoint: string;
  jwksUri: string;
}

export interface SupplierIdentity {
  userId: string;
  tenantId: string;
  vendorId: string;
  legalEntityIds: string[];
  name: string;
  isAdmin: boolean;
}

export type Fetch = (url: string, init?: RequestInit) => Promise<Response>;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const NIL = "00000000-0000-0000-0000-000000000000";
const CLOCK_SKEW_SECONDS = 30;

export function randomToken(bytes = 32): string {
  return randomBytes(bytes).toString("base64url");
}

export function pkcePair(): { verifier: string; challenge: string } {
  const verifier = randomToken(48);
  return { verifier, challenge: createHash("sha256").update(verifier).digest("base64url") };
}

export function authorizationUrl(cfg: OidcConfig, state: string, nonce: string, challenge: string): string {
  const url = new URL(cfg.authorizationEndpoint);
  url.search = new URLSearchParams({
    response_type: "code",
    client_id: cfg.clientId,
    redirect_uri: cfg.redirectUri,
    scope: "openid",
    state,
    nonce,
    code_challenge: challenge,
    code_challenge_method: "S256",
  }).toString();
  return url.toString();
}

export class LoginRefused extends Error {}

export async function exchangeCode(cfg: OidcConfig, code: string, verifier: string, fetcher: Fetch): Promise<string> {
  const res = await fetcher(cfg.tokenEndpoint, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded", accept: "application/json" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      redirect_uri: cfg.redirectUri,
      client_id: cfg.clientId,
      client_secret: cfg.clientSecret,
      code_verifier: verifier,
    }).toString(),
    signal: AbortSignal.timeout(10_000),
  });
  if (!res.ok) throw new LoginRefused(`the identity provider refused the code (${res.status})`);
  const body = (await res.json()) as { id_token?: unknown };
  if (typeof body.id_token !== "string") throw new LoginRefused("the identity provider returned no ID token");
  return body.id_token;
}

/** Caches the realm's signing keys and refetches once when a token names a key it does not hold (rotation). */
export class Jwks {
  private keys = new Map<string, JsonWebKey>();
  private readonly uri: string;
  private readonly fetcher: Fetch;
  constructor(uri: string, fetcher: Fetch) {
    this.uri = uri;
    this.fetcher = fetcher;
  }

  async key(kid: string): Promise<JsonWebKey | undefined> {
    if (!this.keys.has(kid)) await this.refresh();
    return this.keys.get(kid);
  }

  private async refresh(): Promise<void> {
    const res = await this.fetcher(this.uri, { signal: AbortSignal.timeout(10_000) });
    if (!res.ok) throw new LoginRefused(`the signing keys could not be read (${res.status})`);
    const body = (await res.json()) as { keys?: Array<JsonWebKey & { kid?: string; use?: string }> };
    this.keys = new Map((body.keys ?? []).filter((k) => k.kty === "RSA" && k.kid && (k.use ?? "sig") === "sig").map((k) => [k.kid as string, k]));
  }
}

function decodePart(part: string): Record<string, unknown> {
  try {
    const value: unknown = JSON.parse(Buffer.from(part, "base64url").toString("utf8"));
    if (typeof value === "object" && value !== null && !Array.isArray(value)) return value as Record<string, unknown>;
  } catch {
    /* falls through */
  }
  throw new LoginRefused("the ID token is malformed");
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

export async function verifyIdToken(token: string, cfg: OidcConfig, jwks: Jwks, nonce: string, nowMs: number): Promise<SupplierIdentity> {
  const parts = token.split(".");
  if (parts.length !== 3) throw new LoginRefused("the ID token is malformed");
  const [h, p, s] = parts as [string, string, string];
  const header = decodePart(h);
  if (header["alg"] !== "RS256" || typeof header["kid"] !== "string") throw new LoginRefused("the ID token is not RS256 with a key id");
  const jwk = await jwks.key(header["kid"]);
  if (!jwk) throw new LoginRefused("the ID token names an unknown signing key");
  if (!verify("RSA-SHA256", Buffer.from(`${h}.${p}`), createPublicKey({ key: jwk, format: "jwk" }), Buffer.from(s, "base64url"))) {
    throw new LoginRefused("the ID token signature is invalid");
  }
  const claims = decodePart(p);
  const now = Math.floor(nowMs / 1000);
  const aud = claims["aud"];
  const audiences = Array.isArray(aud) ? aud : [aud];
  if (claims["iss"] !== cfg.issuer || !audiences.includes(cfg.clientId)) throw new LoginRefused("the ID token is for another issuer or client");
  if (typeof claims["exp"] !== "number" || claims["exp"] + CLOCK_SKEW_SECONDS < now) throw new LoginRefused("the ID token has expired");
  if (typeof claims["iat"] === "number" && claims["iat"] - CLOCK_SKEW_SECONDS > now) throw new LoginRefused("the ID token is from the future");
  if (claims["nonce"] !== nonce) throw new LoginRefused("the ID token does not answer this sign-in");

  const roles = Array.isArray(claims["roles"]) ? (claims["roles"] as unknown[]).filter((r): r is string => typeof r === "string") : [];
  if (!roles.includes("supplier-user") && !roles.includes("supplier-admin")) throw new LoginRefused("the account is not a supplier user");
  const tenantId = str(claims["tid"]).toLowerCase();
  const vendorId = str(claims["vendor_id"]).toLowerCase();
  const userId = str(claims["sub"]).toLowerCase();
  if (!UUID.test(tenantId) || tenantId === NIL || !UUID.test(vendorId) || vendorId === NIL || userId === "") {
    throw new LoginRefused("the account has no tenant or vendor");
  }
  const legalEntityIds = str(claims["le"]).split(",").map((e) => e.trim().toLowerCase()).filter((e) => UUID.test(e) && e !== NIL);
  if (legalEntityIds.length === 0) throw new LoginRefused("the account has no legal entity");
  return { userId, tenantId, vendorId, legalEntityIds, name: str(claims["name"]) || str(claims["preferred_username"]), isAdmin: roles.includes("supplier-admin") };
}
