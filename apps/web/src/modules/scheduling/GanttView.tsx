import { useRef, useState, type PointerEvent } from "react";
import { cx } from "../../shell/ui/cx";
import type { GanttBar } from "./api/client";
import { formatAmount, formatDateTime, shortId } from "./format";
import { barPosition, changeoverSpan, draggedStart, groupByResource, orderTone, SNAP_MINUTES } from "./gantt";
import styles from "./GanttView.module.css";

export interface GanttResource {
  id: string;
  code: string;
}

/** Window lengths the axis has layout classes for. */
export const GANTT_DAYS = [1, 2, 3, 7, 14] as const;
export type GanttDays = (typeof GANTT_DAYS)[number];

export interface GanttViewProps {
  bars: GanttBar[];
  resources: GanttResource[];
  /** Window start (UTC midnight) and length in days. */
  from: string;
  days: GanttDays;
  selectedJobId?: string | null;
  /** jobId → message; those bars get a danger outline and the message as tooltip (FR-23.7). */
  violations?: Record<string, string>;
  onSelect?(bar: GanttBar): void;
  /** Drag grid in minutes; defaults to 5 (the scheduling parameter dragSnapMinutes). */
  snapMinutes?: number;
  /** Called with the snapped new start after a horizontal drag. */
  onDrop?(bar: GanttBar, newStart: string): void;
}

const VIEW_WIDTH = 1000;
const ROW_HEIGHT = 32;
const BAR_INSET = 5;
const DAY_MS = 86_400_000;

interface Drag {
  bar: GanttBar;
  originX: number;
  trackPx: number;
  dx: number;
}

function dayLabel(ms: number): string {
  const d = new Date(ms);
  return d.toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short", timeZone: "UTC" });
}

/**
 * Resources × time Gantt drawn as one SVG track per resource. Bars are placed
 * by SVG geometry attributes and coloured by order through CSS classes. Drag a
 * bar sideways to propose a new start; click it to select it. The SVG is a
 * pointer surface only: the page's jobs table offers the same actions from the
 * keyboard.
 */
