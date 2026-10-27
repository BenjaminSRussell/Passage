import { describe, expect, it } from "vitest";
import { appendTiming, nearestRank, purgeTelemetry, rollup, validateTiming, type StepTiming } from "./telemetry";

const DAY = 86_400_000;
const NOW = 1_800_000_000_000;

function timing(over: Partial<StepTiming> = {}): StepTiming {
  return {
    flowCode: "PICK_CLUSTER",
    stepId: "scan-lp",
    userId: "worker-1",
    deviceId: "TC52-0042",
    durationMs: 1000,
    failed: false,
    at: NOW,
    ...over,
  };
}

describe("nearestRank", () => {
  const ten = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];
  it.each<[number[], number, number]>([
    [ten, 50, 5],
    [ten, 95, 10],
    [ten, 100, 10],
    [ten, 1, 1],
    [[42], 95, 42],
    [[], 50, 0],
  ])("rank %p at %d%% is %d", (values, pct, want) => {
    expect(nearestRank(values, pct)).toBe(want);
  });
});

describe("rollup", () => {
  const samples = [1000, 2000, 3000, 4000].map((d, i) => timing({ durationMs: d, failed: i === 0 }));

  it("reports count, errors, median and p95 in seconds per flow and step", () => {
    const [row] = rollup(samples);
    expect(row).toEqual({ flowCode: "PICK_CLUSTER", stepId: "scan-lp", count: 4, errors: 1, medianS: 2, p95S: 4 });
    expect(row.userId).toBeUndefined();
  });

  it("splits by user only when asked, since per-user timing is personal data", () => {
    const mixed = [...samples, timing({ userId: "worker-2", durationMs: 500 })];
    expect(rollup(mixed)).toHaveLength(1);
    const byUser = rollup(mixed, { byUser: true });
    expect(byUser.map((r) => r.userId)).toEqual(["worker-1", "worker-2"]);
    expect(byUser[1]).toMatchObject({ count: 1, errors: 0, medianS: 0.5, p95S: 0.5 });
  });

  it("keeps different steps apart", () => {
    const rows = rollup([timing(), timing({ stepId: "confirm" })]);
    expect(rows.map((r) => r.stepId)).toEqual(["confirm", "scan-lp"]);
  });
});

describe("validateTiming", () => {
  it.each<[string, Partial<StepTiming>, boolean]>([
    ["valid timing", {}, true],
    ["negative duration", { durationMs: -1 }, false],
    ["NaN duration", { durationMs: Number.NaN }, false],
    ["missing step", { stepId: "" }, false],
    ["infinite timestamp", { at: Number.POSITIVE_INFINITY }, false],
  ])("%s", (_n, over, ok) => {
    expect(validateTiming(timing(over)) === null).toBe(ok);
  });
});

describe("purgeTelemetry (90-day default retention)", () => {
  it("keeps records inside the window and removes older ones", () => {
    const recent = timing({ at: NOW - 10 * DAY });
    const old = timing({ at: NOW - 100 * DAY });
    const res = purgeTelemetry([recent, old], NOW);
    expect(res).toEqual({ kept: [recent], removed: 1 });
  });

  it("accepts 30 and 365 days and rejects values outside 30–365", () => {
    expect("error" in purgeTelemetry([], NOW, 30)).toBe(false);
    expect("error" in purgeTelemetry([], NOW, 365)).toBe(false);
    expect("error" in purgeTelemetry([], NOW, 29)).toBe(true);
    expect("error" in purgeTelemetry([], NOW, 366)).toBe(true);
    expect("error" in purgeTelemetry([], NOW, 90.5)).toBe(true);
  });
});

describe("appendTiming", () => {
  it("appends a valid timing and drops records past the retention window", () => {
    const old = timing({ at: NOW - 91 * DAY });
    const kept = timing({ at: NOW - DAY });
    const next = timing({ stepId: "confirm" });
    expect(appendTiming([old, kept], next, NOW)).toEqual({ log: [kept, next] });
  });

  it("refuses an invalid timing or retention and leaves the log alone", () => {
    expect(appendTiming([], timing({ durationMs: -5 }), NOW)).toEqual({ error: expect.stringMatching(/duration/) });
    expect(appendTiming([], timing(), NOW, 10)).toEqual({ error: expect.stringMatching(/30 to 365/) });
  });
});
