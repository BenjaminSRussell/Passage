import { describe, expect, it } from "vitest";
import { canonicalQuery, evaluateAskData, sqlEquivalent, type AskDataCase, type CubeQuery } from "../src/askdata.js";

const revenue: CubeQuery = {
  measures: ["revenue"],
  dimensions: ["legal_entity"],
  timeDimension: { dimension: "invoice_date", granularity: "month", dateRange: ["2026-09-01", "2026-09-30"] },
  filters: [{ member: "legal_entity", operator: "equals", values: ["USMF"] }],
};

describe("canonicalQuery / sqlEquivalent (FR-47.9 SQL equivalence)", () => {
  it.each([
    {
      name: "member order and case do not matter",
      other: {
        measures: ["REVENUE"],
        dimensions: ["Legal_Entity"],
        timeDimension: { dimension: "Invoice_Date", granularity: "Month", dateRange: ["2026-09-01", "2026-09-30"] },
        filters: [{ member: "Legal_Entity", operator: "Equals", values: ["USMF"] }],
      },
      want: true,
    },
    { name: "a different date range is not equivalent", other: { ...revenue, timeDimension: { ...revenue.timeDimension!, dateRange: ["2026-08-01", "2026-08-31"] } }, want: false },
    { name: "a different measure is not equivalent", other: { ...revenue, measures: ["cost"] }, want: false },
    { name: "a different filter value is not equivalent", other: { ...revenue, filters: [{ member: "legal_entity", operator: "equals", values: ["DEMF"] }] }, want: false },
  ])("$name", ({ other, want }) => {
    expect(sqlEquivalent(revenue, other as CubeQuery)).toBe(want);
  });

  it("sorts filter values and filters deterministically", () => {
    const a: CubeQuery = { measures: ["m"], filters: [{ member: "x", operator: "in", values: ["b", "a"] }] };
    const b: CubeQuery = { measures: ["m"], filters: [{ member: "x", operator: "in", values: ["a", "b"] }] };
    expect(canonicalQuery(a)).toBe(canonicalQuery(b));
  });

  it("keeps the order of positional values: a reversed date range is a different query", () => {
    const a: CubeQuery = { measures: ["m"], filters: [{ member: "d", operator: "inDateRange", values: ["2026-09-01", "2026-09-30"] }] };
    const b: CubeQuery = { measures: ["m"], filters: [{ member: "d", operator: "inDateRange", values: ["2026-09-30", "2026-09-01"] }] };
    expect(sqlEquivalent(a, b)).toBe(false);
  });

  it("compares relative date ranges without regard to case", () => {
    const a: CubeQuery = { measures: ["m"], timeDimension: { dimension: "d", dateRange: "Last month" } };
    const b: CubeQuery = { measures: ["m"], timeDimension: { dimension: "d", dateRange: "last month" } };
    expect(sqlEquivalent(a, b)).toBe(true);
  });

  it("empty clauses are dropped, so they compare equal to absent ones", () => {
    expect(sqlEquivalent({ measures: ["m"], dimensions: [], filters: [] }, { measures: ["m"] })).toBe(true);
  });

  it("treats duplicate measures as one", () => {
    expect(sqlEquivalent({ measures: ["m", "M"] }, { measures: ["m"] })).toBe(true);
  });
});

describe("evaluateAskData", () => {
  const cases: AskDataCase[] = [
    { id: "1", question: "revenue for USMF in September", expectedMetric: "revenue", expectedQuery: revenue },
    { id: "2", question: "cost last month", expectedMetric: "cost", expectedQuery: { measures: ["cost"] } },
    { id: "3", question: "headcount", expectedMetric: "headcount", expectedQuery: { measures: ["headcount"] } },
  ];

  it("scores metric selection and SQL equivalence separately", () => {
    const result = evaluateAskData(cases, (c) => {
      if (c.id === "1") return { metric: "revenue", query: revenue };
      if (c.id === "2") return { metric: "cost", query: { measures: ["revenue"] } };
      return { metric: "headcount", query: { measures: ["headcount"] } };
    });
    expect(result.selection).toBeCloseTo(1, 12);
    expect(result.sqlEquivalence).toBeCloseTo(2 / 3, 12);
    expect(result.n).toBe(3);
  });

  it("is zero-safe on an empty set", () => {
    expect(evaluateAskData([], () => ({ metric: "x", query: { measures: [] } }))).toMatchObject({ sqlEquivalence: 0, n: 0 });
  });
});
