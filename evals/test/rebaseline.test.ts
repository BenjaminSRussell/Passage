import { describe, expect, it } from "vitest";
import { canPromote, datasetChanged, rebaselineQueue, type EvalRecord } from "../src/rebaseline.js";

const DS = { datasetId: "eval-ap-coding", datasetVersion: 3 };
const V1 = { agentId: "ap_coding", agentVersion: 1 };
const V2 = { agentId: "ap_coding", agentVersion: 2 };

const rec = (v: { agentId: string; agentVersion: number }, datasetVersion: number, passed = true): EvalRecord => ({
  ...v,
  datasetId: DS.datasetId,
  datasetVersion,
  passed,
});

describe("datasetChanged (BR-47.8)", () => {
  it("is true only when the version moved for the same dataset", () => {
    expect(datasetChanged({ datasetId: "d", datasetVersion: 1 }, { datasetId: "d", datasetVersion: 2 })).toBe(true);
    expect(datasetChanged({ datasetId: "d", datasetVersion: 2 }, { datasetId: "d", datasetVersion: 2 })).toBe(false);
  });

  it("refuses to compare different datasets", () => {
    expect(() => datasetChanged({ datasetId: "a", datasetVersion: 1 }, { datasetId: "b", datasetVersion: 1 })).toThrow();
  });
});

describe("rebaselineQueue (BR-47.8)", () => {
  it.each([
    { name: "all versions evaluated on the current dataset", records: [rec(V1, 3), rec(V2, 3)], want: [] },
    { name: "a dataset bump queues every version", records: [rec(V1, 2), rec(V2, 2)], want: [V1, V2] },
    { name: "a failed re-baseline still queues", records: [rec(V1, 3, false), rec(V2, 3)], want: [V1] },
    { name: "no records at all", records: [], want: [V1, V2] },
  ])("$name", ({ records, want }) => {
    expect(rebaselineQueue([V1, V2], DS, records)).toEqual(want);
  });
});

describe("canPromote (BR-47.8, AGT-0204)", () => {
  it("allows promotion with a passing eval on the current dataset version", () => {
    expect(canPromote(V2, DS, [rec(V2, 3)])).toEqual({ allowed: true, reasons: [] });
  });

  it("blocks promotion on a stale dataset version and names the code", () => {
    const check = canPromote(V2, DS, [rec(V2, 2)]);
    expect(check.allowed).toBe(false);
    expect(check.reasons[0]).toContain("AGT-0204");
  });

  it("blocks promotion when the only eval failed", () => {
    expect(canPromote(V2, DS, [rec(V2, 3, false)]).allowed).toBe(false);
  });
});
