// Signs the service tokens this BFF sends to the owning services. The format is platform/go/tenant's (Ed25519 over
// "passage.service-token.v1." + body). The token names the vendor (vnd) of the signed-in supplier user; only this service
// may sign one, and the target reads the vendor from it and never from the request.

import { createPrivateKey, randomBytes, sign } from "node:crypto";

const TOKEN_CONTEXT = "passage.service-token.v1.";
const TOKEN_TTL_SECONDS = 60;
const PKCS8_ED25519_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex");
const SEED_BYTES = 32;

export const SERVICE_NAME = "supplier-portal";

/** The duties this service holds on each target. Mirrors platform/go/tenant `serviceGrants` and policies/authz/data.json. */
export const SERVICE_GRANTS = {
  purchasing: ["procurement.vendor-portal.read", "procurement.vendor-portal.acknowledge"],
  receiving: ["receiving.vendor-portal.asn", "receiving.vendor-portal.invoice"],
  party: ["party.vendor-portal.read", "party.vendor-portal.write"],
} as const;

export type Audience = keyof typeof SERVICE_GRANTS;

/** The verified identity of the signed-in supplier user, and the one legal entity the call is for. */
export interface Caller {
  tenantId: string;
  legalEntityId: string;
  userId: string;
  vendorId: string;
}

export interface ServiceTokenSigner {
  mint(audience: Audience, caller: Caller): string;
}

export interface SignerOptions {
  seedBase64: string;
  keyId?: string | undefined;
  now?: () => number;
  newJti?: () => string;
}

export function createServiceTokenSigner(options: SignerOptions): ServiceTokenSigner {
  const seed = Buffer.from(options.seedBase64.trim(), "base64");
  if (seed.length !== SEED_BYTES) throw new TypeError(`the service signing key must be a base64 ${SEED_BYTES}-byte Ed25519 seed`);
  const key = createPrivateKey({ key: Buffer.concat([PKCS8_ED25519_PREFIX, seed]), format: "der", type: "pkcs8" });
  const now = options.now ?? Date.now;
  const newJti = options.newJti ?? (() => randomBytes(16).toString("hex"));
  return {
    mint(audience, caller) {
      if (!caller.tenantId || !caller.legalEntityId || !caller.userId || !caller.vendorId) {
        throw new TypeError("a service token needs a tenant, one legal entity, the acting user and the vendor");
      }
      const iat = Math.floor(now() / 1000);
      const claims = {
        iss: SERVICE_NAME,
        ...(options.keyId ? { kid: options.keyId } : {}),
        aud: audience,
        tid: caller.tenantId,
        le: caller.legalEntityId,
        act: caller.userId,
        vnd: caller.vendorId,
        duties: [...SERVICE_GRANTS[audience]],
        iat,
        exp: iat + TOKEN_TTL_SECONDS,
        jti: newJti(),
      };
      const body = Buffer.from(JSON.stringify(claims)).toString("base64url");
      const signature = sign(null, Buffer.from(TOKEN_CONTEXT + body), key).toString("base64url");
      return `${body}.${signature}`;
    },
  };
}

export function serviceTokenSignerFromEnv(env: Record<string, string | undefined>, readFile: (path: string) => string): ServiceTokenSigner {
  const file = env["PASSAGE_SERVICE_SIGNING_KEY_FILE"];
  const seedBase64 = file ? readFile(file) : (env["PASSAGE_SERVICE_SIGNING_KEY"] ?? "");
  if (!seedBase64.trim()) throw new Error("PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE is required");
  return createServiceTokenSigner({ seedBase64, keyId: env["PASSAGE_SERVICE_KEY_ID"] });
}