export function GanttView({ bars, resources, from, days, selectedJobId, violations = {}, snapMinutes = SNAP_MINUTES, onSelect, onDrop }: GanttViewProps) {
  const [drag, setDrag] = useState<Drag | null>(null);
  const moved = useRef(false);
  const start = Date.parse(from);
  const to = new Date(start + days * DAY_MS).toISOString();
  const windowMinutes = days * 24 * 60;
  const rows = groupByResource(
    bars,
    resources.map((r) => r.id),
  );
  const ticks = Array.from({ length: days }, (_, i) => start + i * DAY_MS);
  const gridLines = days <= 3 ? days * 4 : days;

  function pointerDown(event: PointerEvent<SVGRectElement>, bar: GanttBar) {
    const track = event.currentTarget.ownerSVGElement?.getBoundingClientRect().width ?? 0;
    event.currentTarget.setPointerCapture?.(event.pointerId);
    moved.current = false;
    setDrag({ bar, originX: event.clientX, trackPx: track, dx: 0 });
  }

  function pointerMove(event: PointerEvent<SVGRectElement>) {
    if (!drag) return;
    const dx = event.clientX - drag.originX;
    if (Math.abs(dx) > 2) moved.current = true;
    setDrag({ ...drag, dx });
  }

  function pointerUp(event: PointerEvent<SVGRectElement>) {
    const current = drag;
    setDrag(null);
    if (!current) return;
    const dx = event.clientX - current.originX;
    const minutes = current.trackPx > 0 ? (dx / current.trackPx) * windowMinutes : 0;
    if (!moved.current || Math.abs(minutes) < snapMinutes) {
      onSelect?.(current.bar);
      return;
    }
    onDrop?.(current.bar, draggedStart(current.bar, dx, current.trackPx, windowMinutes, snapMinutes));
  }

  return (
    <div className={styles.gantt}>
      <div className={styles.axis} aria-hidden="true">
        <span className={styles.corner}>Resource</span>
        <div className={cx(styles.ticks, styles[`days${days}`])}>
          {ticks.map((t) => (
            <span key={t} className={styles.tick}>
              {dayLabel(t)}
            </span>
          ))}
        </div>
      </div>
      {resources.map((res) => {
        const rowBars = rows.get(res.id) ?? [];
        return (
          <div key={res.id} className={styles.row}>
            <span className={styles.label}>{res.code}</span>
            <svg
              className={styles.track}
              viewBox={`0 0 ${VIEW_WIDTH} ${ROW_HEIGHT}`}
              preserveAspectRatio="none"
              aria-hidden="true"
              data-resource={res.code}
            >
              {Array.from({ length: gridLines - 1 }, (_, i) => {
                const x = ((i + 1) * VIEW_WIDTH) / gridLines;
                const major = days <= 3 ? (i + 1) % 4 === 0 : true;
                return <line key={x} x1={x} x2={x} y1={0} y2={ROW_HEIGHT} className={major ? styles.gridMajor : styles.grid} />;
              })}
              {rowBars.map((bar) => {
                const pos = barPosition(bar, from, to);
                if (!pos) return null;
                if (bar.cleanout) {
                  // FR-23.14, BR-23.13: the cleanout window of a placed campaign, closed time in one piece.
                  return (
                    <rect
                      key={`${bar.jobId}-${bar.orderId}-cleanout`}
                      data-cleanout-campaign={bar.jobId}
                      x={pos.x * VIEW_WIDTH}
                      y={BAR_INSET}
                      width={Math.max(pos.width * VIEW_WIDTH, 1)}
                      height={ROW_HEIGHT - 2 * BAR_INSET}
                      rx={3}
                      className={styles.cleanoutBar}
                    >
                      <title>
                        {`Cleanout${bar.setupGroup ? ` after group ${bar.setupGroup}` : ""} following order ${bar.orderId.slice(0, 8)}${bar.cleaningSetId ? `, cleaning set ${bar.cleaningSetId}` : ""}: ${formatDateTime(bar.start)} to ${formatDateTime(bar.end)}, closed time`}
                      </title>
                    </rect>
                  );
                }
                if (bar.secondary) {
                  const primary = bar.primaryResourceId ? (resources.find((r) => r.id === bar.primaryResourceId)?.code ?? shortId(bar.primaryResourceId)) : "";
                  return (
                    <rect
                      key={`${bar.jobId}-${bar.segment}-secondary`}
                      data-secondary-job={bar.jobId}
                      x={pos.x * VIEW_WIDTH}
                      y={BAR_INSET}
                      width={Math.max(pos.width * VIEW_WIDTH, 1)}
                      height={ROW_HEIGHT - 2 * BAR_INSET}
                      rx={3}
                      className={cx(
                        styles.secondaryBar,
                        styles[`tone${orderTone(bar.orderId)}`],
                        bar.outsideCapacity && styles.violation,
                        selectedJobId === bar.jobId && styles.selected,
                      )}
                      onClick={() => onSelect?.(bar)}
                    >
                      <title>
                        {`Held as secondary for order ${bar.orderId.slice(0, 8)} operation ${bar.operNumber}${primary ? ` running on ${primary}` : ""}: ${formatDateTime(bar.start)} to ${formatDateTime(bar.end)}${bar.firmed ? ", firmed" : ""}${bar.outsideCapacity ? ", outside open capacity (SCHED-0002)" : ""}`}
                      </title>
                    </rect>
                  );
                }
                const violation = violations[bar.jobId];
                // AC-23.7: a booked job a later downtime overlaps is outlined like a violation.
                const outside = bar.outsideCapacity ? ", outside open capacity (SCHED-0002)" : "";
                // BR-23.15: a booked job in time a capacity block now keeps from it is dotted, not outlined as a violation.
                const breach = bar.blockBreach && !bar.outsideCapacity;
                const blocked = breach ? ", inside a capacity block it does not match" : "";
                const dragging = drag?.bar.jobId === bar.jobId;
                const shift = dragging && drag.trackPx > 0 ? (drag.dx / drag.trackPx) * VIEW_WIDTH : 0;
                const late = bar.lateMinutes > 0 ? `, late by ${bar.lateMinutes} min` : "";
                const changeover = changeoverSpan(bar);
                const changeoverPos = changeover ? barPosition(changeover, from, to) : null;
                const priced = [
                  bar.changeoverCost !== null && bar.changeoverCurrency !== null ? `costing ${formatAmount(bar.changeoverCost, bar.changeoverCurrency)}` : "",
                  bar.cleaningSetId ? `cleaning set ${bar.cleaningSetId}` : "",
                ].filter(Boolean);
                const setup = changeover
                  ? `, starts with a ${bar.changeoverMinutes} min changeover${bar.setupGroup ? ` to ${bar.setupGroup}` : ""}${priced.length > 0 ? ` (${priced.join(", ")})` : ""}`
                  : "";
                const tools =
                  bar.secondaryResourceIds.length > 0
                    ? `, with secondary ${bar.secondaryResourceIds.map((id) => resources.find((r) => r.id === id)?.code ?? shortId(id)).join(", ")}`
                    : "";
                return (
                  <g key={`${bar.jobId}-${bar.segment}`}>
                    <rect
                      data-job={bar.jobId}
                      x={pos.x * VIEW_WIDTH}
                      y={BAR_INSET}
                      width={Math.max(pos.width * VIEW_WIDTH, 1)}
                      height={ROW_HEIGHT - 2 * BAR_INSET}
                      rx={3}
                      transform={shift ? `translate(${shift} 0)` : undefined}
                      className={cx(
                        styles.bar,
                        styles[`tone${orderTone(bar.orderId)}`],
                        bar.lateMinutes > 0 && styles.late,
                        bar.firmed && styles.firmed,
                        (violation || bar.outsideCapacity) && styles.violation,
                        breach && !violation && styles.breach,
                        selectedJobId === bar.jobId && styles.selected,
                        dragging && styles.dragging,
                      )}
                      onPointerDown={(event) => pointerDown(event, bar)}
                      onPointerMove={pointerMove}
                      onPointerUp={pointerUp}
                      onPointerCancel={() => setDrag(null)}
                    >
                      <title>
                        {violation ??
                          `Order ${bar.orderId.slice(0, 8)} operation ${bar.operNumber}: ${formatDateTime(bar.start)} to ${formatDateTime(bar.end)}${setup}${tools}${late}${bar.firmed ? ", firmed" : ""}${outside}${blocked}`}
                      </title>
                    </rect>
                    {changeoverPos ? (
                      <rect
                        data-changeover={bar.jobId}
                        x={changeoverPos.x * VIEW_WIDTH}
                        y={BAR_INSET}
                        width={Math.max(changeoverPos.width * VIEW_WIDTH, 1)}
                        height={ROW_HEIGHT - 2 * BAR_INSET}
                        transform={shift ? `translate(${shift} 0)` : undefined}
                        className={styles.changeover}
                        aria-hidden="true"
                      />
                    ) : null}
                  </g>
                );
              })}
            </svg>
          </div>
        );
      })}
    </div>
  );
}
