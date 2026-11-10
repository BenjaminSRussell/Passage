import type { OpenItem, OpenItemStatus, PaymentMethod, ProposalStatus } from "./api/client";

export const OPEN_ITEM_LABELS: Record<OpenItemStatus, string> = {
  OPEN: "Open",
  PARTIALLY_SETTLED: "Partly paid",
  SETTLED: "Paid",
  REVERSED: "Reversed",
};

export const PROPOSAL_LABELS: Record<ProposalStatus, string> = {
  DRAFT: "Draft",
  APPROVED: "Approved",
  CANCELLED: "Cancelled",
  EXECUTING: "Running",
  EXECUTED: "Paid",
  FAILED: "Failed",
};

export const METHOD_LABELS: Record<PaymentMethod, string> = {
  TRANSFER: "Bank transfer",
  ACH: "ACH",
  CHECK: "Check",
  CARD: "Card",
  WIRE: "Wire",
};

export const EXCLUDED_REASON = "The payee isn't verified yet. This expense reimbursement is left out until the employee is registered as a vendor.";

export function formatAmount(value: string, currency: string): string {
  const negative = value.startsWith("-");
  const [whole, fraction = ""] = (negative ? value.slice(1) : value).split(".");
  const grouped = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return `${negative ? "-" : ""}${grouped}.${fraction.padEnd(2, "0")} ${currency}`;
}

export function today(now: Date = new Date()): string {
  const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 10);
}

export function isOverdue(item: Pick<OpenItem, "dueDate" | "status">, day: string): boolean {
  return (item.status === "OPEN" || item.status === "PARTIALLY_SETTLED") && item.dueDate < day;
}

export function holdText(item: Pick<OpenItem, "paymentHoldReason">): string {
  return item.paymentHoldReason ? `On hold: ${item.paymentHoldReason}` : "On hold, no reason recorded";
}
