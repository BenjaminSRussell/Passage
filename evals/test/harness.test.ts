import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { remittanceBaseline, sameAmount, vendorHistoryBaseline } from "../src/baselines.js";
import { datasetVersion, isApCodingCase, isCashAppCase, parseJsonl } from "../src/datasets.js";
import { gate } from "../src/gate.js";
import { evaluateApCoding, evaluateCashApp } from "../src/harness.js";

const dir = fileURLToPath(new URL("../datasets/", import.meta.url));
const load = (name: string) => readFileSync(`${dir}${name}`, "utf8");
const apCases = () => parseJsonl(load("ap-coding-seed.jsonl"), isApCodingCase, "ap-coding-seed.jsonl");
const cashCases = () => parseJsonl(load("cash-app-seed.jsonl"), isCashAppCase, "cash-app-seed.jsonl");

describe("golden seed datasets", () => {
  it("parse against their schemas", () => {
    expect(apCases()).toHaveLength(8);
    expect(cashCases()).toHaveLength(4);
  });

  it("are versioned by content, so any edit changes the version (BR-47.8)", () => {
    const text = load("ap-coding-seed.jsonl");
    expect(datasetVersion(text)).toMatch(/^[0-9a-f]{16}$/);
    expect(datasetVersion(text)).toBe(datasetVersion(text));
    expect(datasetVersion(`${text} `)).not.toBe(datasetVersion(text));
  });

  it("reject a line that breaks the schema, with its line number", () => {
    expect(() => parseJsonl('{"invoiceId":"x"}', isApCodingCase, "bad.jsonl")).toThrow("bad.jsonl:1");
    expect(() => parseJsonl("not json", isApCodingCase, "bad.jsonl")).toThrow("not valid JSON");
  });
});

describe("baselines through the harness", () => {
  it("vendor-history baseline scores top-1 0.625 on the AP seed", () => {
    const m = evaluateApCoding(apCases(), vendorHistoryBaseline);
    expect(m.top1).toBeCloseTo(5 / 8);
    expect(m.n).toBe(8);
    expect(gate("ap_coding", m).passed).toBe(false);
  });

  it("remittance baseline scores precision and recall 1.0 on the cash-app seed", () => {
    const m = evaluateCashApp(cashCases(), remittanceBaseline);
    expect(m.precision).toBe(1);
    expect(m.recall).toBe(1);
    expect(gate("cash_app", m).passed).toBe(true);
  });

  it("matches amounts by decimal value, not by string", () => {
    expect(sameAmount("75", "75.00")).toBe(true);
    expect(sameAmount("075.10", "75.1")).toBe(true);
    expect(sameAmount("75.01", "75.00")).toBe(false);
    const c = { paymentId: "p", amount: "480", remittance: "wire", openInvoices: [{ invoiceId: "a", amount: "480.00" }, { invoiceId: "b", amount: "120.00" }], truth: ["a"] };
    expect(remittanceBaseline(c)).toEqual(["a"]);
  });

  it("a cash predictor that over-proposes loses precision and fails the gate", () => {
    const m = evaluateCashApp(cashCases(), (c) => c.openInvoices.map((i) => i.invoiceId));
    expect(m.precision).toBeLessThan(0.9);
    expect(gate("cash_app", m).passed).toBe(false);
  });

  it("the cash-app baseline proposes nothing when two invoices match the amount", () => {
    const c = { paymentId: "p", amount: "75.00", remittance: "x", openInvoices: [{ invoiceId: "a", amount: "75.00" }, { invoiceId: "b", amount: "75.00" }], truth: [] };
    expect(remittanceBaseline(c)).toEqual([]);
  });
});

describe("predictor contract", () => {
  it("a perfect predictor passes the AP gate and a wrong one fails it", () => {
    const cases = apCases();
    const perfect = evaluateApCoding(cases, (c) => [c.account]);
    expect(gate("ap_coding", perfect).passed).toBe(true);
    const wrong = evaluateApCoding(cases, () => ["0000"]);
    expect(wrong.top1).toBe(0);
    expect(gate("ap_coding", wrong)).toMatchObject({ passed: false });
    expect(gate("ap_coding", wrong).failures.join(" ")).toContain("top1");
  });
});
