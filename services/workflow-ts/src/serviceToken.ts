import { createPrivateKey, randomBytes, sign } from "node:crypto";

const TOKEN_CONTEXT = "passage.service-token.v1.";
const TOKEN_TTL_SECONDS = 60;
const PKCS8_ED25519_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex");
const SEED_BYTES = 32;

export const SERVICE_NAME = "workflow-ts";
export const PAYABLES_AUDIENCE = "payables";
export const PAYABLES_GRANT = ["payment-run.execute"] as const;
export const ENGINEERING_AUDIENCE = "engineering";
export const ENGINEERING_GRANT = ["engineering.eco.workflow"] as const;
export const QUALITY_AUDIENCE = "quality";
export const QUALITY_GRANT = ["quality.capa.execute"] as const;
export const TRANSPORT_AUDIENCE = "transport";
export const TRANSPORT_GRANT = ["transport.tender.execute"] as const;
export const PARTY_AUDIENCE = "party";
export const PARTY_GRANT = ["party.approve"] as const;
/** Tenant-level: a merge moves roles in every legal entity, so this token names none (#450). Go `tenantLevelDuties` mirrors it. */
export const PARTY_MERGE_GRANT = ["party.merge.execute"] as const;

export interface TokenSubject {
  tenantId: string;
  legalEntityId: string;
  actor: string;
  scope?: readonly string[];
}

/** The acting user of a tenant-level token: a tenant administrator with no legal-entity claim. */
export interface TenantTokenSubject {
  tenantId: string;
  actor: string;
}

export function inLegalEntityScope(scope: readonly string[] | undefined, legalEntityId: string): boolean {
  const wanted = legalEntityId.trim().toLowerCase();
  return !!wanted && (scope ?? []).some((entry) => entry.trim().toLowerCase() === wanted);
}

export interface ServiceTokenSigner {
  mintForPayables(subject: TokenSubject): string;
  mintForEngineering(subject: TokenSubject): string;
  mintForQuality(subject: TokenSubject): string;
  mintForTransport(subject: TokenSubject): string;
  mintForParty(subject: TokenSubject): string;
  mintForPartyMerge(subject: TenantTokenSubject): string;
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
  function mint(audience: string, grant: readonly string[], { tenantId, legalEntityId, actor, scope }: TokenSubject, scoped: boolean): string {
    if (!tenantId || !legalEntityId || !actor) throw new TypeError("a service token needs a tenant, one legal entity and the acting user");
    if (scoped && !inLegalEntityScope(scope, legalEntityId)) throw new TypeError("a service token needs a legal entity within the acting user's scope");
    return sealed(audience, grant, tenantId, legalEntityId, actor);
  }
  function sealed(audience: string, grant: readonly string[], tenantId: string, legalEntityId: string | undefined, actor: string): string {
    const iat = Math.floor(now() / 1000);
    const claims = {
      iss: SERVICE_NAME,
      ...(options.keyId ? { kid: options.keyId } : {}),
      aud: audience,
      tid: tenantId,
      ...(legalEntityId === undefined ? {} : { le: legalEntityId }),
      act: actor,
      duties: [...grant],
      iat,
      exp: iat + TOKEN_TTL_SECONDS,
      jti: newJti(),
    };
    const body = Buffer.from(JSON.stringify(claims)).toString("base64url");
    const signature = sign(null, Buffer.from(TOKEN_CONTEXT + body), key).toString("base64url");
    return `${body}.${signature}`;
  }
  return {
    mintForPayables: (subject) => mint(PAYABLES_AUDIENCE, PAYABLES_GRANT, subject, false),
    mintForEngineering: (subject) => mint(ENGINEERING_AUDIENCE, ENGINEERING_GRANT, subject, true),
    mintForQuality: (subject) => mint(QUALITY_AUDIENCE, QUALITY_GRANT, subject, true),
    mintForTransport: (subject) => mint(TRANSPORT_AUDIENCE, TRANSPORT_GRANT, subject, true),
    mintForParty: (subject) => mint(PARTY_AUDIENCE, PARTY_GRANT, subject, true),
    mintForPartyMerge: ({ tenantId, actor }) => {
      if (!tenantId || !actor) throw new TypeError("a tenant-level service token needs a tenant and the acting user");
      return sealed(PARTY_AUDIENCE, PARTY_MERGE_GRANT, tenantId, undefined, actor);
    },
  };
}

export function serviceTokenSignerFromEnv(
  env: Record<string, string | undefined>,
  readFile: (path: string) => string,
): ServiceTokenSigner {
  const file = env.PASSAGE_SERVICE_SIGNING_KEY_FILE;
  const seedBase64 = file ? readFile(file) : (env.PASSAGE_SERVICE_SIGNING_KEY ?? "");
  if (!seedBase64.trim()) throw new Error("PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE is required");
  return createServiceTokenSigner({ seedBase64, keyId: env.PASSAGE_SERVICE_KEY_ID });
}
