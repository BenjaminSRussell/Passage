import type { BadgeTone } from "../../shell/ui";
import type { CostGroup, ProductionCommand, ProductionStatus } from "./api/client";

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

/** Pads a decimal string to at least two fraction digits without converting it to a float. */
export function amount(value: string | undefined): string {
  if (!value) {
    return "0.00";
  }
  const [whole, fraction = ""] = value.split(".");
  return `${whole}.${fraction.padEnd(2, "0")}`;
}

export const dash = "—";
