import { describe, expect, it } from "vitest";
import { createVerifier, parsePublicKeys, ServiceTokenError } from "../src/serviceToken.ts";
import { NOW_MS, testSigner } from "./helpers.ts";

describe("service token verifier", () => {
  let clock = NOW_MS;
  const fulfillment = testSigner("fulfillment", () => clock);
  const transport = testSigner("transport", () => clock);
  const stranger = testSigner("sales", () => clock);
  const keys = parsePublicKeys(
    `fulfillment=${fulfillment.publicKeyBase64}, transport:k2=${transport.publicKeyBase64},sales=${stranger.publicKeyBase64}`,
  );
  const v = createVerifier({ keys, now: () => clock });
  const refused = (token: string) => expect(() => v.verify(token)).toThrow(ServiceTokenError);

  it("accepts a fresh token from a granted caller, once", () => {
    clock = NOW_MS;
    const token = fulfillment.mint();
    expect(v.verify(token)).toMatchObject({ iss: "fulfillment", aud: "carrier-gateway" });
    refused(token); // replay
  });

  it("checks the key id", () => {
    clock = NOW_MS;
    expect(v.verify(transport.mint({ kid: "k2", duties: ["carrier.tracking.read"] })).iss).toBe("transport");
    refused(transport.mint({ duties: ["carrier.tracking.read"] })); // no kid: no key ""
  });

  it("refuses duties outside the issuer's grant", () => {
    clock = NOW_MS;
    refused(transport.mint({ kid: "k2", duties: ["carrier.label.create"] }));
    refused(stranger.mint({ duties: ["carrier.rate.quote"] }));
    refused(fulfillment.mint({ duties: [] }));
    refused(fulfillment.mint({ duties: ["org.switcher.all_scope"] }));
  });

  it("refuses tokens for another audience, expired, too long-lived or from the future", () => {
    clock = NOW_MS;
    const iat = Math.floor(NOW_MS / 1000);
    refused(fulfillment.mint({ aud: "fulfillment" }));
    refused(fulfillment.mint({ iat: iat - 61, exp: iat - 1 }));
    refused(fulfillment.mint({ iat, exp: iat + 61 }));
    refused(fulfillment.mint({ iat: iat + 6, exp: iat + 30 }));
    expect(v.verify(fulfillment.mint({ iat: iat + 5, exp: iat + 30 })).iss).toBe("fulfillment"); // within the skew
  });

  it("refuses bad tenant, entity, actor and token id", () => {
    clock = NOW_MS;
    refused(fulfillment.mint({ tid: "0190A5F2-7B1C-7D3E-8A4F-1234567890AB" }));
    refused(fulfillment.mint({ le: "" }));
    refused(fulfillment.mint({ le: "00000000-0000-0000-0000-000000000000" }));
    refused(fulfillment.mint({ act: "" }));
    refused(fulfillment.mint({ act: "service:fulfillment-order" }));
    refused(fulfillment.mint({ jti: "" }));
  });

  it("refuses a tampered or forged token", () => {
    clock = NOW_MS;
    const [body, sig] = fulfillment.mint().split(".") as [string, string];
    const claims = JSON.parse(Buffer.from(body, "base64url").toString());
    const forged = Buffer.from(JSON.stringify({ ...claims, tid: "0190a5f2-7b1c-7d3e-8a4f-1234567890ff" })).toString("base64url");
    refused(`${forged}.${sig}`);
    refused(`${body}`);
    refused("x".repeat(5000));
    const unknown = testSigner("fulfillment", () => clock);
    refused(unknown.mint());
  });

  it("parses the key list like the Go verifier", () => {
    expect(() => parsePublicKeys("fulfillment=abc")).toThrow(TypeError);
    expect(() => parsePublicKeys(`a=${fulfillment.publicKeyBase64},a=${fulfillment.publicKeyBase64}`)).toThrow(/repeats/);
    expect(parsePublicKeys("").size).toBe(0);
  });

  it("bounds the replay cache", () => {
    clock = NOW_MS;
    const small = createVerifier({ keys, now: () => clock, replayCapacity: 1 });
    small.verify(fulfillment.mint());
    expect(() => small.verify(fulfillment.mint())).toThrow(ServiceTokenError);
    clock = NOW_MS + 61_000; // the first token expired and is evicted
    small.verify(fulfillment.mint());
  });
});
