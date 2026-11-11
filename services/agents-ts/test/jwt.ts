// Test identity: a realm key pair generated once per test process, bearer tokens signed with it, and the authenticator
// that trusts it. Requests in tests carry a signed token whose claims match their X-Passage-* headers, as the edge sends.

import { createSign, generateKeyPairSync, type JsonWebKey } from "node:crypto";
import { createAuthenticator } from "../src/api/auth.js";

export const ISSUER = "https://idp.test.passage.dev/realms/passage";
export const AUDIENCE = "passage-api";
export const KID = "test-key-1";

const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
export const JWK: JsonWebKey = { ...publicKey.export({ format: "jwk" }), kid: KID, use: "sig", alg: "RS256" } as JsonWebKey;
/** A second realm's key: a token it signs must be refused. */
export const foreignKey = generateKeyPairSync("rsa", { modulusLength: 2048 }).privateKey;

/** Far in the future, so a minted token is deterministic and never expires during a run. */
const EXP = 4_102_444_800;

export interface Claims {
  tid?: unknown;
  sub?: unknown;
  duties?: unknown;
  iss?: unknown;
  aud?: unknown;
  exp?: unknown;
  nbf?: unknown;
}

export function signToken(claims: Claims, opts: { kid?: string; alg?: string; key?: typeof privateKey } = {}): string {
  const head = Buffer.from(JSON.stringify({ alg: opts.alg ?? "RS256", kid: opts.kid ?? KID, typ: "JWT" })).toString("base64url");
  const body = Buffer.from(JSON.stringify({ iss: ISSUER, aud: AUDIENCE, exp: EXP, ...claims })).toString("base64url");
  const signer = createSign("RSA-SHA256");
  signer.update(`${head}.${body}`);
  return `${head}.${body}.${signer.sign(opts.key ?? privateKey).toString("base64url")}`;
}

/** The bearer token the edge would have verified for this caller. */
export function tokenFor(tenant: string, user: string, duties: string): string {
  return signToken({ tid: tenant, sub: user, duties });
}

/** Request headers as the edge forwards them: the caller's token and the matching X-Passage-* headers. */
export function edgeHeaders(tenant: string, user: string, duties: string): Record<string, string> {
  return {
    "content-type": "application/json",
    authorization: `Bearer ${tokenFor(tenant, user, duties)}`,
    "x-passage-tenant": tenant,
    "x-passage-user": user,
    "x-passage-duties": duties,
  };
}

export const testAuthenticate = createAuthenticator({ issuer: ISSUER, audience: AUDIENCE, jwks: { keys: [JWK] } });
