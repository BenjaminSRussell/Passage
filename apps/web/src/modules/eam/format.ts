import type { BadgeTone } from "../../shell/ui";
import type { PermitStatus, WorkOrderStatus } from "./api/client";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Asset type, job type: the service's ^[A-Z0-9_]{2,20}$ codes. */
export const CODE = /^[A-Z0-9_]{2,20}$/;

/** Plan code: ^[A-Z0-9][A-Z0-9_-]{1,29}$. */
export const PLAN_CODE = /^[A-Z0-9][A-Z0-9_-]{1,29}$/;

export const CURRENCY = /^[A-Z]{3}$/;

/** Fault catalog code: ^[A-Z0-9_]{1,20}$. */
export const FAULT_CODE = /^[A-Z0-9_]{1,20}$/;

/** A calendar date, YYYY-MM-DD. */
export const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** A datetime-local value ("2026-10-21T08:00") read as UTC, as an ISO timestamp; null when it is not one. */
export function utcFromLocalInput(value: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return null;
  const at = new Date(`${value}:00Z`);
  return Number.isNaN(at.getTime()) ? null : at.toISOString();
}

/** A positive decimal with up to 2 places for hours, and up to 6 for rates. */
export const HOURS = /^\d{1,7}(\.\d{1,2})?$/;
export const RATE = /^\d{1,14}(\.\d{1,6})?$/;

/** The calendar day of an ISO timestamp, or a dash. */
export function dayOf(iso?: string | null): string {
  return iso ? iso.slice(0, 10) : dash;
}

/** A timestamp as "YYYY-MM-DD HH:MM" UTC, or a dash. */
export function minuteOf(iso?: string | null): string {
  return iso ? `${iso.slice(0, 10)} ${iso.slice(11, 16)}` : dash;
}

export const statusLabel: Record<WorkOrderStatus, string> = {
  CREATED: "Created",
  SCHEDULED: "Scheduled",
  RELEASED: "Released",
  IN_PROGRESS: "In progress",
  ON_HOLD: "On hold",
  COMPLETED: "Completed",
  CLOSED: "Closed",
  CANCELED: "Canceled",
};

export const statusTone: Record<string, BadgeTone> = {
  CREATED: "neutral",
  SCHEDULED: "info",
  RELEASED: "info",
  IN_PROGRESS: "info",
  ON_HOLD: "warning",
  COMPLETED: "success",
  CLOSED: "success",
  CANCELED: "neutral",
  ACTIVE: "success",
  INACTIVE: "warning",
  DISPOSED: "neutral",
  NEW: "warning",
  TRIAGED: "info",
  CONVERTED: "success",
  REJECTED: "neutral",
  DEFERRED: "neutral",
  VOIDED: "neutral",
  REQUESTED: "warning",
  APPROVED: "info",
  ISSUED: "success",
  SUSPENDED: "warning",
  APPLIED: "warning",
  VERIFIED: "success",
  REMOVED: "neutral",
  DONE: "success",
  SKIPPED: "warning",
};

export const typeLabel: Record<string, string> = {
  PREVENTIVE: "Preventive",
  CORRECTIVE: "Corrective",
  INSPECTION: "Inspection",
  EMERGENCY: "Emergency",
  CAPITAL: "Capital",
  ROUND: "Round",
};

export const kindLabel: Record<string, string> = {
  TIME: "Time",
  COUNTER: "Counter",
  TIME_OR_COUNTER: "Time or counter, whichever first",
  TIME_AND_COUNTER: "Time and counter, whichever last",
  ROUND: "Maintenance round, on time",
};

export function sentence(value: string): string {
  const spaced = value.replace(/_+/g, " ").toLowerCase();
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

/**
 * The next statuses a user may choose (BR-44.3), mirroring domain.CanTransition.
 * Priority 1 (emergency) orders may start before release (BR-44.7).
 */
export function nextStatuses(status: WorkOrderStatus, priority: number): WorkOrderStatus[] {
  const base: Record<WorkOrderStatus, WorkOrderStatus[]> = {
    CREATED: ["SCHEDULED", "CANCELED"],
    SCHEDULED: ["RELEASED", "CANCELED"],
    RELEASED: ["IN_PROGRESS", "CANCELED"],
    IN_PROGRESS: ["ON_HOLD", "COMPLETED", "CANCELED"],
    ON_HOLD: ["IN_PROGRESS", "CANCELED"],
    COMPLETED: ["CLOSED"],
    CLOSED: [],
    CANCELED: [],
  };
  const next = [...base[status]];
  if (priority === 1 && status === "CREATED") next.unshift("RELEASED", "IN_PROGRESS");
  if (priority === 1 && status === "SCHEDULED") next.unshift("IN_PROGRESS");
  return next;
}

/** Hours as "12.5 h" with at most one decimal. */
export function hours(value: number): string {
  return `${Math.round(value * 10) / 10} h`;
}

/** Availability 0..1 as a percentage with one decimal. */
export function percent(value: number): string {
  return `${Math.round(value * 1000) / 10}%`;
}

/** The sum of two decimal strings with two places, in integer cents (never float money). */
export function addMoney(a: string, b: string): string {
  const cents = (v: string) => {
    const m = /^(-?)(\d+)(?:\.(\d+))?$/.exec(v.trim());
    if (!m) return 0n;
    const frac = (m[3] ?? "").padEnd(2, "0").slice(0, 2);
    const n = BigInt(m[2]) * 100n + BigInt(frac);
    return m[1] === "-" ? -n : n;
  };
  const total = cents(a) + cents(b);
  const sign = total < 0n ? "-" : "";
  const abs = total < 0n ? -total : total;
  return `${sign}${abs / 100n}.${String(abs % 100n).padStart(2, "0")}`;
}

/** A decimal string shown with two places ("360.000000" -> "360.00"). */
export function money(value: string): string {
  return addMoney(value, "0");
}

/** The permit transitions a user may choose (BR-44.11), mirroring domain.CanPermitTransition. */
export function nextPermitStatuses(status: PermitStatus): Exclude<PermitStatus, "REQUESTED">[] {
  const next: Record<PermitStatus, Exclude<PermitStatus, "REQUESTED">[]> = {
    REQUESTED: ["APPROVED", "CANCELED"],
    APPROVED: ["ISSUED", "CANCELED"],
    ISSUED: ["SUSPENDED", "CLOSED"],
    SUSPENDED: ["ISSUED", "CLOSED"],
    CLOSED: [],
    CANCELED: [],
  };
  return next[status];
}

/** Personal locks can be applied, verified and removed on an APPROVED, ISSUED or SUSPENDED permit. */
export function permitLockable(status: PermitStatus): boolean {
  return status === "APPROVED" || status === "ISSUED" || status === "SUSPENDED";
}
