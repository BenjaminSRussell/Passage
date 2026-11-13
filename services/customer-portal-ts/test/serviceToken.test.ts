import { createPublicKey, createPrivateKey, randomBytes, verify } from "node:crypto";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { createServiceTokenSigner, SERVICE_GRANTS, SERVICE_NAME } from "../src/serviceToken.ts";
import { CUSTOMER_A, LE_1, TENANT, tokenClaims } from "./helpers.ts";

const root = new URL("../../../", import.meta.url);
const data = JSON.parse(readFileSync(new URL("policies/authz/data.json", root), "utf8")) as {
  duties: Record<string, { service_only?: boolean; permissions: { methods: string[] }[] }>;
  service_grants: Record<string, Record<string, string[]>>;
};

describe("service tokens", () => {
  it("are Ed25519 over the platform context and name the customer", () => {
    const seed = randomBytes(32);
    const signer = createServiceTokenSigner({ seedBase64: seed.toString("base64"), keyId: "k1", now: () => 1_000_000, newJti: () => "jti-1" });
    const token = signer.mint("billing", { tenantId: TENANT, legalEntityId: LE_1, userId: "user-1", customerId: CUSTOMER_A });
    const [body, sig] = token.split(".") as [string, string];
    const priv = createPrivateKey({ key: Buffer.concat([Buffer.from("302e020100300506032b657004220420", "hex"), seed]), format: "der", type: "pkcs8" });
    expect(verify(null, Buffer.from(`passage.service-token.v1.${body}`), createPublicKey(priv), Buffer.from(sig, "base64url"))).toBe(true);
    expect(tokenClaims(token)).toEqual({
      iss: SERVICE_NAME, kid: "k1", aud: "billing", tid: TENANT, le: LE_1, act: "user-1", cus: CUSTOMER_A,
      duties: ["billing.customer-portal.read"], iat: 1000, exp: 1060, jti: "jti-1",
    });
  });

  it("carries only the duties a call asks for, and refuses one outside the grant", () => {
    const signer = createServiceTokenSigner({ seedBase64: randomBytes(32).toString("base64") });
    const caller = { tenantId: TENANT, legalEntityId: LE_1, userId: "u", customerId: CUSTOMER_A };
    expect(tokenClaims(signer.mint("sales", caller, ["sales.customer-portal.order"]))["duties"]).toEqual(["sales.customer-portal.order"]);
    expect(tokenClaims(signer.mint("sales", caller))["duties"]).toEqual([...SERVICE_GRANTS.sales]);
    for (const bad of [["sales.order.write"], ["billing.customer-portal.read"], []]) expect(() => signer.mint("sales", caller, bad)).toThrow(TypeError);
    expect(() => signer.mint("party", caller, ["party.read"])).toThrow(TypeError);
  });

  it("never carries a vendor", () => {
    const signer = createServiceTokenSigner({ seedBase64: randomBytes(32).toString("base64") });
    const token = signer.mint("sales", { tenantId: TENANT, legalEntityId: LE_1, userId: "u", customerId: CUSTOMER_A });
    expect(tokenClaims(token)).not.toHaveProperty("vnd");
  });

  it("refuse to be minted without a customer, a tenant, a legal entity or a user", () => {
    const signer = createServiceTokenSigner({ seedBase64: randomBytes(32).toString("base64") });
    const ok = { tenantId: TENANT, legalEntityId: LE_1, userId: "u", customerId: CUSTOMER_A };
    for (const missing of ["tenantId", "legalEntityId", "userId", "customerId"] as const) {
      expect(() => signer.mint("receivables", { ...ok, [missing]: "" })).toThrow(TypeError);
    }
  });
});

describe("grants", () => {
  it("match policies/authz/data.json and the Go table, and hold only service-only customer-portal duties, with no write but an order and a return (and print's renderer, a POST that reads nothing)", () => {
    expect(data.service_grants[SERVICE_NAME]).toEqual(SERVICE_GRANTS);
    const src = readFileSync(new URL("platform/go/tenant/service.go", root), "utf8");
    const block = src.slice(src.indexOf(`"${SERVICE_NAME}": {`), src.indexOf("}\n}\n", src.indexOf(`"${SERVICE_NAME}": {`)));
    for (const [aud, duties] of Object.entries(SERVICE_GRANTS)) {
      const line = new RegExp(`"${aud}":\\s*\\{([^}]*)\\}`).exec(block)?.[1] ?? "";
      expect([...line.matchAll(/"([^"]+)"/g)].map((m) => m[1])).toEqual(duties);
      for (const d of duties) {
        expect(data.duties[d]?.service_only, d).toBe(true);
        expect(d).toContain("customer-portal");
        const write = d === "sales.customer-portal.order" || d === "sales.customer-portal.return" || d === "print.customer-portal.render";
        for (const perm of data.duties[d]?.permissions ?? []) expect(perm.methods, d).toEqual(write ? ["POST"] : ["GET"]);
      }
    }
  });
});
