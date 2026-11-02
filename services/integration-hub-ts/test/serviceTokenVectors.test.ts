import { readFileSync } from "node:fs";
import { describe, expect, test } from "vitest";

import { createServiceTokenSigner, MIGRATION_GRANT } from "../src/serviceToken.ts";

// The same tokens are verified by the real platform/go/tenant ServiceVerifier in testdata/goverify, so a drift on
// either side (claim names or order, the signing context, the actor rule, the grant) fails one of the two suites.
const doc = JSON.parse(readFileSync(new URL("../testdata/service-token-vectors.json", import.meta.url), "utf8")) as {
  seedBase64: string;
  nowUnixSeconds: number;
  tenantId: string;
  legalEntityId: string;
  duties: string[];
  vectors: { name: string; actor?: string; jti: string; token: string; act: string }[];
};

describe("service tokens the Go verifier checks (testdata/service-token-vectors.json)", () => {
  test("the vectors carry the hub's grant", () => {
    expect(doc.duties).toEqual([...MIGRATION_GRANT]);
  });

  test.each(doc.vectors.map((v) => [v.name, v] as const))("%s: the hub mints the same token byte for byte", (_name, v) => {
    const signer = createServiceTokenSigner({ seedBase64: doc.seedBase64, now: () => doc.nowUnixSeconds * 1000, newJti: () => v.jti });
    const token = signer.mintForMigration({ tenantId: doc.tenantId, legalEntityId: doc.legalEntityId, ...(v.actor === undefined ? {} : { actor: v.actor }) });
    expect(token).toBe(v.token);
    const claims = JSON.parse(Buffer.from(token.split(".")[0] as string, "base64url").toString("utf8")) as Record<string, unknown>;
    expect(claims["act"]).toBe(v.act);
    expect(claims["duties"]).toEqual(doc.duties);
  });
});
