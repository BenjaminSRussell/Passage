/** Pure budget rules for display and input checks. No I/O, no React. */

export type CheckLineResult = "pass" | "warn" | "fail";
export type DocumentType = "Requisition" | "PurchaseOrder" | "Invoice" | "Journal";

export const DOCUMENT_TYPES: DocumentType[] = ["Requisition", "PurchaseOrder", "Invoice", "Journal"];

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const RULE_CODE = /^[A-Z0-9_-]{2,40}$/;
export const CURRENCY = /^[A-Z]{3}$/;
export const DAY = /^\d{4}-\d{2}-\d{2}$/;

/** Decimal string with at most 4 places. Money is never a JS number on the wire. */
const MONEY = /^-?\d+(\.\d{1,4})?$/;
const SCALE = 4n;

export function isMoney(value: string): boolean {
  return MONEY.test(value);
}

/** Decimal string to integer ten-thousandths, so sums never touch floating point. */
export function toUnits(value: string): bigint {
  const negative = value.startsWith("-");
  const [whole, frac = ""] = (negative ? value.slice(1) : value).split(".");
  const units = BigInt(whole || "0") * 10n ** SCALE + BigInt((frac + "0000").slice(0, Number(SCALE)));
  return negative ? -units : units;
}

/** Integer ten-thousandths back to a decimal string with `places` places (half away from zero). */
export function fromUnits(units: bigint, places = 2): string {
  const negative = units < 0n;
  let abs = negative ? -units : units;
  const drop = 10n ** (SCALE - BigInt(places));
  abs = (abs + drop / 2n) / drop;
  const unit = 10n ** BigInt(places);
  const whole = (abs / unit).toString();
  const frac = places > 0 ? "." + (abs % unit).toString().padStart(places, "0") : "";
  return (negative && abs !== 0n ? "-" : "") + whole + frac;
}

/** Shows a server decimal string with two places, or the raw text when it is not a decimal. */
export function formatAmount(value: string | undefined): string {
  if (value === undefined || value === "") return "—";
  return isMoney(value) ? fromUnits(toUnits(value)) : value;
}

export function sumAmounts(values: string[]): bigint {
  return values.filter(isMoney).reduce((acc, v) => acc + toUnits(v), 0n);
}

export function resultLabel(result: CheckLineResult): string {
  switch (result) {
    case "pass":
      return "Within budget";
    case "warn":
      return "Over threshold: warning, reserved";
    case "fail":
      return "Over budget: blocked";
  }
}

export function resultTone(result: CheckLineResult): "success" | "warning" | "danger" {
  return result === "pass" ? "success" : result === "warn" ? "warning" : "danger";
}

/** Pass and warn reserve the amount (FR-38.4 step 8); fail reserves nothing. */
export function reservesFunds(result: CheckLineResult): boolean {
  return result !== "fail";
}

export function overPercentLabel(overPct: string): string {
  const [whole, frac = ""] = overPct.split(".");
  const trimmed = frac ? `${whole}.${frac.slice(0, 4)}` : whole;
  return isMoney(trimmed) ? `${fromUnits(toUnits(trimmed))}%` : "n/a";
}

export interface CheckOutcomeLike {
  result: CheckLineResult;
  lines: { lineId: string; result: CheckLineResult }[];
}

/** Lines the buyer must change or override before the document can proceed. */
export function blockedLineIds(outcome: CheckOutcomeLike): string[] {
  return outcome.lines.filter((l) => l.result === "fail").map((l) => l.lineId);
}

export interface CheckRow {
  lineId: string;
  dimensionKey: string;
  spanKey: string;
  amount: string;
}

export type BuildResult<T> = { ok: true; value: T } | { ok: false; errors: string[] };

/** Validates the rows of a budget check form before the request is sent. */
export function validateCheckRows(rows: CheckRow[]): BuildResult<CheckRow[]> {
  const errors: string[] = [];
  if (rows.length === 0) errors.push("At least one line is required.");
  rows.forEach((row, i) => {
    const n = i + 1;
    if (row.dimensionKey.trim() === "") errors.push(`Line ${n}: dimension is required.`);
    if (row.spanKey.trim() === "") errors.push(`Line ${n}: time span is required.`);
    if (!isMoney(row.amount) || row.amount.startsWith("-")) errors.push(`Line ${n}: amount must be a positive decimal with at most 4 places.`);
  });
  return errors.length === 0 ? { ok: true, value: rows } : { ok: false, errors };
}

export interface RuleForm {
  legalEntityId: string;
  code: string;
  warnPct: string;
  blockPct: string;
  currency: string;
  effectiveFrom: string;
}

export type RuleErrors = Partial<Record<keyof RuleForm, string>>;

const PERCENT = /^\d{1,3}(\.\d{1,6})?$/;

/** Validates a control rule version the way the service does (FR-38.3). */
export function validateRule(f: RuleForm): RuleErrors {
  const errors: RuleErrors = {};
  if (!UUID.test(f.legalEntityId)) errors.legalEntityId = "Enter the legal entity's id (a UUID).";
  if (!RULE_CODE.test(f.code)) errors.code = "Use 2 to 40 capital letters, digits, _ or -, such as DEPT.";
  const warnOk = PERCENT.test(f.warnPct) && toUnits(f.warnPct) <= toUnits("100");
  const blockOk = PERCENT.test(f.blockPct) && toUnits(f.blockPct) <= toUnits("100");
  if (!warnOk) errors.warnPct = "Enter a percentage from 0 to 100.";
  if (!blockOk) errors.blockPct = "Enter a percentage from 0 to 100.";
  else if (warnOk && toUnits(f.blockPct) < toUnits(f.warnPct)) errors.blockPct = "The block threshold cannot be below the warn threshold.";
  if (!CURRENCY.test(f.currency)) errors.currency = "Use a three-letter ISO 4217 code, such as USD.";
  if (!DAY.test(f.effectiveFrom)) errors.effectiveFrom = "Enter the date the rule takes effect.";
  return errors;
}

/** Validates register entry lines; a transfer must net to zero (CO-0604). */
export function validateEntryLines(entryType: string, lines: { dimensionKey: string; spanKey: string; amount: string }[]): string[] {
  const errors: string[] = [];
  if (lines.length === 0) errors.push("At least one line is required.");
  lines.forEach((l, i) => {
    const n = i + 1;
    if (l.dimensionKey.trim() === "") errors.push(`Line ${n}: dimension is required.`);
    if (l.spanKey.trim() === "") errors.push(`Line ${n}: time span is required.`);
    if (!isMoney(l.amount)) errors.push(`Line ${n}: amount must be a decimal with at most 4 places.`);
  });
  if (errors.length === 0 && entryType === "Transfer") {
    const net = sumAmounts(lines.map((l) => l.amount));
    if (net !== 0n) errors.push(`A transfer must net to zero; these lines net to ${fromUnits(net, 4)}.`);
  }
  return errors;
}
