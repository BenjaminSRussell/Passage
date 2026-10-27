import type { BadgeTone } from "../../shell/ui";
import type {
  BulkCommand,
  CostGroup,
  EditOp,
  Flushing,
  JournalType,
  LoggedCommand,
  ProductionCommand,
  ProductionStatus,
  Proposal,
  ReworkReference,
  ReworkRefType,
  Tracking,
  TriggerField,
  TriggerFiring,
  TriggerFunction,
  TriggerOperator,
  VarianceType,
} from "./api/client";

export const commandLabel: Record<ProductionCommand, string> = {
  estimate: "Estimate",
  schedule: "Schedule",
  release: "Release",
  start: "Start",
  "report-finished": "Report as finished",
  end: "End",
  cancel: "Cancel order",
  "reset-status": "Reset status",
};

/** Labels of status-log commands, including split and merge (FR-21.15). */
export const loggedCommandLabel: Record<LoggedCommand, string> = {
  ...commandLabel,
  split: "Split",
  merge: "Merge",
};

/** Labels of the bulk commands, in the words of a list action. */
export const bulkCommandLabel: Record<BulkCommand, string> = {
  estimate: "Estimate",
  schedule: "Schedule",
  release: "Release",
  start: "Start",
  end: "End",
  cancel: "Cancel",
};

export const statusLabel: Record<ProductionStatus, string> = {
  Created: "Created",
  Estimated: "Estimated",
  Scheduled: "Scheduled",
  Released: "Released",
  Started: "Started",
  ReportedAsFinished: "Reported as finished",
  Ended: "Ended",
  Cancelled: "Cancelled",
};

export const statusTone: Record<ProductionStatus, BadgeTone> = {
  Created: "neutral",
  Estimated: "neutral",
  Scheduled: "info",
  Released: "info",
  Started: "warning",
  ReportedAsFinished: "success",
  Ended: "success",
  Cancelled: "danger",
};

export const costGroupLabel: Record<CostGroup, string> = {
  material: "Material",
  labor: "Labor",
  overhead: "Overhead",
  subcontract: "Subcontract",
};

export const journalTypeLabel: Record<JournalType, string> = {
  "picking-list": "Picking list",
  "route-card": "Route card",
  "report-as-finished": "Report as finished",
};

export const proposalLabel: Record<Proposal, string> = {
  remaining: "Remaining quantity",
  proportional: "Proportional to reported output",
  none: "Enter the lines",
};

export const flushingLabel: Record<Flushing, string> = {
  manual: "Manual",
  start: "At start",
  finish: "At finish",
  "available-at-location": "Available at location",
};

export const varianceLabel: Record<VarianceType, string> = {
  "lot-size": "Lot size",
  "production-quantity": "Quantity",
  "production-price": "Price",
  "production-substitution": "Substitution",
  rework: "Rework",
};

export const editOpLabel: Record<EditOp, string> = {
  add: "Added",
  remove: "Removed",
  change: "Changed",
};

export const reworkRefLabel: Record<ReworkRefType, string> = {
  order: "Production order",
  batch: "Batch",
  serial: "Serial number",
};

export const trackingLabel: Record<Tracking, string> = {
  serial: "Serial",
  batch: "Batch",
};

/** "Batch B-7", "Serial number S-1"; an order reference shows the order's id. */
export function describeReference(ref: ReworkReference): string {
  return `${reworkRefLabel[ref.type] ?? ref.type} ${ref.id}`;
}

/** Pads a decimal string to at least two fraction digits without converting it to a float. */
export function amount(value: string | undefined): string {
  if (!value) {
    return "0.00";
  }
  const [whole, fraction = ""] = value.split(".");
  return `${whole}.${fraction.padEnd(2, "0")}`;
}

export const dash = "—";

/** True when a decimal string is zero ("0", "0.00", "-0"). */
export function isZero(value: string | undefined): boolean {
  return !value || !/[1-9]/.test(value);
}

const SCALE = 6;

function toUnits(value: string): bigint {
  const negative = value.startsWith("-");
  const [whole, fraction = ""] = value.replace(/^[-+]/, "").split(".");
  const units = BigInt(whole || "0") * 10n ** BigInt(SCALE) + BigInt((fraction + "0".repeat(SCALE)).slice(0, SCALE) || "0");
  return negative ? -units : units;
}

/** The negation of a decimal string. */
export function negate(value: string): string {
  return value.startsWith("-") ? value.slice(1) : `-${value}`;
}

/** Adds decimal strings exactly (up to 6 places) without converting them to floats. */
export function addDecimals(values: string[]): string {
  const total = values.reduce((sum, v) => sum + toUnits(v), 0n);
  const negative = total < 0n;
  const abs = negative ? -total : total;
  const whole = abs / 10n ** BigInt(SCALE);
  const fraction = (abs % 10n ** BigInt(SCALE)).toString().padStart(SCALE, "0").replace(/0+$/, "");
  return `${negative ? "-" : ""}${whole}${fraction ? `.${fraction}` : ""}`;
}

/** Trigger point functions (FR-21.17). */
export const triggerFunctionLabel: Record<TriggerFunction, string> = {
  "insert-operations": "Insert operations",
  "create-rework-order": "Create a rework order",
  notify: "Notify",
};

/** The feedback a trigger condition compares. */
export const triggerFieldLabel: Record<TriggerField, string> = {
  qty_error: "Error quantity",
  qty_good: "Good quantity",
  hours: "Hours",
};

export const triggerOperatorLabel: Record<TriggerOperator, string> = {
  ">": "is more than",
  ">=": "is at least",
  "<": "is less than",
  "<=": "is at most",
  "==": "equals",
  "!=": "is not",
};

/** A firing in words: what the trigger did, or why it did nothing. */
export function describeFiring(f: TriggerFiring): string {
  if (f.status === "failed") {
    return `Failed: ${f.reason ?? "no reason given"}`;
  }
  switch (f.function) {
    case "insert-operations":
      return `Inserted operations ${f.insertedOperations.join(", ")}`;
    case "create-rework-order":
      return `Created a rework order for ${f.feedback.qtyError}`;
    default:
      return "Notification sent";
  }
}

/** A stored condition (`qty_error > 0`) in words, or as stored when it does not parse. */
export function describeCondition(condition: string): string {
  const [field, op, value, ...rest] = condition.trim().split(/\s+/);
  const fieldLabel = triggerFieldLabel[field as TriggerField];
  const opLabel = triggerOperatorLabel[op as TriggerOperator];
  if (!fieldLabel || !opLabel || value === undefined || rest.length > 0) {
    return condition;
  }
  return `${fieldLabel} ${opLabel} ${value}`;
}
