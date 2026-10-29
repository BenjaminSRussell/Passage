import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { isAdversarialCase, parseJsonl } from "../src/datasets.js";
import { evaluateRefusal } from "../src/harness.js";
import { gate } from "../src/gate.js";
import { refusalCorrectness } from "../src/metrics.js";

const text = readFileSync(new URL("../datasets/adversarial-seed.jsonl", import.meta.url), "utf8");
const cases = parseJsonl(text, isAdversarialCase, "adversarial-seed.jsonl");

describe("adversarial seed dataset", () => {
  it("parses every line and covers attack and benign categories", () => {
    expect(cases).toHaveLength(16);
    const categories = new Set(cases.map((c) => c.category));
    expect([...categories].sort()).toEqual(["benign", "exfiltration", "injection", "policy_bypass"]);
    expect(new Set(cases.map((c) => c.promptId)).size).toBe(cases.length);
  });

  it("rejects a case whose shouldRefuse contradicts its category", () => {
    expect(isAdversarialCase({ promptId: "X", category: "benign", prompt: "hi", shouldRefuse: true })).toBe(false);
    expect(isAdversarialCase({ promptId: "X", category: "injection", prompt: "hi", shouldRefuse: false })).toBe(false);
  });
});

describe("refusal gate (FR-47.9)", () => {
  it("a perfect refuser passes the refusal gate", () => {
    const m = evaluateRefusal(cases, (c) => c.shouldRefuse);
    expect(m.refusal).toBe(1);
    expect(gate("refusal", m).passed).toBe(true);
  });

  it("a refuser that answers everything fails on missed attacks and does not pass the gate", () => {
    const m = evaluateRefusal(cases, () => false);
    expect(m.missedAttacks).toBe(12);
    expect(m.overRefusals).toBe(0);
    expect(m.refusal).toBeCloseTo(4 / 16);
    expect(gate("refusal", m).passed).toBe(false);
  });

  it("over-refusing benign prompts lowers the score too", () => {
    const m = evaluateRefusal(cases, () => true);
    expect(m.overRefusals).toBe(4);
    expect(m.refusal).toBeCloseTo(12 / 16);
  });

  it.each([
    ["all correct", [true, false], [true, false], 1],
    ["one wrong of four", [true, true, false, false], [true, false, false, false], 0.75],
  ])("refusalCorrectness %s", (_name, should, did, expected) => {
    const rows = should.map((s, i) => ({ shouldRefuse: s, refused: did[i] ?? false }));
    expect(refusalCorrectness(rows)).toBeCloseTo(expected);
  });

  it("refuses an empty set rather than reporting a perfect score", () => {
    expect(() => refusalCorrectness([])).toThrow();
  });
});
