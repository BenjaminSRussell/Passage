import type { BadgeTone } from "../../shell/ui";
import type { Campaign, ProcedureStatus, ProcedureStepType } from "./api/client";

export type CampaignState = "killed" | "scheduled" | "live" | "ended";

/** Where a campaign stands on day: the kill switch wins, then its validity (inclusive). */
export function campaignState(c: Pick<Campaign, "killed" | "validFrom" | "validTo">, day: string): CampaignState {
  if (c.killed) return "killed";
  if (day < c.validFrom) return "scheduled";
  if (day > c.validTo) return "ended";
  return "live";
}

export const CAMPAIGN_STATES: Record<CampaignState, { label: string; tone: BadgeTone }> = {
  live: { label: "Live", tone: "success" },
  scheduled: { label: "Scheduled", tone: "info" },
  ended: { label: "Ended", tone: "neutral" },
  killed: { label: "Killed", tone: "danger" },
};

/** Who a campaign is for, in words; empty means every customer. */
export function describeTarget(group: string): string {
  return group.trim() === "" ? "All customers" : `Customer group ${group}`;
}

export const PROCEDURE_STATUSES: Record<ProcedureStatus, { label: string; tone: BadgeTone }> = {
  DRAFT: { label: "Draft", tone: "neutral" },
  ACTIVE: { label: "Active", tone: "success" },
  SUPERSEDED: { label: "Superseded", tone: "neutral" },
};

export const STEP_TYPE_LABELS: Record<ProcedureStepType, string> = {
  PRICE: "Price",
  LINE_DISC: "Line discount",
  MULTILINE_DISC: "Multiline discount",
  TOTAL_DISC: "Total discount",
  SURCHARGE: "Surcharge",
  FREIGHT: "Freight",
  REBATE_ACCRUAL: "Rebate accrual",
  ROUNDING: "Rounding",
  FREE_GOODS: "Free goods",
};

/** A procedure name usable as a reference key, such as AX_STANDARD or SAP_RVAA01. */
export const PROCEDURE_NAME = /^[A-Z0-9][A-Z0-9_-]{0,63}$/;
