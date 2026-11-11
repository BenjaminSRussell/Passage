/** Cost control workspace maths (FR-38.10): plan against actual and committed, variance and period comparison. No I/O. */
import type { ActualRow, Balance } from "./api/client";
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

/** The months a time span covers: "2026-03", "2026-Q3", "FY2026-Q3", "2026Q3", "FY2026" or "2026". Null for a span it cannot read. */
export function spanMonths(span: string): [string, string] | null {
  const m = /^(?:FY)?(\d{4})(?:-?(?:Q([1-4])|(0[1-9]|1[0-2])))?$/.exec(span);
  if (!m) return null;
  const year = m[1];
  const pad = (n: number) => String(n).padStart(2, "0");
  if (m[3]) return [`${year}-${m[3]}`, `${year}-${m[3]}`];
  if (m[2]) {
    const q = Number(m[2]);
    return [`${year}-${pad(q * 3 - 2)}`, `${year}-${pad(q * 3)}`];
  }
  return [`${year}-01`, `${year}-12`];
}

/** The first and last month any span of the balances covers, for the default window of the cost-ledger comparison. */
export function balanceWindow(balances: Balance[]): [string, string] | null {
  let from = "";
  let to = "";
  for (const b of balances) {
    const months = spanMonths(b.timeSpanKey);
    if (!months) continue;
    if (from === "" || months[0] < from) from = months[0];
    if (to === "" || months[1] > to) to = months[1];
  }
  return from === "" ? null : [from, to];
}

/** The cost center part of a budget dimension key (`account|costCenter`); a key without an account is the cost center itself. */
export function costCenterOf(dimension: string): string {
  const i = dimension.indexOf("|");
  return i < 0 ? dimension : dimension.slice(i + 1);
}

export interface CostCenterRow {
  code: string;
  name: string;
  plan: string;
  /** Actuals the budget register holds (posted by journals into budget control). */
  registerActual: string;
  /** Actual cost the cost ledger holds for the cost center, in the rule's currency. */
  ledgerActual: string;
  /** Ledger less register: what the two actuals disagree by, so a gap in either is seen. */
  gap: string;
  committed: string;
  /** plan - ledger actual - committed - earmarked. */
  left: string;
  usedPct: string | null;
  level: ControlLevel;
}

/**
 * Plan, register actuals and commitments per cost center beside the cost ledger's actual cost of the cost object with the same
 * code (FR-38.10). Only balances whose span lies inside from..to count, and only ledger rows in `currency` and inside the window.
 */
export function costCenterRows(balances: Balance[], actuals: ActualRow[], from: string, to: string, currency: string): CostCenterRow[] {
  const by = new Map<string, { name: string; plan: bigint; register: bigint; ledger: bigint; committed: bigint; earmarked: bigint }>();
  const slot = (code: string) => {
    let t = by.get(code);
    if (!t) {
      t = { name: code, plan: 0n, register: 0n, ledger: 0n, committed: 0n, earmarked: 0n };
      by.set(code, t);
    }
    return t;
  };
  for (const b of balances) {
    const months = spanMonths(b.timeSpanKey);
    if (!months || months[0] < from || months[1] > to) continue;
    const t = slot(costCenterOf(b.controlDimensionKey));
    t.plan += units(b.budgetTotal);
    t.register += units(b.actuals);
    t.committed += units(b.encumbrances) + units(b.preEncumbrances);
    t.earmarked += units(b.reservations);
  }
  for (const a of actuals) {
    if (a.currency !== currency || a.period < from || a.period > to) continue;
    const t = slot(a.code);
    t.name = a.name;
    t.ledger += units(a.total);
  }
  return [...by.entries()]
    .map(([code, t]): CostCenterRow => {
      const used = t.ledger + t.committed + t.earmarked;
      return {
        code,
        name: t.name,
        plan: fromUnits(t.plan),
        registerActual: fromUnits(t.register),
        ledgerActual: fromUnits(t.ledger),
        gap: fromUnits(t.ledger - t.register),
        committed: fromUnits(t.committed),
        left: fromUnits(t.plan - used),
        usedPct: percentOf(used, t.plan),
        level: controlLevel(t.plan, used),
      };
    })
    .sort((a, b) => a.code.localeCompare(b.code));
}
