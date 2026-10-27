import { describe, expect, it } from "vitest";
import { checkDrill, displayAmount, fromMicros, toMicros } from "./drill";
import { fiscalCalendar, formatDimensionFilter, parseDimensionFilter, parseReferences } from "./format";

describe("exact drill arithmetic", () => {
  it("sums decimal strings without floats", () => {
    // 0.1 + 0.2 is not 0.3 in floating point; in micro-units it is.
    expect(checkDrill("0.3", [{ amount: "0.1" }, { amount: "0.2" }])).toEqual({ ok: true, sum: "0.300000", difference: "0.000000" });
    expect(checkDrill("10", [{ amount: "9.999999" }])).toEqual({ ok: false, sum: "9.999999", difference: "0.000001" });
    expect(fromMicros(toMicros("-1234.5"))).toBe("-1234.500000");
  });

  it("rejects malformed amounts and more than six decimals", () => {
    expect(() => toMicros("1e3")).toThrow();
    expect(() => toMicros("1.0000001")).toThrow();
  });

  it("formats amounts rounded half away from zero with separators", () => {
    expect(displayAmount("1234567.125000")).toBe("1,234,567.13");
    expect(displayAmount("-1234567.125000")).toBe("-1,234,567.13");
    expect(displayAmount("-0.004")).toBe("0.00");
    expect(displayAmount("999.995")).toBe("1,000.00");
  });
});

describe("designer helpers", () => {
  it("builds a calendar with an opening period and quarters", () => {
    const cal = fiscalCalendar(2025, 2026);
    expect(cal).toHaveLength(26);
    expect(cal[0]).toEqual({ year: 2025, no: 0, quarter: 1 });
    expect(cal.find((p) => p.year === 2026 && p.no === 4)?.quarter).toBe(2);
    expect(cal.find((p) => p.year === 2026 && p.no === 12)?.quarter).toBe(4);
  });

  it("round-trips dimension filters and reads row references", () => {
    const filter = parseDimensionFilter("cost_center=CC100, CC200; region=*");
    expect(filter).toEqual({ cost_center: ["CC100", "CC200"], region: ["*"] });
    expect(parseDimensionFilter(formatDimensionFilter(filter))).toEqual(filter);
    expect(parseReferences("A, B  C,")).toEqual(["A", "B", "C"]);
  });
});
