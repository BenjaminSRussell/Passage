import type { BadgeTone } from "../../shell/ui";
import type {
  BulkCommand,
  ChangeStatus,
  CoProductType,
  OCMMode,
  SplitAllocation,
  CostGroup,
  EditOp,
  Flushing,
  JobType,
  ListedJournalType,
  LoggedCommand,
  OperStatus,
  ProductionCommand,
  ProductionStatus,
  Proposal,
  ReworkReference,
  ReworkRefType,
  ScheduleDirection,
  SchedulingMethod,
  Tracking,
  TriggerField,
  TriggerFiring,
  TriggerFunction,
  TriggerOperator,
  VarianceType,
} from "./api/client";

export const coProductTypeLabel: Record<CoProductType, string> = {
  "co-product": "Co-product",
  "by-product": "By-product",
};

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

/** The feedback status of an operation (FR-21.6). */
export const operStatusLabel: Record<OperStatus, string> = {
  open: "Open",
  started: "Started",
  finished: "Finished",
};

export const operStatusTone: Record<OperStatus, BadgeTone> = {
  open: "neutral",
  started: "warning",
  finished: "success",
};

export const costGroupLabel: Record<CostGroup, string> = {
  material: "Material",
  labor: "Labor",
  overhead: "Overhead",
  subcontract: "Subcontract",
};

export const journalTypeLabel: Record<ListedJournalType, string> = {
  "picking-list": "Picking list",
  "route-card": "Route card",
  "report-as-finished": "Report as finished",
  "split-transfer": "Split transfer",
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

export const changeStatusLabel: Record<ChangeStatus, string> = { pending: "Pending", applied: "Applied", dismissed: "Dismissed" };

export const changeStatusTone: Record<ChangeStatus, BadgeTone> = { pending: "warning", applied: "success", dismissed: "neutral" };

export const ocmModeLabel: Record<OCMMode, string> = {
  automatic: "Automatic (applied at the next status command)",
  manual: "Manual (the planner confirms)",
  none: "None (ignore new versions)",
};

/** A one-line summary of what an ECO changes on an order, for the worklist. */
export function describeDelta(d: {
  linesAdded: number;
  linesRemoved: number;
  linesChanged: number;
  operationsAdded: number;
  operationsRemoved: number;
  operationsChanged: number;
}): string {
  const parts: string[] = [];
  const add = (n: number, what: string) => n > 0 && parts.push(`${n} ${what}`);
  add(d.linesAdded, "lines added");
  add(d.linesRemoved, "lines removed");
  add(d.linesChanged, "lines changed");
  add(d.operationsAdded, "operations added");
  add(d.operationsRemoved, "operations removed");
  add(d.operationsChanged, "operations changed");
  return parts.length > 0 ? parts.join(", ") : "No difference";
}

export const splitAllocationLabel: Record<SplitAllocation, string> = {
  proportional: "Proportional (consumption, hours and WIP move with the quantity)",
  "keep-on-original": "Keep on the original order",
};

export const yesNo = (value: boolean) => (value ? "Yes" : "No");

export const jobTypeLabel: Record<JobType, string> = {
  "queue-before": "Queue before",
  setup: "Setup",
  process: "Process",
  "queue-after": "Queue after",
  transport: "Transport",
};

export const schedulingMethodLabel: Record<SchedulingMethod, string> = {
  operations: "By operation (setup and run together)",
  jobs: "By job (setup and run apart)",
};

export const directionLabel: Record<ScheduleDirection, string> = {
  forward: "Forward from a start",
  backward: "Backward from a finish",
};

/** A schedule instant as the UTC date and minute it falls on, for tables. */
export const instant = (value: string): string => (value ? `${value.slice(0, 10)} ${value.slice(11, 16)}Z` : dash);
