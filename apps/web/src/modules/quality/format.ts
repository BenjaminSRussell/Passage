import type { BadgeTone } from "../../shell/ui";
import type { CalibrationStatus, DecisionKind, OrderStatus, PullStatus, SamplingPlan, StudyStatus, Trigger } from "./api/client";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** A non-negative decimal string, as the service accepts it. */
export const DECIMAL = /^\d{1,12}(\.\d{1,6})?$/;

/** A signed decimal string for variable results and limits. */
export const SIGNED_DECIMAL = /^-?\d{1,12}(\.\d{1,6})?$/;

/** Instrument, test group and compliance list codes (service pattern). */
export const CODE = /^[A-Z0-9][A-Z0-9_-]{1,31}$/;

/** Region codes: a country (DE), a subdivision (US-CA) or a region group (EU). */
export const REGION = /^[A-Z][A-Z0-9-]{1,9}$/;

/**
 * A CAS registry number whose check digit is right: the digits before it, weighted
 * 1, 2, 3, ... from the right, sum to the check digit modulo 10 (7439-92-1 is lead).
 */
export function validCAS(cas: string): boolean {
  const m = /^(\d{2,7})-(\d{2})-(\d)$/.exec(cas);
  if (!m) return false;
  const body = m[1] + m[2];
  let sum = 0;
  for (let i = 0; i < body.length; i++) sum += Number(body[body.length - 1 - i]) * (i + 1);
  return sum % 10 === Number(m[3]);
}

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** "2026-10-26". */
export function formatDate(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toISOString().slice(0, 10);
}

/** Decimal strings with trailing zeros trimmed ("20.000000" → "20"). */
export function qty(value?: string | null): string {
  if (value === undefined || value === null || value === "") return dash;
  return value.includes(".") ? value.replace(/\.?0+$/, "") : value;
}

/** First eight characters of a UUID, for dense tables. */
export function shortId(id?: string | null): string {
  return id ? id.slice(0, 8) : dash;
}

export const TRIGGER_LABELS: Record<Trigger, string> = {
  PURCHASE_RECEIPT: "Purchase receipt",
  PRODUCTION_RAF: "Production report as finished",
  SALES_PICK: "Sales pick",
  TRANSFER_RECEIPT: "Transfer receipt",
  QUARANTINE: "Quarantine",
  MANUAL: "Manual",
  SOURCE_INSPECTION: "Source inspection",
};

export const STATUS_LABELS: Record<OrderStatus, { label: string; tone: BadgeTone }> = {
  CREATED: { label: "Created", tone: "neutral" },
  OPEN: { label: "Open", tone: "neutral" },
  RESULTS_IN_PROGRESS: { label: "Results in progress", tone: "info" },
  PENDING_DECISION: { label: "Pending decision", tone: "warning" },
  CLOSED: { label: "Closed", tone: "success" },
  CANCELLED: { label: "Cancelled", tone: "neutral" },
};

export const ORDER_STATUSES = Object.keys(STATUS_LABELS) as OrderStatus[];

export const DECISION_LABELS: Record<DecisionKind, string> = {
  ACCEPT: "Accept",
  REJECT: "Reject",
  ACCEPT_WITH_DEVIATION: "Accept with deviation",
  REWORK: "Rework",
  RETURN_TO_VENDOR: "Return to vendor",
  SCRAP: "Scrap",
};

export const CALIBRATION_LABELS: Record<CalibrationStatus, { label: string; tone: BadgeTone }> = {
  IN_CALIBRATION: { label: "In calibration", tone: "success" },
  DUE_SOON: { label: "Due within 14 days", tone: "warning" },
  OVERDUE: { label: "Overdue", tone: "danger" },
};

/** "AQL 2.5, normal", "Fixed 5", "10 %". */
export function describePlan(plan: SamplingPlan): string {
  switch (plan.method) {
    case "AQL":
      return `AQL ${qty(plan.aql)}, ${(plan.severity ?? "NORMAL").toLowerCase()}, level ${plan.inspectionLevel ?? "II"}`;
    case "FIXED":
      return `Fixed ${qty(plan.fixedQty)}`;
    case "PERCENT":
      return `${qty(plan.percent)} %`;
  }
  return plan.method;
}

/** Copies service field errors whose names are form fields. */
export function pickFields<K extends string>(fields: Record<string, string>, names: readonly K[]): Partial<Record<K, string>> {
  const out: Partial<Record<K, string>> = {};
  for (const name of names) {
    if (fields[name]) out[name] = fields[name];
  }
  return out;
}

export const STUDY_STATUS_LABELS: Record<StudyStatus, { label: string; tone: BadgeTone }> = {
  PLANNED: { label: "Planned", tone: "neutral" },
  ACTIVE: { label: "Active", tone: "info" },
  COMPLETED: { label: "Completed", tone: "success" },
  TERMINATED: { label: "Terminated", tone: "warning" },
};

export const STUDY_STATUSES = Object.keys(STUDY_STATUS_LABELS) as StudyStatus[];

export const PULL_STATUS_LABELS: Record<PullStatus, { label: string; tone: BadgeTone }> = {
  PLANNED: { label: "Planned", tone: "neutral" },
  ORDERED: { label: "Order created", tone: "info" },
  PULLED: { label: "Pulled", tone: "success" },
  MISSED: { label: "Missed", tone: "danger" },
};
