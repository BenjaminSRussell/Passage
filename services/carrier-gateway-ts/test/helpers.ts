import { createPrivateKey, createPublicKey, randomBytes, sign, type KeyObject } from "node:crypto";
import type { ServiceClaims } from "../src/serviceToken.ts";

const PKCS8_ED25519_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex");

export const TENANT = "0190a5f2-7b1c-7d3e-8a4f-1234567890ab";
export const ENTITY = "0190a5f2-7b1c-7d3e-8a4f-1234567890ac";
export const NOW_MS = Date.UTC(2026, 10, 5, 12, 0, 0);

export interface TestSigner {
  seed: Buffer;
  publicKey: KeyObject;
  publicKeyBase64: string;
  mint(overrides?: Partial<ServiceClaims>): string;
}

/** Mints tokens exactly as platform/go/tenant ServiceSigner.Mint does. */
export function testSigner(iss = "fulfillment", nowMs = () => NOW_MS): TestSigner {
  const seed = randomBytes(32);
  const key = createPrivateKey({ key: Buffer.concat([PKCS8_ED25519_PREFIX, seed]), format: "der", type: "pkcs8" });
  const publicKey = createPublicKey(key);
  const raw = publicKey.export({ format: "der", type: "spki" }).subarray(-32);
  return {
    seed,
    publicKey,
    publicKeyBase64: raw.toString("base64"),
    mint(overrides = {}) {
      const iat = Math.floor(nowMs() / 1000);
      const claims = {
        iss,
        aud: "carrier-gateway",
        tid: TENANT,
        le: ENTITY,
        act: "0190a5f2-7b1c-7d3e-8a4f-0000000000aa",
        duties: ["carrier.rate.quote", "carrier.label.create", "carrier.tracking.read"],
        iat,
        exp: iat + 60,
        jti: randomBytes(16).toString("hex"),
        ...overrides,
      };
      const body = Buffer.from(JSON.stringify(claims)).toString("base64url");
      return `${body}.${sign(null, Buffer.from("passage.service-token.v1." + body), key).toString("base64url")}`;
    },
  };
}

export const US_FROM = { name: "Demo Warehouse", street1: "1 Dock Rd", city: "Columbus", postalCode: "43004", country: "US" };
export const US_TO = { name: "Acme Corp", street1: "200 Main St", city: "Austin", postalCode: "73301", country: "US" };
export const DE_TO = { name: "Muster GmbH", street1: "Hauptstr. 1", city: "Berlin", postalCode: "10115", country: "DE" };
export const SMALL = { weightKg: "2.5", lengthCm: "30", widthCm: "20", heightCm: "10" };
export const BULKY = { weightKg: "1", lengthCm: "50", widthCm: "40", heightCm: "30" };
