import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { evaluateAskData, sqlEquivalent } from "../src/askdata.js";
import { askDataBaseline } from "../src/askdata-baseline.js";
import { gate } from "../src/gate.js";
import { isAskDataCase, parseJsonl } from "../src/datasets.js";

const seed = parseJsonl(
  readFileSync(new URL("../datasets/ask-data-seed.jsonl", import.meta.url), "utf8"),
  isAskDataCase,
  "ask-data-seed.jsonl",
);

describe("ask-the-data baseline on the seed set (FR-47.9)", () => {
  it("selects the expected metric and produces an equivalent query for every seed case", () => {
    const metrics = evaluateAskData(seed, askDataBaseline);
    expect(metrics).toMatchObject({ selection: 1, sqlEquivalence: 1, n: 8 });
    expect(gate("ask_data", metrics).passed).toBe(true);
  });

  it("uses the real month end, so a leap February ends on the 29th", () => {
    const q = askDataBaseline({
      id: "x",
      question: "revenue in February 2028",
      expectedMetric: "revenue",
      expectedQuery: { measures: ["revenue"] },
    }).query;
    expect(q.timeDimension?.dateRange).toEqual(["2028-02-01", "2028-02-29"]);
  });

  it("returns an empty metric for a question it cannot read, which scores as a miss", () => {
    const p = askDataBaseline({
      id: "x",
      question: "tell me a joke",
      expectedMetric: "revenue",
      expectedQuery: { measures: ["revenue"] },
    });
    expect(p.metric).toBe("");
    const m = evaluateAskData(
      [{ id: "x", question: "tell me a joke", expectedMetric: "revenue", expectedQuery: { measures: ["revenue"] } }],
      askDataBaseline,
    );
    expect(m.selection).toBe(0);
  });

  it("treats member order and case as equivalent but a different date range as different", () => {
    const a = { measures: ["Revenue", "cost"], filters: [{ member: "LEGAL_ENTITY.code", operator: "equals", values: ["USMF"] }] };
    const b = { measures: ["cost", "revenue"], filters: [{ member: "legal_entity.code", operator: "Equals", values: ["USMF"] }] };
    expect(sqlEquivalent(a, b)).toBe(true);
    const c = { measures: ["revenue"], timeDimension: { dimension: "ledger.posting_date", dateRange: ["2026-01-01", "2026-01-31"] as [string, string] } };
    const d = { measures: ["revenue"], timeDimension: { dimension: "ledger.posting_date", dateRange: ["2026-01-01", "2026-01-30"] as [string, string] } };
    expect(sqlEquivalent(c, d)).toBe(false);
  });

  it.each([
    ["no expected measures", `{"id":"b","question":"q","expectedMetric":"m","expectedQuery":{"measures":[]}}`],
    ["a metric the query does not measure", `{"id":"b","question":"q","expectedMetric":"cost","expectedQuery":{"measures":["revenue"]}}`],
    ["a filter without values", `{"id":"b","question":"q","expectedMetric":"m","expectedQuery":{"measures":["m"],"filters":[{"member":"x","operator":"equals"}]}}`],
    ["a one-sided date range", `{"id":"b","question":"q","expectedMetric":"m","expectedQuery":{"measures":["m"],"timeDimension":{"dimension":"d","dateRange":["2026-01-01"]}}}`],
    ["dimensions that are not strings", `{"id":"b","question":"q","expectedMetric":"m","expectedQuery":{"measures":["m"],"dimensions":[1]}}`],
    ["a fractional limit", `{"id":"b","question":"q","expectedMetric":"m","expectedQuery":{"measures":["m"],"limit":2.5}}`],
  ])("rejects a seed line with %s", (_name, line) => {
    expect(() => parseJsonl(line, isAskDataCase, "bad.jsonl")).toThrow(/does not match/);
  });

  it("fails the gate when the baseline misses a case, with both metrics named", () => {
    const missed = [...seed, { id: "x", question: "tell me a joke", expectedMetric: "revenue", expectedQuery: { measures: ["revenue"] } }];
    const verdict = gate("ask_data", evaluateAskData(missed, askDataBaseline));
    expect(verdict.passed).toBe(false);
    expect(verdict.failures.join(" ")).toMatch(/selection .* < 0.95/);
    expect(verdict.failures.join(" ")).toMatch(/sqlEquivalence .* < 0.9/);
  });
});
