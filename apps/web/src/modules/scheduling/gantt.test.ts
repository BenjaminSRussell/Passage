import { describe, expect, it } from "vitest";
import { barPosition, draggedStart, groupByResource, jobsOf, localOverlaps, ORDER_TONES, orderTone, snapMinutes, type GanttBar } from "./gantt";
import { describeShifts, formatMinutes, fromUtcInput, parseClock, toUtcInput } from "./format";

const base: GanttBar = {
  jobId: "job-a",
  orderId: "order-1",
  operNumber: 10,
  resourceId: "r1",
  segment: 1,
  start: "2026-10-26T06:00:00Z",
  end: "2026-10-26T09:00:00Z",
  lateMinutes: 0,
  firmed: false,
};

describe("barPosition", () => {
  const from = "2026-10-26T00:00:00Z";
  const to = "2026-10-27T00:00:00Z";

  it("places a bar as fractions of the window", () => {
    const pos = barPosition(base, from, to);
    expect(pos?.x).toBeCloseTo(0.25, 6);
    expect(pos?.width).toBeCloseTo(0.125, 6);
  });

  it("clips a bar that starts before the window", () => {
    const pos = barPosition({ start: "2026-10-25T22:00:00Z", end: "2026-10-26T02:00:00Z" }, from, to);
    expect(pos?.x).toBe(0);
    expect(pos?.width).toBeCloseTo(2 / 24, 6);
  });

  it("returns null outside the window or for an empty window", () => {
    expect(barPosition({ start: "2026-10-28T01:00:00Z", end: "2026-10-28T02:00:00Z" }, from, to)).toBeNull();
    expect(barPosition(base, to, from)).toBeNull();
  });
});

describe("snapping and drag", () => {
  it.each([
    [0, 0],
    [2, 0],
    [3, 5],
    [-3, -5],
    [62, 60],
  ])("snaps %d minutes to %d", (input, out) => {
    expect(snapMinutes(input)).toBe(out);
  });

  it("converts a drag distance to a snapped start", () => {
    // 1000 px for a 2-day window: 1 px ≈ 2.88 min; 100 px ≈ 288 min → 290.
    expect(draggedStart(base, 100, 1000, 2880)).toBe("2026-10-26T10:50:00Z");
    expect(draggedStart(base, -50, 1000, 2880)).toBe("2026-10-26T03:35:00Z");
  });

  it("does not move when the track has no width (not laid out yet)", () => {
    expect(draggedStart(base, 100, 0, 2880)).toBe("2026-10-26T06:00:00Z");
  });
});

describe("grouping and jobs", () => {
  const segs: GanttBar[] = [
    { ...base, segment: 1, start: "2026-10-26T14:00:00Z", end: "2026-10-26T16:00:00Z" },
    { ...base, segment: 2, start: "2026-10-27T08:00:00Z", end: "2026-10-27T11:00:00Z" },
    { ...base, jobId: "job-b", resourceId: "r2", start: "2026-10-26T08:00:00Z", end: "2026-10-26T09:00:00Z" },
    { ...base, jobId: "job-c", resourceId: "gone" },
  ];

  it("groups bars by resource in resource order and drops unknown resources", () => {
    const rows = groupByResource(segs, ["r2", "r1"]);
    expect([...rows.keys()]).toEqual(["r2", "r1"]);
    expect(rows.get("r1")).toHaveLength(2);
    expect(rows.has("gone")).toBe(false);
  });

  it("merges a split job into one row spanning its segments", () => {
    const jobs = jobsOf(segs);
    expect(jobs.map((j) => j.jobId)).toEqual(["job-c", "job-b", "job-a"]);
    const a = jobs.find((j) => j.jobId === "job-a");
    expect([a?.start, a?.end]).toEqual(["2026-10-26T14:00:00Z", "2026-10-27T11:00:00Z"]);
  });

  it("finds local overlaps on the same resource only", () => {
    const hits = localOverlaps(segs, { jobId: "job-x", resourceId: "r1", start: "2026-10-26T15:00:00Z", end: "2026-10-26T15:30:00Z" });
    expect(hits.map((b) => b.segment)).toEqual([1]);
    expect(localOverlaps(segs, { jobId: "job-a", resourceId: "r1", start: "2026-10-26T15:00:00Z", end: "2026-10-26T15:30:00Z" })).toEqual([]);
  });

  it("gives an order the same colour slot every time", () => {
    expect(orderTone("order-1")).toBe(orderTone("order-1"));
    expect(orderTone("order-1")).toBeGreaterThanOrEqual(0);
    expect(orderTone("order-1")).toBeLessThan(ORDER_TONES);
  });
});

describe("format", () => {
  it("round-trips datetime-local values as UTC", () => {
    expect(fromUtcInput("2026-10-26T08:05")).toBe("2026-10-26T08:05:00Z");
    expect(toUtcInput("2026-10-26T08:05:00Z")).toBe("2026-10-26T08:05");
    expect(fromUtcInput("")).toBeNull();
  });

  it("parses shift clocks including end of day", () => {
    expect(parseClock("08:30")).toBe(510);
    expect(parseClock("24:00")).toBe(1440);
    expect(parseClock("24:30")).toBeNull();
    expect(parseClock("8:30")).toBeNull();
  });

  it("summarises a weekly calendar Monday first", () => {
    expect(describeShifts([{ weekday: 0, startMinute: 0, endMinute: 1440 }, { weekday: 1, startMinute: 480, endMinute: 960 }])).toBe(
      "Mon 08:00–16:00 · Sun 00:00–24:00",
    );
    expect(formatMinutes(150)).toBe("2 h 30 min");
    expect(formatMinutes(45)).toBe("45 min");
  });
});
