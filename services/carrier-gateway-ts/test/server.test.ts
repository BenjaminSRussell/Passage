import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { CarrierError, type CarrierAdapter } from "../src/adapter.ts";
import { Gateway } from "../src/gateway.ts";
import { adaptersFromEnv } from "../src/main.ts";
import { SandboxCarrier } from "../src/sandbox.ts";
import { createHttpServer } from "../src/server.ts";
import { createVerifier, parsePublicKeys } from "../src/serviceToken.ts";
import { DE_TO, NOW_MS, SMALL, TENANT, testSigner, US_FROM, US_TO } from "./helpers.ts";

let clock = NOW_MS;
const fulfillment = testSigner("fulfillment", () => clock);
const transport = testSigner("transport", () => clock);

/** A second carrier that is down, to show rate shopping reports it without failing the others. */
const down: CarrierAdapter = {
  code: "down",
  sandbox: true,
  services: () => [{ code: "ANY", name: "Any" }],
  rate: async () => {
    throw new CarrierError("UNAVAILABLE", "carrier timeout");
  },
  label: async () => {
    throw new CarrierError("UNAVAILABLE", "carrier timeout");
  },
  track: async () => {
    throw new CarrierError("UNAVAILABLE", "carrier timeout");
  },
};

const server = createHttpServer({
  gateway: new Gateway([new SandboxCarrier({ now: () => clock }), down]),
  verifier: createVerifier({ keys: parsePublicKeys(`fulfillment=${fulfillment.publicKeyBase64},transport=${transport.publicKeyBase64}`), now: () => clock }),
  log: () => {},
});
let base = "";
beforeAll(async () => {
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});
afterAll(() => new Promise<void>((resolve) => server.close(() => resolve())));

async function call(method: string, path: string, opts: { token?: string; body?: unknown; key?: string } = {}) {
  const headers: Record<string, string> = {};
  if (opts.token !== undefined) headers["x-passage-service-token"] = opts.token;
  if (opts.body !== undefined) headers["content-type"] = "application/json";
  if (opts.key !== undefined) headers["idempotency-key"] = opts.key;
  const res = await fetch(base + path, { method, headers, ...(opts.body !== undefined ? { body: JSON.stringify(opts.body) } : {}) });
  return { status: res.status, type: res.headers.get("content-type"), body: (await res.json()) as Record<string, any> };
}

const shipment = { shipFrom: US_FROM, shipTo: US_TO, parcels: [SMALL] };

