import { DECIMAL, UUID } from "../../shell/api/http";
import type { BadgeTone } from "../../shell/ui";
import type { InvoiceStatus } from "./api/client";

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
}

export const invoiceStatusLabel: Record<InvoiceStatus, string> = {
  POSTED: "Posted",
  AUDIT_BLOCKED: "Held for audit",
  AUDIT_REJECTED: "Rejected after audit",
};

export const invoiceStatusTone: Record<InvoiceStatus, BadgeTone> = { POSTED: "success", AUDIT_BLOCKED: "warning", AUDIT_REJECTED: "danger" };

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
