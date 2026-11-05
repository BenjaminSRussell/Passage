// A token minted by the Go signer (platform/go/tenant, written by services/fulfillment/internal/carrier
// TestGoMintedVector) is accepted here, so the two implementations agree on the format.

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { createVerifier, parsePublicKeys, ServiceTokenError } from "../src/serviceToken.ts";

const v = JSON.parse(readFileSync(new URL("../testdata/go-minted-token.json", import.meta.url), "utf8")) as {
  issuer: string;
  publicKey: string;
  nowMs: number;
  token: string;
};

describe("Go-minted service token", () => {
  it("verifies at its clock and is refused once expired", () => {
    const keys = parsePublicKeys(`${v.issuer}=${v.publicKey}`);
    const claims = createVerifier({ keys, now: () => v.nowMs }).verify(v.token);
    expect(claims).toMatchObject({ iss: "fulfillment", aud: "carrier-gateway", duties: ["carrier.label.create"], act: "packer-1" });
    expect(() => createVerifier({ keys, now: () => v.nowMs + 61_000 }).verify(v.token)).toThrow(ServiceTokenError);
  });
});