describe("carrier-gateway HTTP API", () => {
  it("answers health without a token and refuses /v1 without one", async () => {
    expect((await call("GET", "/healthz")).status).toBe(200);
    const r = await call("POST", "/v1/carrier/rates", { body: shipment });
    expect(r).toMatchObject({ status: 401, type: "application/problem+json", body: { code: "CGW-0401" } });
    expect((await call("POST", "/v1/carrier/rates", { token: "nope", body: shipment })).status).toBe(401);
  });

  it("lists carriers and their services", async () => {
    const r = await call("GET", "/v1/carrier/carriers", { token: fulfillment.mint() });
    expect(r.status).toBe(200);
    expect(r.body["items"].map((c: { code: string }) => c.code)).toEqual(["sandbox", "down"]);
  });

  it("rate-shops across carriers, cheapest first, reporting a carrier that is down", async () => {
    const r = await call("POST", "/v1/carrier/rates", { token: transport.mint({ duties: ["carrier.rate.quote"] }), body: shipment });
    expect(r.status).toBe(200);
    expect(r.body["items"].map((x: { service: string; amount: string }) => `${x.service} ${x.amount}`)).toEqual(["GROUND 8.63", "EXPRESS 19.25"]);
    expect(r.body["failures"]).toEqual([{ carrier: "down", code: "UNAVAILABLE", message: "carrier timeout" }]);
    const only = await call("POST", "/v1/carrier/rates", { token: fulfillment.mint(), body: { ...shipment, carriers: ["sandbox"], services: ["EXPRESS"] } });
    expect(only.body).toMatchObject({ items: [{ service: "EXPRESS" }], failures: [] });
    expect((await call("POST", "/v1/carrier/rates", { token: fulfillment.mint(), body: { ...shipment, carriers: ["ups"] } })).body["code"]).toBe("CGW-0101");
  });

  it("validates the shipment", async () => {
    const r = await call("POST", "/v1/carrier/rates", {
      token: fulfillment.mint(),
      body: { shipFrom: { ...US_FROM, country: "us" }, shipTo: US_TO, parcels: [{ ...SMALL, weightKg: "2.5555" }] },
    });
    expect(r.status).toBe(400);
    expect(Object.keys(r.body["errors"]).sort()).toEqual(["parcels[0].weightKg", "shipFrom.country"]);
    expect((await call("POST", "/v1/carrier/rates", { token: fulfillment.mint(), body: { ...shipment, parcels: [] } })).body["errors"]).toHaveProperty("parcels");
  });

  it("refuses a duty the token does not carry", async () => {
    const r = await call("POST", "/v1/carrier/labels", { token: fulfillment.mint({ duties: ["carrier.rate.quote"] }), key: "k", body: {} });
    expect(r).toMatchObject({ status: 403, body: { code: "CGW-0403" } });
  });

  it("buys a label once per Idempotency-Key and replays it", async () => {
    clock = NOW_MS;
    const body = { ...shipment, carrier: "sandbox", service: "GROUND", reference: "SH-1" };
    expect((await call("POST", "/v1/carrier/labels", { token: fulfillment.mint(), body })).status).toBe(428);
    const first = await call("POST", "/v1/carrier/labels", { token: fulfillment.mint(), key: "ship-1", body });
    expect(first.status).toBe(201);
    expect(first.body).toMatchObject({ carrier: "sandbox", service: "GROUND", amount: "8.63", format: "ZPL", created: true });
    clock = NOW_MS + 10_000; // a retry later must not buy a second label
    const again = await call("POST", "/v1/carrier/labels", { token: fulfillment.mint(), key: "ship-1", body });
    expect(again.status).toBe(200);
    expect(again.body).toEqual({ ...first.body, created: false });
    const changed = await call("POST", "/v1/carrier/labels", { token: fulfillment.mint(), key: "ship-1", body: { ...body, shipTo: DE_TO } });
    expect(changed).toMatchObject({ status: 422, body: { code: "CGW-0422" } });
    // The same key in another tenant is another label.
    const other = await call("POST", "/v1/carrier/labels", { token: fulfillment.mint({ tid: TENANT.replace(/ab$/, "cd") }), key: "ship-1", body });
    expect(other.status).toBe(201);
    expect(other.body["trackingNumber"]).not.toBe(first.body["trackingNumber"]);
  });

  it("maps carrier refusals and outages to problems", async () => {
    const body = { ...shipment, carrier: "sandbox", service: "OVERNIGHT" };
    expect((await call("POST", "/v1/carrier/labels", { token: fulfillment.mint(), key: "bad", body })).body["code"]).toBe("CGW-0102");
    const notServed = await call("POST", "/v1/carrier/labels", { token: fulfillment.mint(), key: "br", body: { ...body, service: "GROUND", shipTo: { ...US_TO, country: "BR" } } });
    expect(notServed.body["code"]).toBe("CGW-0103");
    const outage = await call("POST", "/v1/carrier/labels", { token: fulfillment.mint(), key: "down", body: { ...body, carrier: "down", service: "ANY" } });
    expect(outage).toMatchObject({ status: 503, body: { code: "CGW-0105" } });
  });

  it("tracks a label", async () => {
    clock = NOW_MS;
    const label = await call("POST", "/v1/carrier/labels", { token: fulfillment.mint(), key: "track-1", body: { ...shipment, carrier: "sandbox", service: "EXPRESS" } });
    clock = NOW_MS + 25 * 3600_000;
    const t = await call("GET", `/v1/carrier/tracking/sandbox/${label.body["trackingNumber"]}`, { token: transport.mint({ duties: ["carrier.tracking.read"] }) });
    expect(t.status).toBe(200);
    expect(t.body).toMatchObject({ status: "DELIVERED", trackingNumber: label.body["trackingNumber"] });
    expect((await call("GET", "/v1/carrier/tracking/sandbox/SBX000000000000000001", { token: fulfillment.mint() })).status).toBe(404);
    expect((await call("POST", "/v1/carrier/tracking/sandbox/x", { token: fulfillment.mint() })).status).toBe(405);
    expect((await call("GET", "/v1/carrier/nothing", { token: fulfillment.mint() })).status).toBe(404);
  });

  it("starts only with built adapters", () => {
    expect(adaptersFromEnv(undefined).map((a) => a.code)).toEqual(["sandbox"]);
    expect(() => adaptersFromEnv("sandbox,easypost")).toThrow(/only "sandbox"/);
  });
});
