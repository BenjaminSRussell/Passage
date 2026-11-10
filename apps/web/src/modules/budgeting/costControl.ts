/** Cost control workspace maths (FR-38.10): plan against actual and committed, variance and period comparison. No I/O. */
import type { Balance } from "./api/client";
import { fromUnits, toUnits } from "./logic";

export type ControlLevel = "ok" | "near" | "over";

/** A combination counts as near its budget from this share of it spent, committed or earmarked. */
export const NEAR_PCT = 90n;

export interface ControlRow {
  dimension: string;
  span: string;
  plan: string;
  actual: string;
  /** Encumbrances plus pre-encumbrances: ordered, not yet invoiced. */
  committed: string;
  earmarked: string;
  available: string;
  /** plan - actual - committed - earmarked, the same as available; kept apart so the screen never recomputes it. */
  variance: string;
  /** Share of the plan used by actual, committed and earmarked funds, two decimals; null when there is no plan. */
  usedPct: string | null;
  level: ControlLevel;
}

const units = (s: string): bigint => toUnits(s);

/** used * 100 / plan with two decimals, rounded half away from zero; null for a zero plan. */
export function percentOf(used: bigint, plan: bigint): string | null {
  if (plan === 0n) return null;
  const num = used * 10000n;
  const absNum = num < 0n ? -num : num;
  const absDen = plan < 0n ? -plan : plan;
  const abs = (absNum * 2n + absDen) / (absDen * 2n);
  const negative = abs !== 0n && num < 0n !== plan < 0n;
  return `${negative ? "-" : ""}${abs / 100n}.${(abs % 100n).toString().padStart(2, "0")}`;
}

export function controlLevel(plan: bigint, used: bigint): ControlLevel {
  if (used > plan) return "over";
  if (plan > 0n && used * 100n >= plan * NEAR_PCT) return "near";
  return "ok";
}

export function controlRows(balances: Balance[]): ControlRow[] {
  return balances
    .map((b): ControlRow => {
      const plan = units(b.budgetTotal);
      const actual = units(b.actuals);
      const committed = units(b.encumbrances) + units(b.preEncumbrances);
      const earmarked = units(b.reservations);
      const used = actual + committed + earmarked;
      return {
        dimension: b.controlDimensionKey,
        span: b.timeSpanKey,
        plan: fromUnits(plan),
        actual: fromUnits(actual),
        committed: fromUnits(committed),
        earmarked: fromUnits(earmarked),
        available: fromUnits(plan - used),
        variance: fromUnits(plan - used),
        usedPct: percentOf(used, plan),
        level: controlLevel(plan, used),
      };
    })
    .sort((a, b) => a.dimension.localeCompare(b.dimension) || a.span.localeCompare(b.span));
}

export interface PeriodRow {
  span: string;
  plan: string;
  actual: string;
  committed: string;
  variance: string;
  usedPct: string | null;
  /** Actual spend against the previous span in the list, two decimals; null for the first span or a zero previous. */
  actualChangePct: string | null;
}

/** Totals per time span, in span order, each with how actual spend moved against the span before it. */
export function periodRows(balances: Balance[]): PeriodRow[] {
  const by = new Map<string, { plan: bigint; actual: bigint; committed: bigint; earmarked: bigint }>();
  for (const b of balances) {
    const t = by.get(b.timeSpanKey) ?? { plan: 0n, actual: 0n, committed: 0n, earmarked: 0n };
    t.plan += units(b.budgetTotal);
    t.actual += units(b.actuals);
    t.committed += units(b.encumbrances) + units(b.preEncumbrances);
    t.earmarked += units(b.reservations);
    by.set(b.timeSpanKey, t);
  }
  let previous: bigint | null = null;
  return [...by.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([span, t]) => {
      const used = t.actual + t.committed + t.earmarked;
      const row: PeriodRow = {
        span,
        plan: fromUnits(t.plan),
        actual: fromUnits(t.actual),
        committed: fromUnits(t.committed),
        variance: fromUnits(t.plan - used),
        usedPct: percentOf(used, t.plan),
        actualChangePct: previous === null || previous === 0n ? null : percentOf(t.actual - previous, previous),
      };
      previous = t.actual;
      return row;
    });
}

export interface ControlTotals {
  plan: string;
  actual: string;
  committed: string;
  variance: string;
  usedPct: string | null;
  over: number;
  near: number;
}

export function controlTotals(rows: ControlRow[]): ControlTotals {
  let plan = 0n;
  let used = 0n;
  let actual = 0n;
  let committed = 0n;
  for (const r of rows) {
    plan += units(r.plan);
    actual += units(r.actual);
    committed += units(r.committed);
    used += units(r.actual) + units(r.committed) + units(r.earmarked);
  }
  return {
    plan: fromUnits(plan),
    actual: fromUnits(actual),
    committed: fromUnits(committed),
    variance: fromUnits(plan - used),
    usedPct: percentOf(used, plan),
    over: rows.filter((r) => r.level === "over").length,
    near: rows.filter((r) => r.level === "near").length,
  };
}
