import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { AP_FILE, CASH_FILE, generateApCoding, generateCashApp, RECORDING_FILE, simulatedModel, toJsonl } from "../src/generate.js";
import { datasetVersion, isApCodingCase, isCashAppCase, parseJsonl } from "../src/datasets.js";
import { gate } from "../src/gate.js";
import { evaluateApCoding, evaluateCashApp } from "../src/harness.js";
import { apPrompt, DEFAULT_MODELS, parseRecording, promptHash, record, ReplayError, ReplayPredictor, type ModelProvider } from "../src/replay.js";

const dir = new URL("../datasets/", import.meta.url);
const read = (f: string): string => readFileSync(new URL(f, dir), "utf8");
const ap = parseJsonl(read(AP_FILE), isApCodingCase, AP_FILE);
const cash = parseJsonl(read(CASH_FILE), isCashAppCase, CASH_FILE);

describe("generated datasets", () => {
  it("match the committed files byte for byte (seeded, reproducible)", () => {
    expect(toJsonl(generateApCoding())).toBe(read(AP_FILE));
    expect(toJsonl(generateCashApp())).toBe(read(CASH_FILE));
    expect(datasetVersion(read(AP_FILE))).toMatch(/^[0-9a-f]{16}$/);
  });

  it("have the FR-47.9 shapes: noise, multi-invoice and partial payments", () => {
    expect(ap.length).toBe(400);
    expect(new Set(ap.map((c) => c.vendorId)).size).toBeGreaterThanOrEqual(35);
    const offHabit = ap.filter((c) => c.history.length > 0 && !c.history.includes(c.account));
    expect(offHabit.length).toBeGreaterThan(0);
    expect(cash.length).toBe(300);
    expect(cash.filter((c) => c.truth.length > 1).length).toBeGreaterThanOrEqual(5);
    expect(cash.filter((c) => /short paid/.test(c.remittance)).length).toBeGreaterThanOrEqual(10);
    for (const c of cash) expect(c.truth.every((t) => c.openInvoices.some((o) => o.invoiceId === t))).toBe(true);
  });
});

describe("replay predictor", () => {
  const recording = parseRecording(read(RECORDING_FILE), RECORDING_FILE);
  const replay = new ReplayPredictor(recording);

  it("gates the replayed predictions with no network", () => {
    const apResult = evaluateApCoding(ap, replay.ap);
    const cashResult = evaluateCashApp(cash, replay.cash);
    expect(gate("ap_coding", apResult).passed).toBe(true);
    expect(gate("cash_app", cashResult).passed).toBe(true);
    expect(replay.models()).toEqual([`${DEFAULT_MODELS.sonnet}-simulated`]);
  });

  it("fails the gate for a recording that is wrong", () => {
    const bad = new ReplayPredictor(recording.map((r) => (r.kind === "ap_coding" ? { ...r, output: JSON.stringify({ accounts: ["0000"] }) } : r)));
    expect(gate("ap_coding", evaluateApCoding(ap, bad.ap)).passed).toBe(false);
  });

  it("reports a missing recording", () => {
    const partial = new ReplayPredictor(recording.filter((r) => r.caseId !== ap[0]?.invoiceId));
    expect(() => partial.ap(ap[0]!)).toThrowError(ReplayError);
    expect(() => partial.ap(ap[0]!)).toThrowError(/no recording for ap_coding case INV-10001/);
  });

  it("reports a stale recording when the case or prompt changed", () => {
    const changed = { ...ap[0]!, text: `${ap[0]!.text} (edited)` };
    try {
      replay.ap(changed);
      expect.unreachable();
    } catch (e) {
      expect((e as ReplayError).code).toBe("STALE");
      expect((e as Error).message).toMatch(/stale/);
    }
  });

  it("rejects unparseable recorded output and bad files", () => {
    const c = ap[0]!;
    const r = new ReplayPredictor([{ kind: "ap_coding", caseId: c.invoiceId, promptHash: promptHash(apPrompt(c)), model: "m", output: "not json" }]);
    expect(() => r.ap(c)).toThrowError(/not JSON/);
    expect(() => parseRecording("{bad", "f")).toThrowError(/not valid JSON/);
    expect(() => parseRecording('{"kind":"x"}', "f")).toThrowError(/not a recorded output/);
  });
});

describe("recorder", () => {
  it("records through a provider and replays what it recorded", async () => {
    const models: string[] = [];
    const fake: ModelProvider = {
      async complete(req) {
        models.push(req.model);
        return { text: JSON.stringify({ accounts: ["6100"] }) };
      },
    };
    const some = ap.slice(0, 5);
    const rec = await record(fake, DEFAULT_MODELS.haiku, { ap: some });
    expect(models).toEqual(Array(5).fill("claude-haiku-5-5"));
    const replay = new ReplayPredictor(rec);
    for (const c of some) expect(replay.ap(c)).toEqual(["6100"]);
  });

  it("the simulated fixture model is deterministic", async () => {
    const a = await record(simulatedModel(ap, cash), "m", { ap: ap.slice(0, 20) });
    const b = await record(simulatedModel(ap, cash), "m", { ap: ap.slice(0, 20) });
    expect(a).toEqual(b);
  });
});
