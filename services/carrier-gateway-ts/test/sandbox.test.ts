import { describe, expect, it } from "vitest";
import { CarrierError } from "../src/adapter.ts";
import { billableGrams, gs1CheckDigit, SandboxCarrier } from "../src/sandbox.ts";
import { BULKY, DE_TO, NOW_MS, SMALL, US_FROM, US_TO } from "./helpers.ts";

describe("sandbox carrier", () => {
  let clock = NOW_MS;
  const carrier = new SandboxCarrier({ now: () => clock });

  it("computes the GS1 mod-10 check digit", () => {
    expect(gs1CheckDigit("400638133393")).toBe(1); // EAN-13 4006381333931
  });

  it("bills the larger of actual and volumetric weight, rounded up to 100 g", () => {
    expect(billableGrams(SMALL)).toBe(2500); // 6,000 cm³ / 5,000 = 1.2 kg < 2.5 kg
    expect(billableGrams(BULKY)).toBe(12000); // 60,000 cm³ / 5,000 = 12 kg
    expect(billableGrams({ weightKg: "0.01", lengthCm: "1", widthCm: "1", heightCm: "1" })).toBe(100);
  });

  it("rates by the published formula (hand-computed)", async () => {
    const domestic = await carrier.rate({ shipFrom: US_FROM, shipTo: US_TO, parcels: [SMALL] });
    // GROUND 6.50 + 0.85 × 2.5 = 8.625 → 8.63 (up); EXPRESS 14.00 + 2.10 × 2.5 = 19.25.
    expect(domestic.map((r) => [r.service, r.amount, r.transitDays])).toEqual([
      ["GROUND", "8.63", 3],
      ["EXPRESS", "19.25", 1],
    ]);
    const two = await carrier.rate({ shipFrom: US_FROM, shipTo: US_TO, parcels: [SMALL, BULKY], services: ["GROUND"] });
    expect(two).toEqual([{ carrier: "sandbox", service: "GROUND", amount: "18.83", currency: "USD", transitDays: 3, billableWeightKg: "14.500" }]);
    const intl = await carrier.rate({ shipFrom: US_FROM, shipTo: DE_TO, parcels: [SMALL], services: ["GROUND"] });
    expect(intl[0]).toMatchObject({ amount: "20.63", transitDays: 6 });
  });

  it("refuses unknown services and countries it does not serve", async () => {
    await expect(carrier.rate({ shipFrom: US_FROM, shipTo: US_TO, parcels: [SMALL], services: ["OVERNIGHT"] })).rejects.toMatchObject({ code: "UNKNOWN_SERVICE" });
    await expect(carrier.rate({ shipFrom: US_FROM, shipTo: { ...US_TO, country: "BR" }, parcels: [SMALL] })).rejects.toBeInstanceOf(CarrierError);
  });

  it("derives the same label from the same key and time, and a different one from another key", async () => {
    clock = NOW_MS;
    const req = { shipFrom: US_FROM, shipTo: US_TO, parcels: [SMALL], service: "GROUND", idempotencyKey: "k1", reference: "SH-1" };
    const a = await carrier.label(req);
    const b = await carrier.label(req);
    const c = await carrier.label({ ...req, idempotencyKey: "k2" });
    expect(a).toEqual(b);
    expect(c.trackingNumber).not.toBe(a.trackingNumber);
    expect(a.trackingNumber).toMatch(/^SBX103\d{15}$/);
    expect(a.amount).toBe("8.63");
    const zpl = Buffer.from(a.document, "base64").toString("utf8");
    expect(zpl).toContain("^XA");
    expect(zpl).toContain(`^FD${a.trackingNumber}^FS`);
    expect(zpl).toContain("SANDBOX, NOT FOR SHIPPING");
  });

  it("strips ZPL command prefixes from label text", async () => {
    const a = await carrier.label({ shipFrom: US_FROM, shipTo: { ...US_TO, name: "Evil^XZ~JA" }, parcels: [SMALL], service: "GROUND", idempotencyKey: "z" });
    const zpl = Buffer.from(a.document, "base64").toString("utf8");
    expect(zpl.match(/\^XZ/g)).toHaveLength(1);
    expect(zpl).not.toContain("~JA");
  });

  it("tracks from the tracking number and the clock: labelled, in transit, delivered", async () => {
    clock = NOW_MS;
    const { trackingNumber } = await carrier.label({ shipFrom: US_FROM, shipTo: US_TO, parcels: [SMALL], service: "GROUND", idempotencyKey: "t" });
    const at = async (hours: number) => {
      clock = NOW_MS + hours * 3600_000;
      return carrier.track(trackingNumber);
    };
    expect((await at(0)).status).toBe("LABELLED");
    expect((await at(5)).events.map((e) => e.status)).toEqual(["LABELLED", "IN_TRANSIT"]);
    const done = await at(72);
    expect(done.status).toBe("DELIVERED");
    expect(done.events.map((e) => e.eventKey)).toEqual([1, 2, 3].map((n) => `${trackingNumber}:${n}`));
    expect(done.events[2]!.eventTime).toBe(new Date(NOW_MS + 72 * 3600_000).toISOString());
    expect(await at(500)).toEqual(done); // stable once delivered
  });

  it("ends in EXCEPTION for the exception postal code", async () => {
    clock = NOW_MS;
    const { trackingNumber } = await carrier.label({ shipFrom: US_FROM, shipTo: { ...US_TO, postalCode: "00000" }, parcels: [SMALL], service: "EXPRESS", idempotencyKey: "x" });
    clock = NOW_MS + 30 * 3600_000;
    const t = await carrier.track(trackingNumber);
    expect(t.status).toBe("EXCEPTION");
    expect(t.events.map((e) => e.status)).toEqual(["LABELLED", "IN_TRANSIT", "EXCEPTION"]);
  });

  it("refuses tracking numbers it did not issue", async () => {
    clock = NOW_MS;
    const { trackingNumber } = await carrier.label({ shipFrom: US_FROM, shipTo: US_TO, parcels: [SMALL], service: "GROUND", idempotencyKey: "n" });
    const last = Number(trackingNumber.slice(-1));
    await expect(carrier.track(trackingNumber.slice(0, -1) + String((last + 1) % 10))).rejects.toMatchObject({ code: "NOT_FOUND" });
    await expect(carrier.track("1Z999AA10123456784")).rejects.toMatchObject({ code: "NOT_FOUND" });
    clock = NOW_MS - 1000;
    await expect(carrier.track(trackingNumber)).rejects.toMatchObject({ code: "NOT_FOUND" }); // from the future
  });
});
