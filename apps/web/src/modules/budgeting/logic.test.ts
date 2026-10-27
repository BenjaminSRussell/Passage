import { describe, expect, it } from "vitest";
import budgeting from "./module";
import { loadModules } from "../registry";
import {
  blockedLineIds,
  formatAmount,
  fromUnits,
  isMoney,
  overPercentLabel,
  reservesFunds,
  toUnits,
  validateCheckRows,
  validateEntryLines,
  validateRule,
} from "./logic";

describe("isMoney", () => {
  it.each([
    ["10400.00", true],
    ["-5", true],
    ["0.0001", true],
    ["1.00001", false],
    ["12,000.00", false],
    ["", false],
  ])("isMoney(%s) = %s", (value, want) => {
    expect(isMoney(value)).toBe(want);
  });
});

describe("reservation rule (FR-38.4 step 8)", () => {
  it("pass and warn reserve, fail does not", () => {
    expect(reservesFunds("pass")).toBe(true);
    expect(reservesFunds("warn")).toBe(true);
    expect(reservesFunds("fail")).toBe(false);
  });
});

describe("blockedLineIds", () => {
  it("lists only failed lines", () => {
    const outcome = {
      result: "fail" as const,
      lines: [
        { lineId: "a", result: "pass" as const },
        { lineId: "b", result: "fail" as const },
        { lineId: "c", result: "warn" as const },
      ],
    };
    expect(blockedLineIds(outcome)).toEqual(["b"]);
  });
});

describe("overPercentLabel", () => {
  it("formats decimal percentages and rejects non-money input", () => {
    expect(overPercentLabel("20")).toBe("20.00%");
    expect(overPercentLabel("4.5")).toBe("4.50%");
    expect(overPercentLabel("abc")).toBe("n/a");
    expect(overPercentLabel("33.333333333333")).toBe("33.33%");
  });
});

describe("validateCheckRows", () => {
  const row = { lineId: "l1", dimensionKey: "DEPT-10", spanKey: "2026-09", amount: "10400.00" };

  it("accepts a complete row", () => {
    expect(validateCheckRows([row]).ok).toBe(true);
  });

  it("reports every missing field with its line number", () => {
    const result = validateCheckRows([{ ...row, dimensionKey: " ", amount: "1.000001" }]);
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.errors).toEqual([
        "Line 1: dimension is required.",
        "Line 1: amount must be a positive decimal with at most 4 places.",
      ]);
    }
  });

  it("rejects an empty list and a negative amount", () => {
    expect(validateCheckRows([]).ok).toBe(false);
    expect(validateCheckRows([{ ...row, amount: "-1" }]).ok).toBe(false);
  });
});

describe("decimal arithmetic without floats", () => {
  it("round-trips decimal strings through integer units", () => {
    expect(fromUnits(toUnits("10400.005"))).toBe("10400.01");
    expect(fromUnits(toUnits("-0.004"))).toBe("0.00");
    expect(fromUnits(toUnits("0.1") + toUnits("0.2"))).toBe("0.30");
    expect(formatAmount("100000")).toBe("100000.00");
    expect(formatAmount(undefined)).toBe("—");
  });
});

describe("validateRule (FR-38.3)", () => {
  const ok = { legalEntityId: "11111111-1111-4111-8111-111111111111", code: "DEPT", warnPct: "0", blockPct: "5", currency: "USD", effectiveFrom: "2026-01-01" };

  it("accepts the AC-38.1 thresholds", () => {
    expect(validateRule(ok)).toEqual({});
  });

  it("rejects a block threshold below warn and out-of-range values", () => {
    expect(validateRule({ ...ok, warnPct: "10", blockPct: "5" }).blockPct).toMatch(/below the warn/);
    expect(validateRule({ ...ok, warnPct: "101" }).warnPct).toBeDefined();
    expect(validateRule({ ...ok, code: "dept", currency: "usd", legalEntityId: "x" })).toMatchObject({
      code: expect.any(String),
      currency: expect.any(String),
      legalEntityId: expect.any(String),
    });
  });
});

describe("validateEntryLines (CO-0604)", () => {
  it("requires a transfer to net to zero", () => {
    const lines = [
      { dimensionKey: "dept-10", spanKey: "FY2026", amount: "-5000" },
      { dimensionKey: "dept-20", spanKey: "FY2026", amount: "4000" },
    ];
    expect(validateEntryLines("Transfer", lines)).toEqual(["A transfer must net to zero; these lines net to -1000.0000."]);
    expect(validateEntryLines("Original", lines)).toEqual([]);
    expect(validateEntryLines("Transfer", [lines[0], { ...lines[1], amount: "5000" }])).toEqual([]);
  });
});

describe("budgeting module manifest", () => {
  it("exports the module id, routes and duty-gated nav", () => {
    expect(budgeting.id).toBe("budgeting");
    const paths = budgeting.routes.map((r) => r.path);
    expect(paths).toContain("/budgeting");
    expect(paths).toContain("/budgeting/rules/:id");
    for (const n of budgeting.nav) {
      expect(n.to.startsWith("/budgeting")).toBe(true);
      expect(paths).toContain(n.to);
      expect(n.duty).toMatch(/^budget\./);
    }
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("budgeting");
  });
});
