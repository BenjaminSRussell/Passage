// Verifies the signed service tokens other Passage services present in X-Passage-Service-Token: the same format
// and checks as platform/go/tenant ServiceVerifier.VerifyContext (Ed25519 over "passage.service-token.v1." + body,
// addressed to this service, at most 60 s lifetime, 5 s clock skew, a canonical tenant and legal entity, every duty
// within the issuer's grant to carrier-gateway, a token id never seen before).
//
// SERVICE_GRANTS mirrors service_grants[*]["carrier-gateway"] in policies/authz/data.json and the serviceGrants table
// in platform/go/tenant/service.go; test/grants.test.ts keeps the three equal.

import { createPublicKey, verify, type KeyObject } from "node:crypto";

export const SERVICE_NAME = "carrier-gateway";
export const HEADER = "x-passage-service-token";
const TOKEN_CONTEXT = "passage.service-token.v1.";
const MAX_TTL_S = 60;
const CLOCK_SKEW_S = 5;
const MAX_TOKEN_SIZE = 4096;
const SPKI_ED25519_PREFIX = Buffer.from("302a300506032b6570032100", "hex");
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const NIL_UUID = "00000000-0000-0000-0000-000000000000";

export const DUTY_RATE = "carrier.rate.quote";
export const DUTY_LABEL = "carrier.label.create";
export const DUTY_TRACK = "carrier.tracking.read";

export const SERVICE_GRANTS: Readonly<Record<string, readonly string[]>> = {
  fulfillment: [DUTY_RATE, DUTY_LABEL, DUTY_TRACK],
  transport: [DUTY_RATE, DUTY_TRACK],
};

export interface ServiceClaims {
  iss: string;
  kid?: string;
  aud: string;
  tid: string;
  le: string;
  act: string;
  duties: string[];
  iat: number;
  exp: number;
  jti: string;
}

export class ServiceTokenError extends Error {}

export interface VerifierOptions {
  /** issuer -> key id ("" for none) -> public key. */
  keys: Map<string, Map<string, KeyObject>>;
  now?: () => number;
  replayCapacity?: number;
}

export interface ServiceTokenVerifier {
  verify(token: string): ServiceClaims;
}

export function createVerifier(options: VerifierOptions): ServiceTokenVerifier {
  const now = options.now ?? Date.now;
  const capacity = options.replayCapacity ?? 100_000;
  // jti -> exp; insertion order is close to expiry order because the lifetime is capped at 60 s.
  const seen = new Map<string, number>();

  function remember(key: string, exp: number, nowS: number): boolean {
    for (const [k, e] of seen) {
      if (e > nowS) break;
      seen.delete(k);
    }
    if (seen.has(key) || seen.size >= capacity) return false;
    seen.set(key, exp);
    return true;
  }

  return {
    verify(token: string): ServiceClaims {
      const fail = (): never => {
        throw new ServiceTokenError("invalid service token");
      };
      if (typeof token !== "string" || token.length === 0 || token.length > MAX_TOKEN_SIZE) fail();
      const dot = token.indexOf(".");
      if (dot < 0) fail();
      const body = token.slice(0, dot);
      const sig = token.slice(dot + 1);
      if (!/^[A-Za-z0-9_-]+$/.test(body) || !/^[A-Za-z0-9_-]+$/.test(sig)) fail();
      let c: ServiceClaims;
      try {
        c = JSON.parse(Buffer.from(body, "base64url").toString("utf8")) as ServiceClaims;
      } catch {
        return fail();
      }
      if (typeof c !== "object" || c === null) fail();
      const key = typeof c.iss === "string" ? options.keys.get(c.iss)?.get(typeof c.kid === "string" ? c.kid : "") : undefined;
      if (!key || !verify(null, Buffer.from(TOKEN_CONTEXT + body), key, Buffer.from(sig, "base64url"))) fail();
      const nowS = Math.floor(now() / 1000);
      // No service principal (`service:` actor) is registered for a carrier-gateway caller: every grant is
      // exercised on behalf of a person (a shipping clerk or planner), so a service actor is refused.
      if (typeof c.act !== "string" || c.act.trim() === "" || c.act.trim().toLowerCase().startsWith("service:")) fail();
      if (c.aud !== SERVICE_NAME || typeof c.jti !== "string" || c.jti === "") fail();
      if (!Number.isInteger(c.exp) || !Number.isInteger(c.iat) || c.exp <= nowS || c.exp - c.iat > MAX_TTL_S || c.iat > nowS + CLOCK_SKEW_S) fail();
      for (const id of [c.tid, c.le]) {
        if (typeof id !== "string" || !UUID.test(id) || id === NIL_UUID) fail();
      }
      const grant = SERVICE_GRANTS[c.iss] ?? [];
      if (!Array.isArray(c.duties) || c.duties.length === 0 || c.duties.some((d) => !grant.includes(d))) fail();
      if (!remember(`${c.iss}:${c.jti}`, c.exp, nowS)) fail();
      return c;
    },
  };
}

/** Parses PASSAGE_SERVICE_PUBLIC_KEYS: comma separated `name[:kid]=base64 Ed25519 public key` (as the Go verifier). */
export function parsePublicKeys(raw: string): Map<string, Map<string, KeyObject>> {
  const keys = new Map<string, Map<string, KeyObject>>();
  for (const entry of raw.split(",").map((e) => e.trim()).filter((e) => e !== "")) {
    const eq = entry.indexOf("=");
    const id = eq < 0 ? entry : entry.slice(0, eq);
    const [name = "", kid = ""] = id.split(/:(.*)/s);
    const pub = eq < 0 ? Buffer.alloc(0) : Buffer.from(entry.slice(eq + 1), "base64");
    if (eq < 0 || name === "" || pub.length !== 32) throw new TypeError(`PASSAGE_SERVICE_PUBLIC_KEYS entry ${JSON.stringify(id)} is not name[:kid]=base64 Ed25519 public key`);
    const byKid = keys.get(name) ?? new Map<string, KeyObject>();
    if (byKid.has(kid)) throw new TypeError(`PASSAGE_SERVICE_PUBLIC_KEYS repeats ${name}:${kid}`);
    byKid.set(kid, createPublicKey({ key: Buffer.concat([SPKI_ED25519_PREFIX, pub]), format: "der", type: "spki" }));
    keys.set(name, byKid);
  }
  return keys;
}
