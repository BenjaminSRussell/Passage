import type { BadgeTone } from "../../shell/ui";
import type { ChangeOrderStatus, ChangeOrderType, PlanKind, ProjectType, RiskKind, RiskStatus, Stage } from "./api/client";

export const dash = "—";

export const stageLabel: Record<Stage, string> = {
  CREATED: "Created",
  IN_PROCESS: "In process",
  ON_HOLD: "On hold",
  FINISHED: "Finished",
};

export const stageTone: Record<Stage, BadgeTone> = {
  CREATED: "neutral",
  IN_PROCESS: "info",
  ON_HOLD: "warning",
  FINISHED: "success",
};

export const typeLabel: Record<ProjectType, string> = {
  TIME_MATERIAL: "Time and material",
  FIXED_PRICE: "Fixed price",
  INTERNAL: "Internal",
  COST: "Cost",
  INVESTMENT: "Investment",
  TIME: "Time",
};


export const planKindLabel: Record<PlanKind, string> = {
  WORKING: "Working",
  BASELINE: "Baseline",
  SIMULATION: "Simulation",
};

export const planKindTone: Record<PlanKind, BadgeTone> = {
  WORKING: "info",
  BASELINE: "success",
  SIMULATION: "neutral",
};

export const linePropertyLabel: Record<string, string> = {
  CHARGEABLE: "Chargeable",
  NON_CHARGEABLE: "Non-chargeable",
  "*": "Any",
};

export const changeOrderTypeLabel: Record<ChangeOrderType, string> = {
  SCOPE_CHANGE: "Scope change",
  CLAIM_CUSTOMER: "Customer claim",
  CLAIM_VENDOR: "Vendor claim",
  INTERNAL: "Internal",
};

export const changeOrderStatusLabel: Record<ChangeOrderStatus, string> = {
  DRAFT: "Draft",
  SUBMITTED: "Submitted",
  APPROVED: "Approved",
  REJECTED: "Rejected",
  APPLIED: "Applied",
};

export const changeOrderStatusTone: Record<ChangeOrderStatus, BadgeTone> = {
  DRAFT: "neutral",
  SUBMITTED: "info",
  APPROVED: "warning",
  REJECTED: "danger",
  APPLIED: "success",
};

export const riskKindLabel: Record<RiskKind, string> = { RISK: "Risk", ISSUE: "Issue" };

export const riskStatusLabel: Record<RiskStatus, string> = { OPEN: "Open", MITIGATED: "Mitigated", CLOSED: "Closed" };

export const riskStatusTone: Record<RiskStatus, BadgeTone> = { OPEN: "warning", MITIGATED: "info", CLOSED: "neutral" };

/** Score bands for probability × impact: 15 and above high, 8 and above medium. */
export function riskScoreTone(score: number): BadgeTone {
  return score >= 15 ? "danger" : score >= 8 ? "warning" : "success";
}
