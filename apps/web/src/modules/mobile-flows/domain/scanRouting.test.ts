import { describe, expect, it } from "vitest";
import type { ScanClass } from "./gs1";
import { routeScan, type FocusedStep } from "./scanRouting";

const scanStep = (...expectedScan: ScanClass[]): FocusedStep => ({ type: "scan", expectedScan });

describe("focus gating (AC-32.7, FR-32.10)", () => {
  it("ignores a DataWedge ]C1 scan when a display step has focus", () => {
    const d = routeScan("]C10109501101020917", { type: "display" });
    expect(d).toEqual({ kind: "ignored", reason: "Scan ignored: the focused step is a display step, not a scan step." });
  });

  it("ignores a scan when no step has focus", () => {
    expect(routeScan("LP-000123", null)).toMatchObject({ kind: "ignored", reason: expect.stringMatching(/missing/) });
  });

  it("ignores a scan on a confirm step", () => {
    expect(routeScan("A1-01-02", { type: "confirm" }).kind).toBe("ignored");
  });
});

describe("expected scan class (BR-32.2)", () => {
  it.each<[string, string, ScanClass, string | null]>([
    ["GS1 SSCC satisfies LP", "]C100340123456789012340", "LP", null],
    ["plain LP format satisfies LP", "LP-000123", "LP", null],
    ["GS1 GTIN satisfies GTIN", "]C10109501101020917", "GTIN", null],
    ["plain GTIN with a valid check digit satisfies GTIN", "09501101020917", "GTIN", null],
    ["GS1 GTIN with a bad check digit is reported as MOB-0102", "]C10109501101020918", "GTIN", "MOB-0102"],
    ["GS1 GTIN where an LP is expected is a mismatch", "]C10109501101020917", "LP", "MOB-0101"],
    ["location format satisfies LOCATION", "A1-01-02", "LOCATION", null],
    ["unprefixed 10-01-02 is a location, not AI 10", "10-01-02", "LOCATION", null],
    ["LP scanned where a location is expected is a mismatch", "LP-000123", "LOCATION", "MOB-0101"],
    ["ANY accepts plain text", "whatever 123", "ANY", null],
  ])("%s", (_name, raw, expected, code) => {
    const d = routeScan(raw, scanStep(expected));
    if (code === null) expect(d).toMatchObject({ kind: "accepted", scanClass: expected });
    else expect(d).toMatchObject({ kind: "rejected", code });
  });

  it("explains what was expected and what was scanned", () => {
    expect(routeScan("LP-000123", scanStep("LOCATION"))).toMatchObject({ kind: "rejected", message: "Expected location, scanned LP." });
  });

  it("accepts the first of several classes the scan satisfies and names them all on a mismatch", () => {
    expect(routeScan("A1-01-02", scanStep("LP", "LOCATION"))).toMatchObject({ kind: "accepted", scanClass: "LOCATION" });
    expect(routeScan("]C10109501101020917", scanStep("LP", "LOCATION"))).toMatchObject({
      kind: "rejected",
      code: "MOB-0101",
      message: "Expected LP or location, scanned GTIN.",
    });
  });

  it("reports an unknown AI as MOB-0103 from the parser", () => {
    expect(routeScan("]C12345", scanStep("LP"))).toMatchObject({ kind: "rejected", code: "MOB-0103" });
  });

  it("applies the configured FNC1 substitute before parsing", () => {
    const d = routeScan("]C110ABC~17261231", scanStep("ANY"), { fnc1Substitute: "~" });
    expect(d).toMatchObject({ kind: "accepted" });
    if (d.kind === "accepted") expect(d.classified.gs1?.fields).toMatchObject({ "10": "ABC", "17": "2026-12-31" });
  });

  it("with requireSymbologyId, unprefixed GS1 text is a plain value and fails an LP step", () => {
    expect(routeScan("00340123456789012340", scanStep("LP"), { requireSymbologyId: true })).toMatchObject({ kind: "rejected", code: "MOB-0101" });
  });

  it("treats a scan step without classes as ANY", () => {
    expect(routeScan("anything", { type: "scan" }).kind).toBe("accepted");
  });
});
