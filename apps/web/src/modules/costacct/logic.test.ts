import { describe, expect, it } from "vitest";
import costacct from "./module";
import { loadModules } from "../registry";
import {
  buildBasis,
  formatAmount,
  formatTicks,
  isDate,
  isPeriod,
  netPostingsBalanced,
  sumTicks,
  toTicks,
  validateAccountRange,
  validateBehaviorRule,
  validateCodeForm,
} from "./logic";

describe("formatTicks", () => {
  it("formats with four places and keeps the sign", () => {
    expect(formatTicks(125000n)).toBe("12.5000");
    expect(formatTicks(-1n)).toBe("-0.0001");
    expect(formatTicks(0n)).toBe("0.0000");
  });
});

describe("toTicks", () => {
  it.each([
    ["12.5", 125000n],
    ["-0.0001", -1n],
    ["0", 0n],
    ["1.00001", null],
    ["1,00", null],
  ])("toTicks(%s)", (value, want) => {
    expect(toTicks(value)).toBe(want);
  });
});

describe("sums and conservation", () => {
  it("sums exactly without float drift", () => {
    expect(sumTicks(["0.1", "0.2"])).toBe(3000n);
  });

  it("rejects an invalid amount in the list", () => {
    expect(sumTicks(["1", "x"])).toBeNull();
  });

  it("net postings balance only when they sum to zero", () => {
    expect(netPostingsBalanced(["-100.00", "0.00", "100.00"])).toBe(true);
    expect(netPostingsBalanced(["-100.00", "0.01", "100.00"])).toBe(false);
  });
});

describe("buildBasis", () => {
  it("treats blanks as zero and ignores the diagonal", () => {
    expect(buildBasis([["9", "1"], ["", "5"]])).toEqual({ ok: true, value: [["0", "1"], ["0", "0"]] });
  });

  it("rejects a negative or non-numeric basis and a ragged grid", () => {
    expect(buildBasis([["0", "-1"], ["1", "0"]]).ok).toBe(false);
    expect(buildBasis([["0", "x"], ["1", "0"]]).ok).toBe(false);
    expect(buildBasis([["0", "1"], ["1"]]).ok).toBe(false);
  });
});

describe("formatAmount", () => {
  it("rounds to cents without floats", () => {
    expect(formatAmount("350.25")).toBe("350.25");
    expect(formatAmount("-0.005")).toBe("-0.01");
    expect(formatAmount("-0.004")).toBe("0.00");
    expect(formatAmount("100")).toBe("100.00");
  });
});

describe("validators", () => {
  it("checks codes, names and legal entity ids", () => {
    expect(validateCodeForm({ legalEntityId: "11111111-1111-4111-8111-111111111111", code: "CC-100", name: "Plant" })).toEqual({});
    expect(Object.keys(validateCodeForm({ legalEntityId: "x", code: "cc", name: " " })).sort()).toEqual(["code", "legalEntityId", "name"]);
  });

  it("requires a well-ordered account range", () => {
    expect(validateAccountRange("600000", "609999")).toBeNull();
    expect(validateAccountRange("609999", "600000")).toMatch(/reversed/);
    expect(validateAccountRange("", "1")).toMatch(/both/);
  });
});

describe("isPeriod", () => {
  it("requires YYYY-MM with a valid month", () => {
    expect(isPeriod("2026-09")).toBe(true);
    expect(isPeriod("2026-13")).toBe(false);
    expect(isPeriod("26-09")).toBe(false);
  });
});

describe("isDate", () => {
  it("accepts real calendar dates only", () => {
    expect(isDate("2026-09-01")).toBe(true);
    expect(isDate("2028-02-29")).toBe(true);
    expect(isDate("2026-02-30")).toBe(false);
    expect(isDate("2026-9-01")).toBe(false);
    expect(isDate("")).toBe(false);
  });
});

describe("validateBehaviorRule", () => {
  const ok = { legalEntityId: "11111111-1111-4111-8111-111111111111", costElementId: "22222222-2222-4222-8222-222222222222", objectKind: "" as const, behavior: "Fixed" as const, effectiveFrom: "2026-09-01" };
  it("passes a complete rule for every kind", () => {
    expect(validateBehaviorRule(ok)).toEqual({});
  });
  it("names each missing or malformed field", () => {
    expect(Object.keys(validateBehaviorRule({ ...ok, legalEntityId: "x", costElementId: "", effectiveFrom: "2026-13-01" })).sort()).toEqual([
      "costElementId",
      "effectiveFrom",
      "legalEntityId",
    ]);
  });
});

describe("costacct module manifest", () => {
  it("routes every nav entry behind a controlling duty", () => {
    expect(costacct.id).toBe("costacct");
    const paths = costacct.routes.map((r) => r.path);
    for (const n of costacct.nav) {
      expect(n.to.startsWith("/costacct")).toBe(true);
      expect(paths).toContain(n.to);
      expect(n.duty).toMatch(/^controlling\./);
    }
  });

  it("lists the behavior rules for maintainers to find", () => {
    const paths = costacct.routes.map((r) => r.path);
    expect(paths).toEqual(expect.arrayContaining(["/costacct/behavior-rules", "/costacct/behavior-rules/new"]));
    expect(costacct.nav.find((n) => n.to === "/costacct/behavior-rules")?.duty).toBe("controlling.view");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("costacct");
  });
});
