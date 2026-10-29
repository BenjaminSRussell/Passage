import { describe, expect, it } from "vitest";
import { reconcileRuns, runVariance, type RunUsageLine } from "../src/domain/reconcile.js";

const line = (over: Partial<RunUsageLine> & Pick<RunUsageLine, "billed" | "recorded">): RunUsageLine => ({
  runId: "run",
  estimated: false,
  ...over,
});

describe("runVariance", () => {
  it.each([
    { name: "identical", billed: { tokensIn: 1000, tokensOut: 100 }, recorded: { tokensIn: 1000, tokensOut: 100 }, want: 0 },
    { name: "zero both", billed: { tokensIn: 0, tokensOut: 0 }, recorded: { tokensIn: 0, tokensOut: 0 }, want: 0 },
    { name: "1% on input", billed: { tokensIn: 1000, tokensOut: 100 }, recorded: { tokensIn: 990, tokensOut: 100 }, want: 0.01 },
    { name: "larger of the two", billed: { tokensIn: 1000, tokensOut: 100 }, recorded: { tokensIn: 1000, tokensOut: 110 }, want: 10 / 110 },
  ])("$name", ({ billed, recorded, want }) => {
    expect(runVariance(billed, recorded)).toBeCloseTo(want, 12);
  });
});

describe("reconcileRuns (9.47.9 token reconciliation)", () => {
  it("passes runs within 1% and reports no variance", () => {
    const report = reconcileRuns([
      line({ runId: "a", billed: { tokensIn: 10_000, tokensOut: 1_000 }, recorded: { tokensIn: 10_050, tokensOut: 1_000 } }),
    ]);
    expect(report).toMatchObject({ checked: 1, withinTolerance: 1, variance: 0, unestimatedVarianceRate: 0 });
  });

  it("flags unestimated runs outside tolerance as variance (alert condition)", () => {
    const report = reconcileRuns([
      line({ runId: "a", billed: { tokensIn: 10_000, tokensOut: 1_000 }, recorded: { tokensIn: 9_000, tokensOut: 1_000 } }),
      line({ runId: "b", billed: { tokensIn: 100, tokensOut: 10 }, recorded: { tokensIn: 100, tokensOut: 10 } }),
    ]);
    expect(report.variance).toBe(1);
    expect(report.lines[0]).toMatchObject({ runId: "a", kind: "variance" });
    expect(report.unestimatedVarianceRate).toBeCloseTo(0.5, 12);
  });

  it("treats estimated runs outside tolerance as flagged, not as variance (BR-47.4)", () => {
    const report = reconcileRuns([
      line({ runId: "e", estimated: true, billed: { tokensIn: 10_000, tokensOut: 1_000 }, recorded: { tokensIn: 7_000, tokensOut: 1_000 } }),
    ]);
    expect(report).toMatchObject({ estimatedVariance: 1, variance: 0, unestimatedVarianceRate: 0 });
    expect(report.lines[0]?.kind).toBe("estimated_variance");
  });

  it("is empty-safe", () => {
    expect(reconcileRuns([])).toMatchObject({ checked: 0, unestimatedVarianceRate: 0 });
  });

  it("property: variance is symmetric, bounded in [0,1] and zero only when equal", () => {
    const counts = [0, 1, 7, 100, 9_999, 1_000_000];
    for (const a of counts) {
      for (const b of counts) {
        const v1 = runVariance({ tokensIn: a, tokensOut: 0 }, { tokensIn: b, tokensOut: 0 });
        const v2 = runVariance({ tokensIn: b, tokensOut: 0 }, { tokensIn: a, tokensOut: 0 });
        expect(v1).toBe(v2);
        expect(v1).toBeGreaterThanOrEqual(0);
        expect(v1).toBeLessThanOrEqual(1);
        expect(v1 === 0).toBe(a === b);
      }
    }
  });
});
