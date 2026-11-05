import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import { validateRate } from "./RateShopPage";
import { fixed, matchTone, money, stateTone, sumAmounts } from "./format";
import { validateTender } from "./NewTenderPage";
import { validateTracking } from "./NewTrackingEventPage";
import { validateVoyage, type VoyageValues } from "./NewVoyagePage";
import transport from "./module";
import { validateCard, type CardValues } from "./NewRateCardPage";

const CARRIER = "0195f0a0-0000-7000-8000-0000000000c1";
const SERVICE = "0195f0a0-0000-7000-8000-0000000000e1";

describe("transport module", () => {
  it("declares its routes and duty-gated nav under /transport", () => {
    expect(transport.id).toBe("transport");
    expect(transport.routes.map((r) => r.path)).toEqual([
      "/transport",
      "/transport/loads",
      "/transport/loads/build",
      "/transport/loads/:id",
      "/transport/rate-shop",
      "/transport/rate-cards",
      "/transport/rate-cards/new",
      "/transport/freight-bills",
      "/transport/freight-bills/new",
      "/transport/freight-bills/:id",
      "/transport/carrier-invoices",
      "/transport/carrier-invoices/new",
      "/transport/tenders",
      "/transport/tenders/new",
      "/transport/tenders/:id",
      "/transport/tracking",
      "/transport/tracking/new",
      "/transport/tracking/:loadId",
      "/transport/voyages",
      "/transport/voyages/new",
      "/transport/voyages/:id",
      "/transport/goods-in-transit",
      "/transport/goods-in-transit/new",
      "/transport/goods-in-transit/:id",
      "/transport/gate",
      "/transport/gate/check-in",
      "/transport/gate/:id",
      "/transport/screening-holds",
      "/transport/screening-holds/:id",
      "/transport/routes",
      "/transport/routes/new",
      "/transport/routes/:id",
      "/transport/load-plans",
    ]);
    expect(transport.routes.every((r) => r.element !== null && r.element !== undefined)).toBe(true);
    expect(transport.nav.every((n) => n.to.startsWith("/transport") && n.duty?.startsWith("transport."))).toBe(true);
    expect(transport.nav.find((n) => n.to === "/transport/carrier-invoices")?.duty).toBe("transport.freight-bill.reconcile");
    expect(transport.nav.find((n) => n.to === "/transport/tenders")?.duty).toBe("transport.load.tender");
    expect(transport.nav.find((n) => n.to === "/transport/voyages")?.duty).toBe("transport.landed-cost.maintain");
    expect(transport.nav.find((n) => n.to === "/transport/loads")?.duty).toBe("transport.view");
    expect(transport.nav.find((n) => n.to === "/transport/gate")?.duty).toBe("transport.gate.operate");
    expect(transport.nav.find((n) => n.to === "/transport/goods-in-transit")?.duty).toBe("transport.landed-cost.maintain");
    expect(transport.nav.find((n) => n.to === "/transport/routes")?.duty).toBe("transport.load.plan");
    expect(transport.nav.find((n) => n.to === "/transport/screening-holds")?.duty).toBe("transport.view");
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

  it("sums decimal strings exactly", () => {
    expect(sumAmounts(["0.1", "0.2"])).toBe("0.300000");
    expect(sumAmounts(["1000.00", "45.55", "-0.01"])).toBe("1045.540000");
    expect(sumAmounts([])).toBe("0.000000");
    expect(money(sumAmounts(["333.33", "333.33", "333.34"]), "USD")).toBe("1000.00 USD");
  });

  it("maps tender, stop and voyage states to tones", () => {
    expect(stateTone("ACCEPTED")).toBe("success");
    expect(stateTone("TENDER_FAILED")).toBe("danger");
    expect(stateTone("EXPIRED")).toBe("warning");
    expect(stateTone("PICKED_UP")).toBe("info");
    expect(stateTone("POSTED")).toBe("success");
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

const LOAD = "0195f0a0-0000-7000-8000-0000000000a1";

describe("tender validation", () => {
  it("builds the request with quoted rates and the default response time", () => {
    const { input, loadId } = validateTender({
      loadId: ` ${LOAD} `,
      carriers: [{ carrierId: CARRIER, rateQuoted: "412.50" }, { carrierId: SERVICE, rateQuoted: "" }],
      currency: "USD",
      timeoutMin: "",
    });
    expect(loadId).toBe(LOAD);
    expect(input).toEqual({ carriers: [{ carrierId: CARRIER, rateQuoted: "412.50" }, { carrierId: SERVICE }], currency: "USD", timeoutMin: 120 });
  });

  it("leaves the currency out without rates and reports what the service would reject", () => {
    expect(validateTender({ loadId: LOAD, carriers: [{ carrierId: CARRIER, rateQuoted: "" }], currency: "USD", timeoutMin: "30" }).input).toEqual({
      carriers: [{ carrierId: CARRIER }],
      timeoutMin: 30,
    });
    const { errors, input } = validateTender({
      loadId: "load-1",
      carriers: [{ carrierId: CARRIER, rateQuoted: "1.005" }, { carrierId: CARRIER, rateQuoted: "" }],
      currency: "",
      timeoutMin: "4",
    });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["carriers[0]", "carriers[1]", "currency", "loadId", "timeoutMin"]);
  });
});

describe("tracking validation", () => {
  it("reads the time as UTC and drops an empty location", () => {
    const { input } = validateTracking({ loadId: LOAD, stopNo: "2", code: "D1", occurredAt: "2026-06-01T11:00", location: " ", source: "DRIVER", ref: " call-7 " });
    expect(input).toEqual({ loadId: LOAD, stopNo: 2, code: "D1", occurredAt: "2026-06-01T11:00:00Z", source: "DRIVER", ref: "call-7" });
  });

  it("requires a stop, a time and a reference", () => {
    const { errors } = validateTracking({ loadId: LOAD, stopNo: "0", code: "AF", occurredAt: "", location: "", source: "API", ref: "" });
    expect(Object.keys(errors).sort()).toEqual(["occurredAt", "ref", "stopNo"]);
  });
});

describe("voyage validation", () => {
  const valid: VoyageValues = {
    reference: " MAEU-2611E ",
    currency: "USD",
    legs: [{ from: "CNSHA", to: "USLAX", mode: "OCEAN" }],
    containers: [{ number: "CSQU3054383", type: "40HC", seal: "" }],
    folios: [{ folioNo: "F-1", forwarder: "Kuehne", containers: "CSQU3054383, " }],
    costLines: [{ costType: "FREIGHT", amount: "1000.00", allocationMethod: "WEIGHT" }],
    receiptLines: [{ line: "1", value: "100", weight: "1", volume: "", quantity: "" }],
  };

  it("builds the request the service expects", () => {
    expect(validateVoyage(valid).input).toEqual({
      reference: "MAEU-2611E",
      currency: "USD",
      legs: [{ from: "CNSHA", to: "USLAX", mode: "OCEAN" }],
      containers: [{ number: "CSQU3054383", type: "40HC" }],
      folios: [{ folioNo: "F-1", forwarder: "Kuehne", containers: ["CSQU3054383"] }],
      costLines: [{ costType: "FREIGHT", amount: "1000.00", allocationMethod: "WEIGHT" }],
      receiptLines: [{ line: 1, value: "100", weight: "1", volume: "0", quantity: "0" }],
    });
  });

  it("reports the rows the service would reject", () => {
    const { errors, input } = validateVoyage({
      ...valid,
      containers: [{ number: "CSQU305438", type: "40HC", seal: "" }],
      folios: [{ folioNo: "F-1", forwarder: "Kuehne", containers: "TGHU0000013" }],
      costLines: [{ costType: "FREIGHT", amount: "10.005", allocationMethod: "WEIGHT" }],
      receiptLines: [
        { line: "1", value: "100", weight: "1", volume: "", quantity: "" },
        { line: "1", value: "100", weight: "1", volume: "", quantity: "" },
      ],
    });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["containers[0]", "costLines[0]", "folios[0]", "receiptLines[1]"]);
  });
});
