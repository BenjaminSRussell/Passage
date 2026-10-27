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

export type ReservationTypeName = "FundsReservation" | "Precommitment" | "FundsBlocking" | "RevenueForecast";
export type ReservationStatusName = "Open" | "PartiallyConsumed" | "Consumed" | "Closed" | "Expired";

export const RESERVATION_TYPES: { value: ReservationTypeName; label: string; hint: string }[] = [
  { value: "FundsReservation", label: "Funds reservation", hint: "Earmarks funds that later requisitions, POs, invoices or journals consume." },
  { value: "Precommitment", label: "Precommitment", hint: "Earmarks funds for a planned purchase; consumed like a funds reservation." },
  { value: "FundsBlocking", label: "Funds blocking", hint: "Blocks funds so nothing can use them. It cannot be consumed and does not expire." },
  { value: "RevenueForecast", label: "Revenue forecast", hint: "Records expected revenue. It holds no funds." },
];

export function reservationTypeLabel(t: string): string {
  return RESERVATION_TYPES.find((r) => r.value === t)?.label ?? t;
}

/** "PartiallyConsumed" as "Partially consumed"; other statuses read as they are. */
export function reservationStatusLabel(s: string): string {
  return s === "PartiallyConsumed" ? "Partially consumed" : s;
}

const OPEN_STATUSES = new Set<string>(["Open", "PartiallyConsumed"]);

/** Only funds reservations and precommitments that are still open can be drawn on (BR-38.14). */
export function canConsume(type: string, status: string): boolean {
  return (type === "FundsReservation" || type === "Precommitment") && OPEN_STATUSES.has(status);
}

/** Every type closes from Open; consumable types also once partly or fully consumed. Closed and Expired are final. */
export function canClose(type: string, status: string): boolean {
  if (status === "Open") return true;
  return (status === "PartiallyConsumed" || status === "Consumed") && (type === "FundsReservation" || type === "Precommitment");
}

export interface ReservationForm {
  ruleVersionId: string;
  type: ReservationTypeName;
  description: string;
  expiryDate: string;
}

/** Validates a reservation document the way the service does (FR-38.20). */
export function validateReservation(f: ReservationForm, lines: { dimensionKey: string; spanKey: string; amount: string }[]): string[] {
  const errors: string[] = [];
  if (!UUID.test(f.ruleVersionId)) errors.push("Enter the budget control rule version id (a UUID).");
  if (f.description.length > 500) errors.push("The description is at most 500 characters.");
  if (f.expiryDate !== "") {
    if (!DAY.test(f.expiryDate)) errors.push("Enter the expiry date as YYYY-MM-DD.");
    else if (f.type === "FundsBlocking") errors.push("Funds blocking does not expire; leave the expiry date empty.");
  }
  if (lines.length === 0) errors.push("At least one line is required.");
  lines.forEach((l, i) => {
    const n = i + 1;
    if (l.dimensionKey.trim() === "") errors.push(`Line ${n}: dimension is required.`);
    if (l.spanKey.trim() === "") errors.push(`Line ${n}: time span is required.`);
    if (!isMoney(l.amount) || toUnits(l.amount) <= 0n) errors.push(`Line ${n}: amount must be a positive decimal with at most 4 places.`);
  });
  return errors;
}

export const POSITION_CODE = /^[A-Z0-9_.-]{1,40}$/;
export const MAX_PROGRAM_LEVELS = 6;
export const POSITION_CATEGORIES = ["Capex", "Opex-related", "Contingency"] as const;
export const ASSIGNED_OBJECT_TYPES: { value: "CapexRequest" | "AssetUnderConstruction" | "Project"; label: string }[] = [
  { value: "CapexRequest", label: "Capex request" },
  { value: "AssetUnderConstruction", label: "Asset under construction" },
  { value: "Project", label: "Project" },
];

/** A non-negative decimal string with at most 4 places. */
function isAmount(value: string): boolean {
  return isMoney(value) && !value.startsWith("-");
}

export interface ProgramForm {
  legalEntityId: string;
  code: string;
  name: string;
  approvalYear: string;
  currency: string;
  budget: string;
}

