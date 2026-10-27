/**
 * Pure cost accounting rules for display and input checks. No I/O, no React.
 * Money stays a decimal string on the wire; arithmetic uses BigInt ticks of 1/10000
 * so that sums and conservation checks are exact (never float).
 */

const MONEY = /^-?\d+(\.\d{1,4})?$/;
const PERIOD = /^\d{4}-(0[1-9]|1[0-2])$/;

/** Parses a decimal string with at most 4 places into ticks of 1/10000, or null. */
export function toTicks(value: string): bigint | null {
  if (!MONEY.test(value)) return null;
  const negative = value.startsWith("-");
  const body = negative ? value.slice(1) : value;
  const [whole, fraction = ""] = body.split(".");
  const ticks = BigInt(whole) * 10000n + BigInt(fraction.padEnd(4, "0"));
  return negative ? -ticks : ticks;
}

/** Formats ticks as a decimal string with four places (exact, no float). */
export function formatTicks(ticks: bigint): string {
  const sign = ticks < 0n ? "-" : "";
  const abs = ticks < 0n ? -ticks : ticks;
  return `${sign}${abs / 10000n}.${String(abs % 10000n).padStart(4, "0")}`;
}

/** Exact sum of decimal strings, or null when any value is not a valid amount. */
export function sumTicks(values: string[]): bigint | null {
  let total = 0n;
  for (const v of values) {
    const t = toTicks(v);
    if (t === null) return null;
    total += t;
  }
  return total;
}

/** Net postings of an allocation step must sum to zero to the cent (BR-38.6). */
export function netPostingsBalanced(postings: string[]): boolean {
  return sumTicks(postings) === 0n;
}

export function isPeriod(value: string): boolean {
  return PERIOD.test(value);
}

/** Shows a server decimal string with two places (half away from zero), or the raw text. */
export function formatAmount(value: string | undefined): string {
  if (value === undefined || value === "") return "—";
  const t = toTicks(value);
  if (t === null) return value;
  const negative = t < 0n;
  const cents = ((negative ? -t : t) + 50n) / 100n;
  const text = `${cents / 100n}.${String(cents % 100n).padStart(2, "0")}`;
  return negative && cents !== 0n ? `-${text}` : text;
}

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const CODE = /^[A-Z0-9_-]{2,40}$/;
export const CURRENCY = /^[A-Z]{3}$/;

export interface CodeForm {
  legalEntityId: string;
  code: string;
  name: string;
}

/** Field errors shared by cost objects and cost elements (FR-38.6). */
export function validateCodeForm(f: CodeForm): Partial<Record<keyof CodeForm, string>> {
  const errors: Partial<Record<keyof CodeForm, string>> = {};
  if (!UUID.test(f.legalEntityId)) errors.legalEntityId = "Enter the legal entity's id (a UUID).";
  if (!CODE.test(f.code)) errors.code = "Use 2 to 40 capital letters, digits, _ or -, such as CC-100.";
  if (f.name.trim() === "" || f.name.length > 200) errors.name = "Enter a name of at most 200 characters.";
  return errors;
}

/** A primary element maps an inclusive main account range; From must not sort after To. */
export function validateAccountRange(from: string, to: string): string | null {
  if (from === "" || to === "") return "A primary element needs both main account bounds.";
  if (from.length > 40 || to.length > 40) return "Main accounts are at most 40 characters.";
  if (from > to) return `The range ${from}..${to} is reversed.`;
  return null;
}

/**
 * Builds the basis matrix from the grid the user typed: blank cells are 0 and
 * the diagonal is ignored. Every value must be a non-negative decimal.
 */
export function buildBasis(grid: string[][]): { ok: true; value: string[][] } | { ok: false; error: string } {
  const n = grid.length;
  const rows: string[][] = [];
  for (let j = 0; j < n; j++) {
    if (grid[j].length !== n) return { ok: false, error: `Each basis row must have ${n} entries.` };
    const row: string[] = [];
    for (let i = 0; i < n; i++) {
      const cell = i === j ? "0" : grid[j][i].trim() || "0";
      const t = toTicks(cell);
      if (t === null || t < 0n) return { ok: false, error: `Basis "${cell}" in row ${j + 1} must be a non-negative number.` };
      row.push(cell);
    }
    rows.push(row);
  }
  return { ok: true, value: rows };
}

/** Parses "mainAccount,amount" lines (an optional header row is skipped) for a reconciliation. */
export function parseJournalCsv(text: string): { ok: true; value: { mainAccount: string; amount: string }[] } | { ok: false; error: string } {
  const out: { mainAccount: string; amount: string }[] = [];
  const lines = text.split(/\r?\n/).map((l) => l.trim()).filter((l) => l !== "");
  for (const [i, line] of lines.entries()) {
    const [account = "", amount = "", ...rest] = line.split(",").map((c) => c.trim());
    if (i === 0 && toTicks(amount) === null && /account/i.test(account)) continue;
    if (rest.length > 0 || account === "" || toTicks(amount) === null) {
      return { ok: false, error: `Line ${i + 1} must be "mainAccount,amount" with a decimal amount.` };
    }
    out.push({ mainAccount: account, amount });
  }
  return { ok: true, value: out };
}
