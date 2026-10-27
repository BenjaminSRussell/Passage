import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import { validateRate } from "./RateShopPage";
import { fixed, matchTone, money } from "./format";
import transport from "./module";
import { validateCard, type CardValues } from "./NewRateCardPage";

const CARRIER = "0195f0a0-0000-7000-8000-0000000000c1";
const SERVICE = "0195f0a0-0000-7000-8000-0000000000e1";

describe("transport module", () => {
  it("declares its routes and duty-gated nav under /transport", () => {
    expect(transport.id).toBe("transport");
    expect(transport.routes.map((r) => r.path)).toEqual([
      "/transport",
      "/transport/rate-shop",
      "/transport/rate-cards",
      "/transport/rate-cards/new",
      "/transport/freight-bills",
      "/transport/freight-bills/new",
      "/transport/freight-bills/:id",
      "/transport/carrier-invoices",
      "/transport/carrier-invoices/new",
    ]);
    expect(transport.routes.every((r) => r.element !== null && r.element !== undefined)).toBe(true);
    expect(transport.nav.every((n) => n.to.startsWith("/transport") && n.duty?.startsWith("TMS_"))).toBe(true);
    expect(transport.nav.find((n) => n.to === "/transport/carrier-invoices")?.duty).toBe("TMS_RECONCILE");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("transport");
  });
});

describe("format", () => {
  it("rounds decimal strings half away from zero without floats", () => {
    expect(fixed("412.5", 2)).toBe("412.50");
    expect(fixed("0.005", 2)).toBe("0.01");
    expect(fixed("-5.555", 2)).toBe("-5.56");
    expect(fixed("999.999", 2)).toBe("1000.00");
    expect(money(undefined, "USD")).toBe("—");
    expect(money("418", "USD")).toBe("418.00 USD");
  });

  it("maps match states to tones", () => {
    expect(matchTone("MATCHED")).toBe("success");
    expect(matchTone("VARIANCE")).toBe("warning");
    expect(matchTone("UNEXPECTED_INVOICE")).toBe("danger");
    expect(matchTone("UNMATCHED")).toBe("neutral");
  });
});

describe("rate card validation", () => {
  const valid: CardValues = {
    carrierId: CARRIER,
    serviceId: SERVICE,
    basis: "WEIGHT_BREAK",
    currency: "USD",
    validFrom: "2026-01-01",
    validTo: "",
    minimumCharge: "15",
    dimDivisor: "",
    fuelPct: "18.5",
    rows: [{ origin: "chi", destination: "*", breakFrom: "0", breakTo: "100", rate: "1.00", unit: "KG" }],
    accessorials: [{ code: "liftgate", kind: "flat", amount: "25" }],
  };

  it("builds the request the service expects", () => {
    expect(validateCard(valid).input).toEqual({
      carrierId: CARRIER,
      serviceId: SERVICE,
      basis: "WEIGHT_BREAK",
      currency: "USD",
      validFrom: "2026-01-01",
      validTo: null,
      minimumCharge: "15",
      fuelPct: "18.5",
      rows: [{ origin: "CHI", destination: "*", breakFrom: "0", breakTo: "100", rate: "1.00", unit: "KG" }],
      accessorials: [{ code: "LIFTGATE", flat: "25", pct: "0" }],
    });
  });

  it("reports the fields the service would reject", () => {
    const { errors, input } = validateCard({
      ...valid,
      carrierId: CARRIER.toUpperCase(),
      currency: "usd",
      validTo: "2025-12-31",
      rows: [{ origin: "", destination: "*", breakFrom: "0", breakTo: "", rate: "1", unit: "KG" }],
      accessorials: [{ code: "", kind: "pct", amount: "5" }],
    });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["accessorials[0]", "carrierId", "currency", "rows[0]", "validTo"]);
  });

  it("rejects a break band whose upper bound is not above the lower", () => {
    const { errors } = validateCard({ ...valid, rows: [{ ...valid.rows[0], breakFrom: "100", breakTo: "50" }] });
    expect(errors["rows[0]"]).toMatch(/above the break from/);
  });
});

describe("rate shop validation", () => {
  it("normalises zones and accessorial codes", () => {
    const { body } = validateRate({
      shipmentId: CARRIER,
      originZone: " chi ",
      destinationZone: "dal",
      weightKg: "80",
      volumeM3: "0",
      pallets: "1",
      distanceKm: "",
      shipDate: "2026-06-01",
      accessorials: "liftgate, resi",
    });
    expect(body).toEqual({
      originZone: "CHI",
      destinationZone: "DAL",
      weightKg: "80",
      volumeM3: "0",
      pallets: 1,
      shipDate: "2026-06-01",
      accessorialCodes: ["LIFTGATE", "RESI"],
    });
  });
});
