import { ApiError } from "./api/client";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DECIMAL = /^\d+(\.\d{1,6})?$/;

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

/** amount / qty to six places, rounded half up; empty when the quantity is zero. */
export function unitPriceOf(amount: string, qty: string): string {
  const q = toMicros(qty);
  if (q === 0n) return "";
  return fromMicros((toMicros(amount) * ONE * 2n + q) / (q * 2n));
}

export function describeFailure(error: unknown): string {
  return error instanceof ApiError ? `${error.message} (${error.code})` : (error as Error).message;
}

export function fieldErrors(error: unknown): Record<string, string> {
  return error instanceof ApiError ? error.fields : {};
}
