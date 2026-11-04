import type { GanttBar } from "./api/client";

export type { GanttBar };

/** Drag snaps to a 5-minute grid (PRD 4.23.6 interaction rules, default user parameter). */
export const SNAP_MINUTES = 5;

/** Order colours cycle through this many token-based classes in GanttView.module.css. */
export const ORDER_TONES = 8;

const MINUTE_MS = 60_000;

/** Start and width of a bar inside the window [from, to), as fractions 0..1; null when outside. */
export function barPosition(bar: Pick<GanttBar, "start" | "end">, from: string, to: string): { x: number; width: number } | null {
  const windowStart = Date.parse(from);
  const windowEnd = Date.parse(to);
  const start = Date.parse(bar.start);
  const end = Date.parse(bar.end);
  if (!(windowEnd > windowStart) || !(end > start) || end <= windowStart || start >= windowEnd) {
    return null;
  }
  const span = windowEnd - windowStart;
  const visibleStart = Math.max(start, windowStart);
  const visibleEnd = Math.min(end, windowEnd);
  return { x: (visibleStart - windowStart) / span, width: (visibleEnd - visibleStart) / span };
}

/**
 * The changeover part of a bar (FR-23.15): the first changeoverMinutes of the
 * job's first segment, cut at the segment end. Null when there is none.
 */
export function changeoverSpan(bar: Pick<GanttBar, "start" | "end" | "segment" | "changeoverMinutes">): { start: string; end: string } | null {
  if (bar.segment !== 1 || !(bar.changeoverMinutes > 0)) return null;
  const start = Date.parse(bar.start);
  const end = Math.min(start + bar.changeoverMinutes * MINUTE_MS, Date.parse(bar.end));
  return { start: bar.start, end: new Date(end).toISOString().replace(".000Z", "Z") };
}

/** Round a minute offset to the nearest grid step. */
export function snapMinutes(minutes: number, snap: number = SNAP_MINUTES): number {
  return Math.round(minutes / snap) * snap || 0;
}

/**
 * New start (ISO UTC) for a bar dragged horizontally by deltaPx on a track
 * trackPx wide that shows windowMinutes, snapped to the grid.
 */
export function draggedStart(bar: Pick<GanttBar, "start">, deltaPx: number, trackPx: number, windowMinutes: number, snap: number = SNAP_MINUTES): string {
  const minutes = trackPx > 0 ? snapMinutes((deltaPx / trackPx) * windowMinutes, snap) : 0;
  return new Date(Date.parse(bar.start) + minutes * MINUTE_MS).toISOString().replace(".000Z", "Z");
}

/** Bars grouped by resource id, in the order of the resource list; bars on unknown resources are dropped. */
export function groupByResource(bars: GanttBar[], resourceIds: string[]): Map<string, GanttBar[]> {
  const out = new Map<string, GanttBar[]>();
  for (const id of resourceIds) {
    out.set(id, []);
  }
  for (const bar of bars) {
    out.get(bar.resourceId)?.push(bar);
  }
  return out;
}

/** One row per job: the first segment's start and the last segment's end. */
export function jobsOf(bars: GanttBar[]): GanttBar[] {
  const byJob = new Map<string, GanttBar>();
  for (const bar of bars) {
    const seen = byJob.get(bar.jobId);
    if (!seen) {
      byJob.set(bar.jobId, { ...bar });
      continue;
    }
    if (bar.start < seen.start) seen.start = bar.start;
    if (bar.end > seen.end) seen.end = bar.end;
    if (bar.outsideCapacity) seen.outsideCapacity = true;
    if (bar.blockBreach) seen.blockBreach = true;
  }
  return [...byJob.values()].sort((a, b) => a.start.localeCompare(b.start) || a.operNumber - b.operNumber);
}

/**
 * Client-side pre-check for a drag: other jobs on the same resource that the
 * moved job would overlap. The server dry run stays authoritative (FR-23.7).
 */
export function localOverlaps(bars: GanttBar[], moved: { jobId: string; resourceId: string; start: string; end: string }): GanttBar[] {
  const s = Date.parse(moved.start);
  const e = Date.parse(moved.end);
  return bars.filter((b) => b.jobId !== moved.jobId && b.resourceId === moved.resourceId && Date.parse(b.start) < e && s < Date.parse(b.end));
}

/** Stable colour slot per order so an order keeps its colour across the Gantt. */
export function orderTone(orderId: string): number {
  let hash = 0;
  for (let i = 0; i < orderId.length; i++) {
    hash = (hash * 31 + orderId.charCodeAt(i)) >>> 0;
  }
  return hash % ORDER_TONES;
}
