/**
 * Exact decimal helpers for drill-down (BR-15.4). Amounts arrive as decimal
 * strings and are summed as integer micro-units so no float ever touches money.
 */

const SCALE = 6;
const FACTOR = BigInt(1_000_000);

/** Parses a decimal string such as "-1234.5" into micro-units. */
export function toMicros(value: string): bigint {
  const m = /^(-?)(\d+)(?:\.(\d+))?$/.exec(value.trim());
  if (!m) {
    throw new Error(`not a decimal amount: ${value}`);
  }
  const [, sign, whole, frac = ""] = m;
  if (frac.length > SCALE) {
    throw new Error(`more than ${SCALE} decimal places: ${value}`);
  }
  const micros = BigInt(whole) * FACTOR + BigInt(frac.padEnd(SCALE, "0"));
  return sign === "-" ? -micros : micros;
}

/** Formats micro-units back to a fixed six-place decimal string. */
export function fromMicros(micros: bigint): string {
  const negative = micros < BigInt(0);
  const abs = negative ? -micros : micros;
  const whole = abs / FACTOR;
  const frac = (abs % FACTOR).toString().padStart(SCALE, "0");
  return `${negative ? "-" : ""}${whole.toString()}.${frac}`;
}

export interface DrillCheck {
  ok: boolean;
  sum: string;
  /** cell value minus sum of keys; zero when ok. */
  difference: string;
}

/** Compares a cell value with the sum of its balance keys. */
export function checkDrill(value: string, keys: { amount: string }[]): DrillCheck {
  const sum = keys.reduce((acc, k) => acc + toMicros(k.amount), BigInt(0));
  const diff = toMicros(value) - sum;
  return { ok: diff === BigInt(0), sum: fromMicros(sum), difference: fromMicros(diff) };
}

/**
 * Formats a decimal string for a statement: rounded half away from zero to
 * `places` decimals, with thousands separators. Exact (no float).
 */
export function displayAmount(value: string, places = 2): string {
  const micros = toMicros(value);
  const negative = micros < BigInt(0);
  const abs = negative ? -micros : micros;
  const unit = BigInt(10) ** BigInt(SCALE - places);
  const rounded = (abs + unit / BigInt(2)) / unit;
  const scale = BigInt(10) ** BigInt(places);
  const whole = (rounded / scale).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  const frac = places > 0 ? `.${(rounded % scale).toString().padStart(places, "0")}` : "";
  return `${negative && rounded !== BigInt(0) ? "-" : ""}${whole}${frac}`;
}
