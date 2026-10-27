import type { BadgeTone } from "../../shell/ui";
import type { CostGroup, Flushing, JournalType, ProductionCommand, ProductionStatus, Proposal, VarianceType } from "./api/client";

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
};

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
