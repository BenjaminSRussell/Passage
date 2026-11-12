import { DECIMAL, UUID } from "../../shell/api/http";
import type { BadgeTone } from "../../shell/ui";
import type { CaptureStatus, InvoiceStatus, MatchStatus, REVIEW_REASONS, SubmissionStatus } from "./api/client";

export { DECIMAL, UUID };

export interface InvoiceCandidate {
  vendorId: string;
  currency: string;
  receiptId: string;
  receiptLineNo: number;
  qty: string;
  unitPrice: string;
}

export interface InvoiceNavState {
  items?: InvoiceCandidate[];
  /** Header fields a vendor already gave, such as an invoice submitted in the supplier portal. */
  header?: { legalEntityId?: string; invoiceNumber?: string; invoiceDate?: string };
}

export const invoiceStatusLabel: Record<InvoiceStatus, string> = {
  POSTED: "Posted",
  AUDIT_BLOCKED: "Held for audit",
  AUDIT_REJECTED: "Rejected after audit",
};

export const invoiceStatusTone: Record<InvoiceStatus, BadgeTone> = { POSTED: "success", AUDIT_BLOCKED: "warning", AUDIT_REJECTED: "danger" };

export const submissionStatusLabel: Record<SubmissionStatus, string> = {
  SUBMITTED: "Waiting for review",
  WITHDRAWN: "Withdrawn by the vendor",
  REJECTED: "Rejected",
  POSTED: "Posted as an invoice",
};

export const submissionStatusTone: Record<SubmissionStatus, BadgeTone> = { SUBMITTED: "info", WITHDRAWN: "neutral", REJECTED: "danger", POSTED: "success" };

export const captureStatusLabel: Record<CaptureStatus, string> = { CAPTURED: "Captured", NEEDS_REVIEW: "Needs review" };

export const captureStatusTone: Record<CaptureStatus, BadgeTone> = { CAPTURED: "info", NEEDS_REVIEW: "warning" };

export const reviewReasonLabel: Record<(typeof REVIEW_REASONS)[number], string> = {
  LOW_CONFIDENCE: "The document could not be read with certainty",
  TOTAL_MISMATCH: "The total differs from the sum of the lines",
  NO_VENDOR_TAX_ID: "No vendor tax number was found",
};

export const matchStatusLabel: Record<MatchStatus, string> = {
  MATCHED: "Matches the receipt",
  PRICE_VARIANCE: "Price is outside the standard tolerance",
  QTY_VARIANCE: "More than was received",
  NO_RECEIPT: "No receipt found",
  WAITING_RECEIPT: "The receipt is not posted yet",
};

export const matchStatusTone: Record<MatchStatus, BadgeTone> = { MATCHED: "success", PRICE_VARIANCE: "danger", QTY_VARIANCE: "danger", NO_RECEIPT: "warning", WAITING_RECEIPT: "warning" };

export function priceDifferenceText(amount: string, currency: string): string {
  if (/^-?0+(\.0+)?$/.test(amount)) return "None";
  return amount.startsWith("-") ? `${amount.slice(1)} ${currency} below the receipt price` : `${amount} ${currency} above the receipt price`;
}

const SCALE = 6;
const ONE = 10n ** BigInt(SCALE);

function toMicros(value: string): bigint {
  const [whole, fraction = ""] = value.split(".");
  return BigInt(whole) * ONE + BigInt(fraction.padEnd(SCALE, "0").slice(0, SCALE));
}

function fromMicros(micros: bigint): string {
  const whole = micros / ONE;
  const fraction = (micros % ONE).toString().padStart(SCALE, "0").replace(/0+$/, "");
  return fraction ? `${whole}.${fraction}` : String(whole);
}

/** Exact decimal subtraction in millionths, never floats; clamps at zero. */
export function subtractDecimal(a: string, b: string): string {
  const diff = toMicros(a) - toMicros(b);
  return fromMicros(diff < 0n ? 0n : diff);
}
