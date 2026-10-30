import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { createServiceTokenSigner, PAYABLES_GRANT, SERVICE_NAME, serviceTokenSignerFromEnv } from "../src/serviceToken.js";

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
    expect(data.service_grants[SERVICE_NAME]).toEqual({ payables: [...PAYABLES_GRANT] });
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
});
