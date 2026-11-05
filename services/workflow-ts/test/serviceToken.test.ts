import { createPrivateKey, createPublicKey, verify } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  createServiceTokenSigner,
  ENGINEERING_GRANT,
  PAYABLES_GRANT,
  QUALITY_GRANT,
  SERVICE_NAME,
  serviceTokenSignerFromEnv,
  PARTY_GRANT,
  PARTY_MERGE_GRANT,
  TRANSPORT_GRANT,
} from "../src/serviceToken.js";

const vector = JSON.parse(
  readFileSync(fileURLToPath(new URL("../../../platform/go/tenant/testdata/workflow_ts_token_vector.json", import.meta.url)), "utf8"),
) as Record<string, string | number>;

describe("service token", () => {
  it("reproduces the vector the Go verifier accepts, byte for byte", () => {
    const signer = createServiceTokenSigner({
      seedBase64: String(vector.seedBase64),
      keyId: String(vector.keyId),
      now: () => Number(vector.iat) * 1000,
      newJti: () => String(vector.jti),
    });
    const token = signer.mintForPayables({ tenantId: String(vector.tenantId), legalEntityId: String(vector.legalEntityId), actor: String(vector.actor) });
    expect(token).toBe(vector.token);
  });

  it("mirrors the workflow-ts grant in policies/authz/data.json", () => {
    const data = JSON.parse(readFileSync(fileURLToPath(new URL("../../../policies/authz/data.json", import.meta.url)), "utf8")) as {
      service_grants: Record<string, Record<string, string[]>>;
    };
    expect(data.service_grants[SERVICE_NAME]).toEqual({
      payables: [...PAYABLES_GRANT],
      engineering: [...ENGINEERING_GRANT],
      quality: [...QUALITY_GRANT],
      transport: [...TRANSPORT_GRANT],
      party: [...PARTY_GRANT, ...PARTY_MERGE_GRANT],
    });
  });

  it("signs each target with its own audience and its single service duty", () => {
    const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 1).toString("base64") });
    const subject = { tenantId: "t", legalEntityId: "l", actor: "a", scope: ["L"] };
    const claims = (token: string) => JSON.parse(Buffer.from(token.split(".")[0]!, "base64url").toString()) as { aud: string; duties: string[]; iss: string };
    expect(claims(signer.mintForEngineering(subject))).toMatchObject({ iss: SERVICE_NAME, aud: "engineering", duties: ["engineering.eco.workflow"] });
    expect(claims(signer.mintForQuality(subject))).toMatchObject({ iss: SERVICE_NAME, aud: "quality", duties: ["quality.capa.execute"] });
    expect(claims(signer.mintForTransport(subject))).toMatchObject({ iss: SERVICE_NAME, aud: "transport", duties: ["transport.tender.execute"] });
    expect(claims(signer.mintForParty(subject))).toMatchObject({ iss: SERVICE_NAME, aud: "party", duties: ["party.approve"] });
    expect(claims(signer.mintForPayables(subject))).toMatchObject({ aud: "payables", duties: ["payment-run.execute"] });
  });

  it("refuses to mint for engineering, quality or transport outside the acting user's legal-entity scope", () => {
    const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 1).toString("base64") });
    const subject = { tenantId: "t", legalEntityId: "le-1", actor: "a" };
    for (const scope of [undefined, [], ["le-2"], ["le-2", ""]]) {
      expect(() => signer.mintForEngineering({ ...subject, scope })).toThrow(/scope/);
      expect(() => signer.mintForQuality({ ...subject, scope })).toThrow(/scope/);
      expect(() => signer.mintForTransport({ ...subject, scope })).toThrow(/scope/);
      expect(() => signer.mintForParty({ ...subject, scope })).toThrow(/scope/);
    }
    expect(() => signer.mintForEngineering({ ...subject, scope: ["le-2", " LE-1 "] })).not.toThrow();
    expect(() => signer.mintForTransport({ ...subject, scope: ["le-2", " LE-1 "] })).not.toThrow();
    expect(() => signer.mintForPayables(subject)).not.toThrow();
  });

  it("expires after 60 seconds and omits kid when none is set", () => {
    const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 1).toString("base64"), now: () => 1_000_000 });
    const claims = JSON.parse(Buffer.from(signer.mintForPayables({ tenantId: "t", legalEntityId: "l", actor: "a" }).split(".")[0]!, "base64url").toString());
    expect(claims.exp - claims.iat).toBe(60);
    expect("kid" in claims).toBe(false);
  });

  it("refuses a token without a tenant, legal entity or acting user", () => {
    const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 1).toString("base64") });
    for (const bad of [{ tenantId: "", legalEntityId: "l", actor: "a" }, { tenantId: "t", legalEntityId: "", actor: "a" }, { tenantId: "t", legalEntityId: "l", actor: "" }]) {
      expect(() => signer.mintForPayables(bad)).toThrow(/needs/);
      expect(() => signer.mintForEngineering({ ...bad, scope: ["l"] })).toThrow(/needs/);
      expect(() => signer.mintForQuality({ ...bad, scope: ["l"] })).toThrow(/needs/);
    }
  });

  it("refuses a seed of the wrong size", () => {
    expect(() => createServiceTokenSigner({ seedBase64: Buffer.alloc(16).toString("base64") })).toThrow(/32-byte/);
  });

  it("reads the seed from the environment or a mounted file and fails without one", () => {
    const seed = Buffer.alloc(32, 3).toString("base64");
    expect(() => serviceTokenSignerFromEnv({ PASSAGE_SERVICE_SIGNING_KEY: seed }, () => "")).not.toThrow();
    expect(() => serviceTokenSignerFromEnv({ PASSAGE_SERVICE_SIGNING_KEY_FILE: "/secret" }, () => `${seed}\n`)).not.toThrow();
    expect(() => serviceTokenSignerFromEnv({}, () => "")).toThrow(/required/);
  });

  it("prefers the mounted key file over the environment variable", () => {
    const mounted = Buffer.alloc(32, 7);
    const stale = Buffer.alloc(32, 9).toString("base64");
    const signer = serviceTokenSignerFromEnv({ PASSAGE_SERVICE_SIGNING_KEY: stale, PASSAGE_SERVICE_SIGNING_KEY_FILE: "/secret" }, () => `${mounted.toString("base64")}\n`);
    const [body, signature] = signer.mintForPayables({ tenantId: "t", legalEntityId: "l", actor: "a" }).split(".");
    const publicKey = createPublicKey(createPrivateKey({ key: Buffer.concat([Buffer.from("302e020100300506032b657004220420", "hex"), mounted]), format: "der", type: "pkcs8" }));
    expect(verify(null, Buffer.from(`passage.service-token.v1.${body}`), publicKey, Buffer.from(signature!, "base64url"))).toBe(true);
  });
});
