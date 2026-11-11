import type { OpenItem, OpenItemStatus, PaymentMethod, ProposalStatus, RunStatus } from "./api/client";

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

const decimalsByCurrency = new Map<string, number>();

function minorUnits(currency: string): number {
  let known = decimalsByCurrency.get(currency);
  if (known === undefined) {
    try {
      known = new Intl.NumberFormat("en", { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
    } catch {
      known = 2;
    }
    decimalsByCurrency.set(currency, known);
  }
  return known;
}

/** The amount with the currency's minor units and thousands separators, without the currency; null when it isn't a number. */
export function formatDecimal(value: string, currency: string): string | null {
  if (typeof value !== "string" || !/^-?\d+(\.\d*)?$/.test(value.trim())) return null;
  value = value.trim();
  const digits = minorUnits(currency);
  const negative = value.startsWith("-");
  const [whole, fraction = ""] = (negative ? value.slice(1) : value).split(".");
  const grouped = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  const extra = fraction.slice(digits).replace(/0+$/, "");
  const decimals = (fraction.slice(0, digits).padEnd(digits, "0") + extra);
  return `${negative ? "-" : ""}${grouped}${decimals === "" ? "" : `.${decimals}`}`;
}

export function formatAmount(value: string, currency: string): string {
  const amount = formatDecimal(value, currency);
  return amount === null ? "Not available" : `${amount} ${currency}`;
}

/** Adds decimal strings exactly (no floating point); a value that isn't a number counts as zero. */
export function sumDecimals(values: string[]): string {
  const clean = values.map((v) => (typeof v === "string" ? v.trim() : "")).filter((v) => /^-?\d+(\.\d*)?$/.test(v));
  const scale = Math.max(0, ...clean.map((v) => (v.split(".")[1] ?? "").length));
  let total = 0n;
  for (const v of clean) {
    const negative = v.startsWith("-");
    const [whole, fraction = ""] = (negative ? v.slice(1) : v).split(".");
    const units = BigInt(whole + fraction.padEnd(scale, "0"));
    total += negative ? -units : units;
  }
  const digits = (total < 0n ? -total : total).toString().padStart(scale + 1, "0");
  const text = scale === 0 ? digits : `${digits.slice(0, -scale)}.${digits.slice(-scale)}`;
  return `${total < 0n ? "-" : ""}${text}`;
}

/** Hands a downloaded file to the browser under the given name. */
export function saveBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

export function today(now: Date = new Date()): string {
  const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 10);
}

export function utcToday(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}

export function isOverdue(item: Pick<OpenItem, "dueDate" | "status">, day: string): boolean {
  return (item.status === "OPEN" || item.status === "PARTIALLY_SETTLED") && item.dueDate < day;
}

export function holdText(item: Pick<OpenItem, "paymentHoldReason">): string {
  return item.paymentHoldReason ? `On hold: ${item.paymentHoldReason}` : "On hold, no reason recorded";
}

export const RUN_LABELS: Record<RunStatus, string> = { RUNNING: "Running", COMPLETED: "Paid", FAILED: "Failed" };

/** The user ids the services record are not names; say "You" for the signed-in user and keep other ids out of sight. */
export function personLabel(id: string | undefined, me: string): string {
  if (!id) return "Not recorded";
  if (id === me) return "You";
  return /^[0-9a-f]{8}-[0-9a-f]{4}-/i.test(id) ? "Another user" : id;
}
