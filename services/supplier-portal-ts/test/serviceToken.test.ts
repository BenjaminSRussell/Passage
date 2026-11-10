import { createPublicKey, createPrivateKey, randomBytes, verify } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { createServiceTokenSigner, SERVICE_GRANTS, SERVICE_NAME } from "../src/serviceToken.ts";
import { LE_1, TENANT, VENDOR_A, tokenClaims } from "./helpers.ts";

const root = new URL("../../../", import.meta.url);
const data = JSON.parse(readFileSync(new URL("policies/authz/data.json", root), "utf8")) as {
  duties: Record<string, { service_only?: boolean }>;
  service_grants: Record<string, Record<string, string[]>>;
};

describe("service tokens", () => {
  it("are Ed25519 over the platform context and name the vendor", () => {
    const seed = randomBytes(32);
    const signer = createServiceTokenSigner({ seedBase64: seed.toString("base64"), keyId: "k1", now: () => 1_000_000, newJti: () => "jti-1" });
    const token = signer.mint("purchasing", { tenantId: TENANT, legalEntityId: LE_1, userId: "user-1", vendorId: VENDOR_A });
    const [body, sig] = token.split(".") as [string, string];
    const priv = createPrivateKey({ key: Buffer.concat([Buffer.from("302e020100300506032b657004220420", "hex"), seed]), format: "der", type: "pkcs8" });
    expect(verify(null, Buffer.from(`passage.service-token.v1.${body}`), createPublicKey(priv), Buffer.from(sig, "base64url"))).toBe(true);
    expect(tokenClaims(token)).toEqual({
      iss: SERVICE_NAME, kid: "k1", aud: "purchasing", tid: TENANT, le: LE_1, act: "user-1", vnd: VENDOR_A,
      duties: ["procurement.vendor-portal.read", "procurement.vendor-portal.acknowledge"], iat: 1000, exp: 1060, jti: "jti-1",
    });
  });

  it("refuse to be minted without a vendor, a tenant, a legal entity or a user", () => {
    const signer = createServiceTokenSigner({ seedBase64: randomBytes(32).toString("base64") });
    const ok = { tenantId: TENANT, legalEntityId: LE_1, userId: "u", vendorId: VENDOR_A };
    for (const missing of ["tenantId", "legalEntityId", "userId", "vendorId"] as const) {
      expect(() => signer.mint("party", { ...ok, [missing]: "" })).toThrow(TypeError);
    }
  });
});

describe("grants", () => {
  it("match policies/authz/data.json and the Go table, and hold only service-only vendor-portal duties", () => {
    expect(data.service_grants[SERVICE_NAME]).toEqual(SERVICE_GRANTS);
    const src = readFileSync(new URL("platform/go/tenant/service.go", root), "utf8");
    const block = src.slice(src.indexOf(`"${SERVICE_NAME}": {`), src.indexOf("}\n}\n", src.indexOf(`"${SERVICE_NAME}": {`)));
    for (const [aud, duties] of Object.entries(SERVICE_GRANTS)) {
      const line = new RegExp(`"${aud}":\\s*\\{([^}]*)\\}`).exec(block)?.[1] ?? "";
      expect([...line.matchAll(/"([^"]+)"/g)].map((m) => m[1])).toEqual(duties);
      for (const d of duties) {
        expect(data.duties[d]?.service_only, d).toBe(true);
        expect(d).toContain("vendor-portal");
      }
    }
  });
});