export function validateProgram(f: ProgramForm): Partial<Record<keyof ProgramForm, string>> {
  const errors: Partial<Record<keyof ProgramForm, string>> = {};
  if (!UUID.test(f.legalEntityId)) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!RULE_CODE.test(f.code)) errors.code = "Use 2 to 40 characters: A-Z, 0-9, _ or -.";
  if (f.name.length > 200) errors.name = "Keep the name to 200 characters.";
  const year = Number(f.approvalYear);
  if (!/^\d{4}$/.test(f.approvalYear) || year < 1900 || year > 2999) errors.approvalYear = "Enter a four-digit year.";
  if (!CURRENCY.test(f.currency)) errors.currency = "Use a three-letter currency code.";
  if (!isAmount(f.budget)) errors.budget = "Enter an amount of zero or more with up to 4 decimals.";
  return errors;
}

export interface AllocationRow {
  positionCode: string;
  category: string;
  budget: string;
}

/**
 * Checks a distribution on the page before it is sent: codes, amounts, no
 * duplicates, and Σ children within the parent's budget (BR-38.15). Existing
 * children not in the rows keep their budget and count towards the total.
 */
export function validateDistribution(parentBudget: string, existing: { code: string; budget: string }[], rows: AllocationRow[]): string[] {
  const errors: string[] = [];
  if (rows.length === 0) errors.push("Add at least one position.");
  const seen = new Set<string>();
  rows.forEach((r, i) => {
    const n = i + 1;
    if (!POSITION_CODE.test(r.positionCode)) errors.push(`Row ${n}: the position code uses 1 to 40 characters of A-Z, 0-9, _, . or -.`);
    else if (seen.has(r.positionCode)) errors.push(`Row ${n}: ${r.positionCode} appears twice.`);
    seen.add(r.positionCode);
    if (!isAmount(r.budget)) errors.push(`Row ${n}: the budget is an amount of zero or more with up to 4 decimals.`);
  });
  if (errors.length > 0 || !isMoney(parentBudget)) return errors;
  const kept = existing.filter((e) => !seen.has(e.code) && isMoney(e.budget)).reduce((acc, e) => acc + toUnits(e.budget), 0n);
  const total = kept + rows.reduce((acc, r) => acc + toUnits(r.budget), 0n);
  const over = total - toUnits(parentBudget);
  if (over > 0n) errors.push(`The positions would exceed the parent budget by ${fromUnits(over)} (CO-0641).`);
  return errors;
}

export interface AssignForm {
  positionId: string;
  objectType: string;
  objectRef: string;
  plan: string;
  actual: string;
  commitment: string;
}

export function validateAssignment(f: AssignForm): string[] {
  const errors: string[] = [];
  if (!UUID.test(f.positionId)) errors.push("Choose a position.");
  if (!ASSIGNED_OBJECT_TYPES.some((t) => t.value === f.objectType)) errors.push("Choose what is assigned.");
  if (f.objectRef.trim() === "" || f.objectRef.length > 80) errors.push("Enter the object's reference (1 to 80 characters).");
  for (const [label, v] of [["Plan", f.plan], ["Actual", f.actual], ["Commitment", f.commitment]] as const) {
    if (!isAmount(v)) errors.push(`${label} is an amount of zero or more with up to 4 decimals.`);
  }
  return errors;
}

/** Positions in tree order (each parent followed by its children, by code) with their depth. */
export function positionTree<T extends { id: string; parentId?: string; code: string }>(positions: T[]): { position: T; depth: number }[] {
  const children = new Map<string, T[]>();
  for (const p of positions) {
    const key = p.parentId ?? "";
    children.set(key, [...(children.get(key) ?? []), p]);
  }
  for (const list of children.values()) list.sort((a, b) => a.code.localeCompare(b.code));
  const out: { position: T; depth: number }[] = [];
  const ids = new Set(positions.map((p) => p.id));
  const walk = (parent: string, depth: number) => {
    for (const p of children.get(parent) ?? []) {
      out.push({ position: p, depth });
      walk(p.id, depth + 1);
    }
  };
  walk("", 0);
  // Positions whose parent is missing from the list are shown at the top level.
  for (const p of positions) if (p.parentId && !ids.has(p.parentId) && !out.some((o) => o.position.id === p.id)) out.push({ position: p, depth: 0 });
  return out;
}
