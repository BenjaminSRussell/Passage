import type { InvoiceStatus, RecognitionMethod, SspSource } from "./api/client";

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

export const METHOD_LABELS: Record<RecognitionMethod, string> = {
  POINT_IN_TIME: "Point in time",
  RATABLE: "Ratable",
  MILESTONE: "Milestone",
  USAGE: "Usage",
};

/** The invoice's name for people: its number once posted, otherwise "Proposal". */
export function invoiceName(inv: { number?: string; status: InvoiceStatus }): string {
  return inv.number ? inv.number : inv.status === "PROPOSAL" ? "Proposal" : "Invoice";
}

/** Today's calendar day in the browser's zone, as YYYY-MM-DD. */
export function today(now: Date = new Date()): string {
  const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 10);
}
