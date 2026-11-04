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

/** True for a real calendar date written YYYY-MM-DD (2026-02-30 is not one). */
export function isDate(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const d = new Date(`${value}T00:00:00Z`);
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === value;
}

export interface BehaviorRuleForm {
  legalEntityId: string;
  costElementId: string;
  objectKind: "" | "CostCenter" | "InternalOrder" | "Product" | "Project";
  behavior: "Fixed" | "Variable";
  effectiveFrom: string;
}

/** Field errors for a cost behavior rule (FR-38.16); the service checks the same rules. */
export function validateBehaviorRule(f: BehaviorRuleForm): Partial<Record<keyof BehaviorRuleForm, string>> {
  const errors: Partial<Record<keyof BehaviorRuleForm, string>> = {};
  if (!UUID.test(f.legalEntityId)) errors.legalEntityId = "Enter the legal entity's id (a UUID).";
  if (!UUID.test(f.costElementId)) errors.costElementId = "Choose the cost element the rule classifies.";
  if (!isDate(f.effectiveFrom)) errors.effectiveFrom = "Enter the first day the rule applies, as YYYY-MM-DD.";
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

/** Ticks (1/10000) to whole cents, half away from zero. */
function ticksToCents(t: bigint): bigint {
  const negative = t < 0n;
  const cents = ((negative ? -t : t) + 50n) / 100n;
  return negative ? -cents : cents;
}

function divRound(n: bigint, d: bigint): bigint {
  const q = (2n * n + d) / (2n * d);
  return n >= 0n ? q : -((2n * -n + d) / (2n * d));
}

export interface SharePreview {
  id: string;
  /** Decimal string with two places. */
  amount: string;
}

/**
 * Previews a top-down distribution the way the service computes it (BR-38.13): each share is
 * amount × basis / Σ basis rounded to cents, and the segment with the largest basis (the first
 * on a tie) takes the rounding residual, so the shares sum to the amount exactly. Zero-basis
 * segments get nothing. Returns null when the inputs are not valid amounts or no basis is positive.
 */
export function previewTopDown(amount: string, segments: { id: string; basis: string }[]): SharePreview[] | null {
  const total = toTicks(amount);
  if (total === null) return null;
  const cents = ticksToCents(total);
  const bases = segments.map((s) => toTicks(s.basis));
  if (bases.some((b) => b === null || b < 0n)) return null;
  const sum = (bases as bigint[]).reduce((a, b) => a + b, 0n);
  if (sum === 0n) return null;
  let largest = -1;
  (bases as bigint[]).forEach((b, i) => {
    if (b > 0n && (largest < 0 || b > (bases[largest] as bigint))) largest = i;
  });
  const out = (bases as bigint[]).map((b) => (b === 0n ? 0n : divRound(cents * b, sum)));
  const others = out.reduce((acc, v, i) => (i === largest ? acc : acc + v), 0n);
  out[largest] = cents - others;
  return segments.map((s, i) => ({ id: s.id, amount: formatAmount(formatTicks(out[i] * 100n)) }));
}

/** Validates a top-down run form before it is sent (FR-38.19). */
export function validateTopDown(f: { legalEntityId: string; period: string; referencePeriod: string; currency: string; sourceCostObjectId: string; costElementId: string; amount: string }, segments: { id: string; basis: string }[]): string[] {
  const errors: string[] = [];
  if (!UUID.test(f.legalEntityId)) errors.push("Enter the legal entity's id (a UUID).");
  if (!isPeriod(f.period)) errors.push("Enter the period as YYYY-MM.");
  if (f.referencePeriod !== "" && !isPeriod(f.referencePeriod)) errors.push("Enter the reference period as YYYY-MM, or leave it empty.");
  if (!CURRENCY.test(f.currency)) errors.push("Use a three-letter currency code.");
  if (!UUID.test(f.sourceCostObjectId)) errors.push("Choose the source cost object that holds the amount.");
  if (!UUID.test(f.costElementId)) errors.push("Choose the cost element to distribute.");
  const amount = toTicks(f.amount);
  if (amount === null || amount <= 0n || amount % 100n !== 0n) errors.push("Enter a positive amount with at most 2 decimal places.");
  if (segments.length === 0) errors.push("Choose at least one segment.");
  segments.forEach((s, i) => {
    const b = toTicks(s.basis);
    if (b === null || b < 0n) errors.push(`Segment ${i + 1}: basis must be a non-negative decimal with at most 4 places.`);
  });
  if (segments.length > 0 && segments.every((s) => toTicks(s.basis) === 0n)) errors.push("At least one segment needs a positive basis.");
  return errors;
}

/** BR-38.9: a settlement must be positive and cannot exceed the order's collected cost. */
export function settlementError(amount: string, collected: bigint | null): string | null {
  const t = toTicks(amount);
  if (t === null || t <= 0n || t % 100n !== 0n) return "Enter a positive amount with at most 2 decimal places.";
  if (collected !== null && t > collected) return `The settlement exceeds the collected cost of ${formatAmount(formatTicks(collected))}.`;
  return null;
}

export const MEASURE_CODE = /^[A-Za-z_][A-Za-z0-9_]{0,39}$/;
export const MAX_FORMULA_LENGTH = 500;

/**
 * The references a template formula reads: m.<measure code> and b.<cost element code>.
 * The service parses the full grammar at save; the page uses this to warn early about
 * names the legal entity does not have.
 */
export function formulaRefs(formula: string): { measures: string[]; elements: string[] } {
  const measures = new Set<string>();
  const elements = new Set<string>();
  for (const m of formula.matchAll(/(?<![A-Za-z0-9_.])([mb])\.([A-Za-z_][A-Za-z0-9_]*)/g)) {
    (m[1] === "m" ? measures : elements).add(m[2]);
  }
  return { measures: [...measures].sort(), elements: [...elements].sort() };
}

export interface TemplateForm {
  legalEntityId: string;
  code: string;
  name: string;
  currency: string;
  senderCostObjectId: string;
}

/** Checks a template before it is saved: shape, lines and known references (FR-38.17). */
export function validateTemplate(
  f: TemplateForm,
  lines: { receiverCostObjectId: string; secondaryElementId: string; formula: string }[],
  known: { measures: string[]; elements: string[] },
): string[] {
  const errors: string[] = [];
  if (!UUID.test(f.legalEntityId)) errors.push("Enter the legal entity's id (a UUID).");
  if (!CODE.test(f.code)) errors.push("Use 2 to 40 characters for the code: A-Z, 0-9, _ or -.");
  if (f.name.trim() === "" || f.name.length > 200) errors.push("Enter a name of up to 200 characters.");
  if (!CURRENCY.test(f.currency)) errors.push("Use a three-letter currency code.");
  if (!UUID.test(f.senderCostObjectId)) errors.push("Choose the sender cost object.");
  if (lines.length === 0) errors.push("Add at least one line.");
  const measures = new Set(known.measures);
  const elements = new Set(known.elements);
  lines.forEach((l, i) => {
    const n = i + 1;
    if (!UUID.test(l.receiverCostObjectId)) errors.push(`Line ${n}: choose a receiver.`);
    else if (l.receiverCostObjectId === f.senderCostObjectId) errors.push(`Line ${n}: the receiver must differ from the sender.`);
    if (!UUID.test(l.secondaryElementId)) errors.push(`Line ${n}: choose a secondary cost element.`);
    if (l.formula.trim() === "") errors.push(`Line ${n}: enter a formula.`);
    else if (l.formula.length > MAX_FORMULA_LENGTH) errors.push(`Line ${n}: keep the formula to ${MAX_FORMULA_LENGTH} characters.`);
    const refs = formulaRefs(l.formula);
    for (const m of refs.measures) if (!measures.has(m)) errors.push(`Line ${n}: m.${m} is not a statistical measure of this legal entity.`);
    for (const e of refs.elements) if (!elements.has(e)) errors.push(`Line ${n}: b.${e} is not a cost element of this legal entity.`);
  });
  return errors;
}

/** Validates one period's measure values: decimals of zero or more with up to 4 places; blank rows are skipped. */
export function validateMeasureValues(period: string, rows: { costObjectId: string; label?: string; value: string }[]): string[] {
  const errors: string[] = [];
  if (!isPeriod(period)) errors.push("Enter the period as YYYY-MM.");
  const filled = rows.filter((r) => r.value.trim() !== "");
  if (filled.length === 0) errors.push("Enter at least one value.");
  for (const r of filled) {
    const t = toTicks(r.value.trim());
    if (t === null || t < 0n) errors.push(`${r.label ?? r.costObjectId}: enter zero or more with up to 4 decimals.`);
  }
  return errors;
}

/** One node of a cost object hierarchy with its depth below the root (0 for a root). */
export interface TreeRow<T> {
  node: T;
  depth: number;
}

/**
 * Orders hierarchy nodes depth first, each parent before its children and siblings in the
 * order given. A node whose parent is not in the list is shown as a root.
 */
export function nodeTree<T extends { id: string; parentNodeId?: string }>(nodes: T[]): TreeRow<T>[] {
  const ids = new Set(nodes.map((n) => n.id));
  const children = new Map<string, T[]>();
  const roots: T[] = [];
  for (const n of nodes) {
    if (n.parentNodeId && ids.has(n.parentNodeId) && n.parentNodeId !== n.id) {
      children.set(n.parentNodeId, [...(children.get(n.parentNodeId) ?? []), n]);
    } else {
      roots.push(n);
    }
  }
  const out: TreeRow<T>[] = [];
  const seen = new Set<string>();
  const walk = (n: T, depth: number) => {
    if (seen.has(n.id)) return;
    seen.add(n.id);
    out.push({ node: n, depth });
    for (const c of children.get(n.id) ?? []) walk(c, depth + 1);
  };
  roots.forEach((r) => walk(r, 0));
  return out;
}

/** A collection is non-zero with at most 2 decimal places; a negative amount corrects one. */
export function collectError(amount: string): string | null {
  const t = toTicks(amount);
  if (t === null || t === 0n || t % 100n !== 0n) return "Enter a non-zero amount with at most 2 decimal places; a negative amount corrects a collection.";
  return null;
}

export interface HierarchyPreview {
  shares: string[];
  residual: string;
}

/**
 * Previews a hierarchy distribution the way the service computes it (FR-38.18). ActualOutput and
 * StandardCost follow the top-down rule (rounding residue to the largest basis; nothing moves when
 * every basis is zero). FixedPercent truncates each share to the cent and gives the residue to the
 * largest percentage only when the percentages total 100; otherwise the rest stays on the node.
 * Returns null for invalid inputs or percentages over 100.
 */
export function previewHierarchy(amount: string, basis: string, children: { id: string; basis: string }[]): HierarchyPreview | null {
  const total = toTicks(amount);
  if (total === null || total <= 0n || children.length === 0) return null;
  const cents = ticksToCents(total);
  const bases = children.map((c) => toTicks(c.basis));
  if (bases.some((b) => b === null || b < 0n)) return null;
  const b = bases as bigint[];
  const money = (c: bigint) => formatAmount(formatTicks(c * 100n));
  let shares: bigint[];
  if (basis === "FixedPercent") {
    const hundred = 100n * 10000n;
    const sum = b.reduce((a, v) => a + v, 0n);
    if (b.some((v) => v > hundred) || sum > hundred) return null;
    shares = b.map((v) => (cents * v) / hundred);
    if (sum === hundred) {
      let largest = 0;
      b.forEach((v, i) => {
        if (v > b[largest]) largest = i;
      });
      shares[largest] += cents - shares.reduce((a, v) => a + v, 0n);
    }
  } else {
    const proportional = previewTopDown(formatTicks(cents * 100n), children);
    shares = proportional ? proportional.map((s) => ticksToCents(toTicks(s.amount) ?? 0n)) : b.map(() => 0n);
  }
  const given = shares.reduce((a, v) => a + v, 0n);
  return { shares: shares.map(money), residual: money(cents - given) };
}

/** Validates a distribution run form before it is sent (FR-38.18). */
export function validateDistribution(f: { period: string; currency: string; costElementId: string; basis: string }, children: { id: string; basis: string }[]): string[] {
  const errors: string[] = [];
  if (!isPeriod(f.period)) errors.push("Enter the period as YYYY-MM.");
  if (!CURRENCY.test(f.currency)) errors.push("Use a three-letter currency code.");
  if (!UUID.test(f.costElementId)) errors.push("Choose the cost element to distribute.");
  if (children.length === 0) errors.push("Choose at least one child node to receive the cost.");
  children.forEach((c, i) => {
    const t = toTicks(c.basis);
    if (t === null || t < 0n) errors.push(`Child ${i + 1}: the basis must be a non-negative decimal with at most 4 places.`);
  });
  if (f.basis === "FixedPercent") {
    const sum = sumTicks(children.map((c) => c.basis));
    if (sum !== null && sum > 100n * 10000n) errors.push("The percentages total more than 100.");
  }
  return errors;
}
