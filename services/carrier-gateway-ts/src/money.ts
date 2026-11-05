// Fixed-point helpers: the gateway never does money or weight arithmetic in floating point.

const DECIMAL = /^(0|[1-9]\d{0,6})(?:\.(\d{1,3}))?$/;

/** Parses a non-negative decimal string with at most `places` (<= 3) fractional digits into an integer of 10^places units. */
export function parseFixed(value: unknown, places: number): number | null {
  if (typeof value !== "string") return null;
  const m = DECIMAL.exec(value);
  if (!m) return null;
  const frac = m[2] ?? "";
  if (frac.length > places) return null;
  return Number(m[1]) * 10 ** places + Number(frac.padEnd(places, "0"));
}

/** Formats an integer of 10^places units as a decimal string with exactly `places` fractional digits. */
export function formatFixed(units: number, places: number): string {
  const scale = 10 ** places;
  const whole = Math.floor(units / scale);
  const frac = String(units % scale).padStart(places, "0");
  return places === 0 ? String(whole) : `${whole}.${frac}`;
}

/** Integer division rounding up, for non-negative integers. */
export function ceilDiv(a: number, b: number): number {
  return Math.floor((a + b - 1) / b);
}
