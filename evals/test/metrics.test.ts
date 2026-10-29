import { describe, expect, it } from "vitest";
import { accuracyTopK, auroc, macroF1, meanAbsoluteError, precisionRecall } from "../src/metrics.js";
import { gate } from "../src/gate.js";

describe("metrics", () => {
  it("accuracyTopK counts a hit when the truth is in the first k", () => {
    const rows = [
      { truth: "a", ranked: ["a", "b"] },
      { truth: "b", ranked: ["a", "b"] },
      { truth: "c", ranked: ["a", "b", "c"] },
      { truth: "d", ranked: [] },
    ];
    expect(accuracyTopK(rows, 1)).toBe(0.25);
    expect(accuracyTopK(rows, 3)).toBe(0.75);
    expect(() => accuracyTopK([], 1)).toThrow();
  });

  it("precisionRecall returns 0 for empty denominators, never NaN", () => {
    expect(precisionRecall(0, 0, 0)).toEqual({ precision: 0, recall: 0 });
    expect(precisionRecall(3, 1, 1)).toEqual({ precision: 0.75, recall: 0.75 });
  });

  it("macroF1 is 1 for perfect predictions and penalises a missed class", () => {
    expect(macroF1([{ truth: "x", predicted: "x" }, { truth: "y", predicted: "y" }])).toBe(1);
    expect(macroF1([{ truth: "x", predicted: "x" }, { truth: "y", predicted: "x" }])).toBeLessThan(1);
  });

  it("auroc is 1 for perfect separation and 0.5 when all scores tie", () => {
    expect(auroc([{ score: 0.9, label: true }, { score: 0.1, label: false }])).toBe(1);
    expect(auroc([{ score: 0.5, label: true }, { score: 0.5, label: false }])).toBe(0.5);
    expect(() => auroc([{ score: 0.5, label: true }])).toThrow(/both classes/);
  });

  it("meanAbsoluteError averages absolute differences", () => {
    expect(meanAbsoluteError([{ predicted: 5, actual: 3 }, { predicted: 1, actual: 4 }])).toBe(2.5);
  });
});

describe("gate (FR-47.9)", () => {
  it("passes when every threshold is met", () => {
    expect(gate("ap_coding", { top1: 0.92, top3: 0.98 })).toEqual({ passed: true, failures: [] });
  });

  it("fails below a minimum and names the metric", () => {
    const v = gate("ap_coding", { top1: 0.919, top3: 0.99 });
    expect(v.passed).toBe(false);
    expect(v.failures.join(" ")).toContain("top1");
  });

  it("fails a maximum that is exceeded (anomaly false positives)", () => {
    expect(gate("anomaly", { auroc: 0.9, fpr: 0.06 }).passed).toBe(false);
    expect(gate("anomaly", { auroc: 0.9, fpr: 0.05 }).passed).toBe(true);
  });

  it("fails a missing metric", () => {
    expect(gate("refusal", {}).failures).toEqual(["refusal: missing"]);
  });

  it("fails a regression of more than one point against the last release", () => {
    expect(gate("ap_coding", { top1: 0.94, top3: 0.99 }, { top1: 0.955, top3: 0.99 }).passed).toBe(false);
    expect(gate("ap_coding", { top1: 0.94, top3: 0.99 }, { top1: 0.95, top3: 0.99 }).passed).toBe(true);
  });

  it("rejects an unknown agent", () => {
    expect(() => gate("nope", {})).toThrow(/no thresholds/);
  });
});
