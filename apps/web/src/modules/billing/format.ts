import type { ContractStatus, InvoiceStatus, RatableBasis, RecognitionMethod, ScheduleStatus, SspSource } from "./api/client";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DAY = /^\d{4}-\d{2}-\d{2}$/;
/** Mirrors the services: up to 14 integer and 6 fraction digits, no sign or exponent. */
export const DECIMAL = /^\d{1,14}(\.\d{1,6})?$/;
export const CURRENCY = /^[A-Z]{3}$/;
export const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;
export const TAX_CODE = /^[A-Z0-9_-]{1,20}$/;

export const STATUS_LABELS: Record<InvoiceStatus, string> = {
  PROPOSAL: "Proposal",
  POSTED: "Posted",
  PARTIALLY_SETTLED: "Partially settled",
  SETTLED: "Settled",
  REVERSED: "Reversed",
};

export const TYPE_LABELS: Record<string, string> = {
  SHIPMENT: "Shipment",
  CONSOLIDATED: "Consolidated",
  FREE_TEXT: "Free text",
  SUBSCRIPTION: "Subscription",
  MILESTONE: "Milestone",
  CREDIT_NOTE: "Credit note",
};

export const SSP_SOURCE_LABELS: Record<SspSource, string> = {
  EXPLICIT: "Explicit",
  LIST: "List price",
  PCT_OF_LIST: "Percent of list",
  RESIDUAL: "Residual",
};

/** The service derives a RESIDUAL SSP, so the forms do not ask for it. */
export const RESIDUAL_SSP_HINT = "The standalone selling price is the contract price less the other obligations' prices.";

export const PERIOD = /^\d{4}-(0[1-9]|1[0-2])$/;

export const CONTRACT_STATUS_LABELS: Record<ContractStatus, string> = {
  OPEN: "Open",
  COMPLETE: "Complete",
  CANCELLED: "Cancelled",
};

export const SCHEDULE_STATUS_LABELS: Record<ScheduleStatus, string> = {
  SCHEDULED: "Scheduled",
  RECOGNIZED: "Recognized",
  REVERSED: "Reversed",
};

/** The month before today's, as YYYY-MM: the period a controller usually closes. */
export function lastMonth(now: Date = new Date()): string {
  const d = new Date(now.getFullYear(), now.getMonth() - 1, 1);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
}

export const BASIS_LABELS: Record<RatableBasis, string> = {
  DAILY: "Daily (by the days of each month)",
  MONTHLY: "Monthly (equal per month)",
};

/** The field hint under a ratable basis choice. */
export const BASIS_HINT = "How ratable obligations spread over their term. Daily weighs a partial month by its days; monthly gives every month the term touches the same share.";

export const METHOD_LABELS: Record<RecognitionMethod, string> = {
  POINT_IN_TIME: "Point in time",
  RATABLE: "Ratable",
  MILESTONE: "Milestone",
  USAGE: "Usage",
};

/** The document's name for people: its number once posted, otherwise "Proposal" or "Credit note proposal". */
export function invoiceName(inv: { number?: string; status: InvoiceStatus; type?: string }): string {
  if (inv.number) return inv.number;
  const creditNote = inv.type === "CREDIT_NOTE";
  if (inv.status === "PROPOSAL") return creditNote ? "Credit note proposal" : "Proposal";
  return creditNote ? "Credit note" : "Invoice";
}

/** Today's calendar day in the browser's zone, as YYYY-MM-DD. */
export function today(now: Date = new Date()): string {
  const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 10);
}
