// Field checks shared by the slice 2 forms. They mirror the service limits in
// services/scheduling/internal/api so most mistakes are caught before a call.

/** Setup groups and codes: 1 to 32 capital letters, digits, _ or -. */
export const CODE = /^[A-Z0-9][A-Z0-9_-]{0,31}$/;
export const CURRENCY = /^[A-Z]{3}$/;
export const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Minutes in a week, the service cap on transfer, wait and changeover minutes. */
export const WEEK_MINUTES = 10_080;

/** A whole number in [min, max], or null. */
export function wholeNumber(value: string, min: number, max: number): number | null {
  const n = Number(value);
  return value.trim() !== "" && Number.isInteger(n) && n >= min && n <= max ? n : null;
}

/** An optional whole number: undefined when blank, null when invalid. */
export function optionalWhole(value: string, min: number, max: number): number | undefined | null {
  return value.trim() === "" ? undefined : wholeNumber(value, min, max);
}

/**
 * A non-negative decimal kept as text so no binary float touches money or
 * quantities: at most `integers` digits before the point and `scale` after.
 */
export function decimalText(value: string, scale: number, integers = 12): string | null {
  const v = value.trim();
  const pattern = new RegExp(`^\\d{1,${integers}}(\\.\\d{1,${scale}})?$`);
  return pattern.test(v) ? v : null;
}

/** Drops trailing zeros and a bare point: "100.0000" becomes "100". */
export function trimDecimal(value: string): string {
  const v = value.trim();
  return v.includes(".") ? v.replace(/0+$/, "").replace(/\.$/, "") : v;
}

/** Compares two non-negative decimal strings as text, without a float: -1, 0 or 1. */
export function compareDecimal(a: string, b: string): number {
  const [ai = "0", af = ""] = trimDecimal(a).split(".");
  const [bi = "0", bf = ""] = trimDecimal(b).split(".");
  const scale = Math.max(af.length, bf.length);
  const x = (ai.replace(/^0+(?=\d)/, "") + af.padEnd(scale, "0")).padStart(40, "0");
  const y = (bi.replace(/^0+(?=\d)/, "") + bf.padEnd(scale, "0")).padStart(40, "0");
  return x < y ? -1 : x > y ? 1 : 0;
}

/** True when a decimal string is greater than zero. */
export function positive(value: string): boolean {
  return /[1-9]/.test(value);
}

/** Copies the service's field errors onto form fields by name. */
export function mapFields<K extends string>(fields: Record<string, string>, names: Record<string, K>): Partial<Record<K, string>> {
  const out: Partial<Record<K, string>> = {};
  for (const [name, message] of Object.entries(fields)) {
    const field = names[name];
    if (field) out[field] = message;
  }
  return out;
}
