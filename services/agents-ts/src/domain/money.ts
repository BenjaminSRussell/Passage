/** Money as a decimal string plus currency (CONVENTIONS: never float). */
export interface Money {
  amount: string;
  currency: string;
}

const DECIMAL = /^-?\d+(\.\d{1,6})?$/;

/** Scales a decimal string to millionths so comparisons are exact. */
export function scaled6(amount: string): bigint {
  if (!DECIMAL.test(amount)) throw new Error(`invalid decimal amount ${amount}`);
  const negative = amount.startsWith("-");
  const body = negative ? amount.slice(1) : amount;
  const [whole = "0", fraction = ""] = body.split(".");
  const value = BigInt(whole) * 1_000_000n + BigInt(fraction.padEnd(6, "0"));
  return negative ? -value : value;
}

/** Exact comparison; amounts in different currencies are a programming error. */
export function compareMoney(a: Money, b: Money): number {
  if (a.currency !== b.currency) throw new Error(`currency mismatch ${a.currency} vs ${b.currency}`);
  const x = scaled6(a.amount);
  const y = scaled6(b.amount);
  return x < y ? -1 : x > y ? 1 : 0;
}

/** Formats millionths back to a decimal string with six places. */
export function fromScaled6(value: bigint): string {
  const negative = value < 0n;
  const abs = negative ? -value : value;
  const whole = abs / 1_000_000n;
  const fraction = (abs % 1_000_000n).toString().padStart(6, "0");
  return `${negative ? "-" : ""}${whole}.${fraction}`;
}

/** Exact sum of amounts in one currency. */
export function sumMoney(currency: string, items: readonly Money[]): Money {
  let total = 0n;
  for (const m of items) {
    if (m.currency !== currency) throw new Error(`currency mismatch ${m.currency} vs ${currency}`);
    total += scaled6(m.amount);
  }
  return { amount: fromScaled6(total), currency };
}
