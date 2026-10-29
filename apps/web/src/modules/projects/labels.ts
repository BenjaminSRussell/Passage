import type { BadgeTone } from "../../shell/ui";
import type { BillingReason, ChangeOrderStatus, ChangeOrderType, PlanKind, ProjectType, ProposalStatus, RiskKind, RiskStatus, Stage, TxnType } from "./api/client";

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

export const txnTypeLabel: Record<TxnType, string> = {
  HOUR: "Hours",
  COST: "Expense",
  ITEM: "Item",
  FEE: "Fee",
  ON_ACCOUNT: "On account",
};

export const proposalStatusLabel: Record<ProposalStatus, string> = {
  DRAFT: "Draft",
  APPROVED: "Approved",
  POSTED: "Posted",
  CANCELED: "Canceled",
};

export const proposalStatusTone: Record<ProposalStatus, BadgeTone> = {
  DRAFT: "neutral",
  APPROVED: "info",
  POSTED: "success",
  CANCELED: "warning",
};

/** The "why not billed" reasons of BR-41.3 in words. */
export const billingReasonLabel: Record<BillingReason, string> = {
  NOT_CHARGEABLE: "The line is not chargeable.",
  ALREADY_PROPOSED: "The line is already on an invoice proposal.",
  OUT_OF_RANGE: "The line's date is outside the window.",
  PROJECT_STAGE: "The project is not in process or finished.",
  TXN_TYPE: "The project does not bill this transaction type in this selection.",
  PROJECT_NOT_IN_SCOPE: "The project is not in the selection.",
  MILESTONE_NOT_DUE: "The line is not on a billable milestone that finishes by the end of the window.",
  PROJECT_TYPE: "Projects of this type do not bill a customer.",
};
