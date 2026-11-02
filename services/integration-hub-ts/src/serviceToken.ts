// Service tokens the hub presents to other Passage services (the same token format as
// services/workflow-ts/src/serviceToken.ts: Ed25519 over "passage.service-token.v1." + body,
// 60 second lifetime, one tenant, one legal entity, the acting principal and a fixed duty grant).
// The signer name is `integration-hub`; the receiving service must list the hub's public key in
// PASSAGE_SERVICE_PUBLIC_KEYS and grant `integration-hub` its duties (handoff, NOT-IMPLEMENTED.md).

import { createPrivateKey, randomBytes, sign } from "node:crypto";

const TOKEN_CONTEXT = "passage.service-token.v1.";
const TOKEN_TTL_SECONDS = 60;
const PKCS8_ED25519_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex");
const SEED_BYTES = 32;

export const SERVICE_NAME = "integration-hub";
export const MIGRATION_AUDIENCE = "migration";
export const MIGRATION_GRANT = ["migration.outbound.render", "migration.xref.record"] as const;
/** The actor recorded on tokens the hub mints for its own background work. */
export const HUB_ACTOR = "service:integration-hub";

/**
 * The verifier (platform/go/tenant VerifyContext) refuses any `service:` actor that is not registered in
 * servicePrincipals, and only `service:integration-hub` is registered for this signer. So a service actor (an
 * event produced by service:sales, say) is never forwarded: the token says the hub acted. Human user ids pass.
 */
export function actorFor(actor: string | undefined): string {
  const a = (actor ?? "").trim();
  return a === "" || a.toLowerCase().startsWith("service:") ? HUB_ACTOR : a;
}

export interface TokenSubject {
  tenantId: string;
  /** The event's legal entity. Required: the platform verifier refuses a token without one (or with the nil UUID). */
  legalEntityId?: string | null;
  /** A human operator's user id (test sends). Background work omits it and signs as HUB_ACTOR. */
  actor?: string;
}

export interface ServiceTokenSigner {
  mintForMigration(subject: TokenSubject): string;
}

export interface SignerOptions {
  seedBase64: string;
  keyId?: string;
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
    mintForMigration({ tenantId, legalEntityId, actor }) {
      if (!tenantId) throw new TypeError("a service token needs a tenant");
      if (!legalEntityId) throw new TypeError("a service token needs one legal entity");
      const iat = Math.floor(now() / 1000);
      const claims = {
        iss: SERVICE_NAME,
        ...(options.keyId ? { kid: options.keyId } : {}),
        aud: MIGRATION_AUDIENCE,
        tid: tenantId,
        le: legalEntityId,
        act: actorFor(actor),
        duties: [...MIGRATION_GRANT],
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

export function serviceTokenSignerFromEnv(env: NodeJS.ProcessEnv, readFile: (path: string) => string): ServiceTokenSigner | null {
  const file = env["PASSAGE_SERVICE_SIGNING_KEY_FILE"];
  const seedBase64 = file ? readFile(file) : (env["PASSAGE_SERVICE_SIGNING_KEY"] ?? "");
  if (!seedBase64.trim()) return null;
  return createServiceTokenSigner({ seedBase64, ...(env["PASSAGE_SERVICE_KEY_ID"] ? { keyId: env["PASSAGE_SERVICE_KEY_ID"] } : {}) });
}
