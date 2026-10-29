import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { mrpKeywordBaseline, offHoursAnomalyBaseline } from "../src/baselines.js";
import { isAnomalyCase, isMrpTriageCase, parseJsonl } from "../src/datasets.js";
import { gate } from "../src/gate.js";
import { ANOMALY_FLAG_THRESHOLD, evaluateAnomaly, evaluateMrpTriage } from "../src/harness.js";
import { auroc, falsePositiveRate, macroF1 } from "../src/metrics.js";

const dir = fileURLToPath(new URL("../datasets/", import.meta.url));
const load = (name: string) => readFileSync(`${dir}${name}`, "utf8");
const mrpCases = () => parseJsonl(load("mrp-triage-seed.jsonl"), isMrpTriageCase, "mrp-triage-seed.jsonl");
const anomalyCases = () => parseJsonl(load("anomaly-seed.jsonl"), isAnomalyCase, "anomaly-seed.jsonl");

describe("falsePositiveRate", () => {
  it("counts negatives scored strictly above the threshold", () => {
    const rows = [
      { score: 0.9, label: false },
      { score: 0.8, label: false },
      { score: 0.1, label: false },
      { score: 0.95, label: true },
    ];
    // 0.8 is not strictly above 0.8, so one of three negatives is flagged.
    expect(falsePositiveRate(rows, 0.8)).toBeCloseTo(1 / 3);
  });

  it("throws when there are no negatives", () => {
    expect(() => falsePositiveRate([{ score: 0.5, label: true }], 0.8)).toThrow("at least one negative");
  });
});

describe("MRP triage seed", () => {
  it("parses nine labeled messages and rejects an empty cause", () => {
    expect(mrpCases()).toHaveLength(9);
    expect(() => parseJsonl('{"messageId":"x","text":"t","cause":""}', isMrpTriageCase, "bad")).toThrow("bad:1");
  });

  it("keyword baseline misses the postpone case and stays below the macro-F1 gate", () => {
    const m = evaluateMrpTriage(mrpCases(), mrpKeywordBaseline);
    // Five causes right, SUPPLY_EARLY predicted UNKNOWN: 7 classes, 5 with F1 = 1.
    expect(m.macroF1).toBeCloseTo(5 / 7);
    expect(m.n).toBe(9);
    expect(gate("mrp_triage", m).failures).toEqual([`macroF1 ${(5 / 7).toFixed(4)} < 0.8`]);
  });

  it("a predictor that returns the labels clears the gate", () => {
    const cases = mrpCases();
    const truth = new Map(cases.map((c) => [c.messageId, c.cause]));
    const m = evaluateMrpTriage(cases, (c) => truth.get(c.messageId) ?? "UNKNOWN");
    expect(m.macroF1).toBe(1);
    expect(gate("mrp_triage", m).passed).toBe(true);
  });

  it("an always-UNKNOWN predictor fails the gate", () => {
    const m = evaluateMrpTriage(mrpCases(), () => "UNKNOWN");
    expect(m.macroF1).toBeLessThan(0.8);
    expect(gate("mrp_triage", m).passed).toBe(false);
    expect(macroF1([{ truth: "A", predicted: "A" }, { truth: "B", predicted: "B" }])).toBe(1);
  });
});

describe("anomaly seed", () => {
  it("parses twelve journals with both classes present", () => {
    const cases = anomalyCases();
    expect(cases).toHaveLength(12);
    expect(cases.some((c) => c.anomalous)).toBe(true);
    expect(cases.some((c) => !c.anomalous)).toBe(true);
  });

  it("rejects an hour outside 0-23", () => {
    const bad = '{"journalId":"J","amount":"1","accountPair":"a-b","postedAtHour":24,"userId":"U","anomalous":false}';
    expect(() => parseJsonl(bad, isAnomalyCase, "bad")).toThrow("bad:1");
  });

  it("off-hours baseline is fooled by a night batch and an in-hours anomaly", () => {
    const m = evaluateAnomaly(anomalyCases(), offHoursAnomalyBaseline);
    // 4 anomalies x 8 normal journals: three off-hours anomalies beat the 7 daytime
    // journals and tie the night batch (7.5 each); the 11:00 anomaly ties the 7
    // daytime journals and loses to the batch (3.5). (22.5 + 3.5) / 32.
    expect(m.auroc).toBeCloseTo(26 / 32);
    // The 01:00 batch posting is the one flagged normal journal of eight.
    expect(m.fpr).toBeCloseTo(1 / 8);
    expect(gate("anomaly", m).passed).toBe(false);
    expect(ANOMALY_FLAG_THRESHOLD).toBe(0.8);
  });

  it("a scorer that separates the classes clears both anomaly gates", () => {
    const m = evaluateAnomaly(anomalyCases(), (c) => (c.anomalous ? 0.95 : 0.2));
    expect(m.auroc).toBe(1);
    expect(m.fpr).toBe(0);
    expect(gate("anomaly", m).passed).toBe(true);
  });

  it("flags everything and fails the FPR gate", () => {
    const m = evaluateAnomaly(anomalyCases(), () => 0.99);
    expect(m.fpr).toBe(1);
    expect(m.auroc).toBeCloseTo(0.5);
    expect(gate("anomaly", m).passed).toBe(false);
    expect(auroc([{ score: 1, label: true }, { score: 0, label: false }])).toBe(1);
  });
});
