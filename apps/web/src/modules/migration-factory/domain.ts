/** Pure helpers for the migration-factory screens. Mirrors tools/migration-factory/internal/domain. */
import type { OpenItemRecord } from "./api/client";

/** The nine transactional packs (FR-49.1). */
export const PACKS = [
  "open_items",
  "gl_balances",
  "onhand",
  "open_orders",
  "wip_prod",
  "fixed_assets",
  "project_wip",
  "payroll_ytd",
  "bank_balances",
] as const;
export type Pack = (typeof PACKS)[number];

/** Packs the slice-1 API runs. */
export const AVAILABLE_PACKS: readonly Pack[] = ["open_items"];

export const SOURCE_SYSTEMS = ["AX2012", "D365FO", "SAPECC", "S4HANA"] as const;
export type SourceSystem = (typeof SOURCE_SYSTEMS)[number];

export const SOURCE_LABELS: Record<SourceSystem, string> = {
  AX2012: "AX 2012",
  D365FO: "D365 F&O",
  SAPECC: "SAP ECC",
  S4HANA: "SAP S/4HANA",
};

export const RUN_STATUSES = ["EXTRACTED", "VALIDATED", "LOADED", "RECONCILED", "FAILED", "ROLLED_BACK"] as const;
export type RunStatus = (typeof RUN_STATUSES)[number];
export type ControlStatus = "MATCH" | "WITHIN_TOLERANCE" | "MISMATCH";

export interface ControlTotal {
  name: string;
  key: Record<string, string>;
  source: string;
  target: string;
  delta: string;
  tolerance?: string;
  toleranceReason?: string;
  status: ControlStatus;
}

export type Tone = "ok" | "amber" | "red";

/** Runbook overrun tone: amber at 110 %, red at 125 % by default (9.49.13 parameters). */
export function overrunTone(planned: number, actual: number, amberPct = 110, redPct = 125): Tone {
  if (planned <= 0) return "ok";
  const pct = Math.floor((actual * 100) / planned);
  if (pct >= redPct) return "red";
  if (pct >= amberPct) return "amber";
  return "ok";
}

export const toneLabel: Record<Tone, string> = { ok: "On plan", amber: "Amber", red: "Red" };

/** A run may reach RECONCILED only when no control total is MISMATCH (9.49.4 invariant). */
export function canReconcile(totals: ControlTotal[]): boolean {
  return totals.every((t) => t.status !== "MISMATCH");
}

/** Formats a minute offset from T-0 as T-14d, T+30h, T+45m or T-0. */
export function formatOffset(minutes: number): string {
  if (minutes === 0) return "T-0";
  const sign = minutes < 0 ? "-" : "+";
  const abs = Math.abs(minutes);
  if (abs % 1440 === 0) return `T${sign}${abs / 1440}d`;
  if (abs % 60 === 0) return `T${sign}${abs / 60}h`;
  return `T${sign}${abs}m`;
}

/** Renders a control-total key as "company=USMF, currency=USD". */
export function formatKey(key: Record<string, string>): string {
  return Object.entries(key)
    .map(([k, v]) => `${k}=${v}`)
    .join(", ");
}

export const dash = "—";

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** A datetime-local value ("2026-12-31T18:00") read as UTC, as an RFC 3339 string; null when invalid. */
export function localToUtc(value: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return null;
  const ms = Date.parse(`${value}:00Z`);
  return Number.isNaN(ms) ? null : new Date(ms).toISOString().replace(".000Z", "Z");
}

/** Trims a decimal string with 6 places to at least 2: "125000.000000" -> "125000.00". */
export function money(value: string): string {
  const m = /^(-?\d+)(?:\.(\d*))?$/.exec(value);
  if (!m) return value;
  const frac = (m[2] ?? "").replace(/0+$/, "").padEnd(2, "0");
  return `${m[1]}.${frac}`;
}

const DECIMAL = /^-?\d{1,22}(\.\d{1,6})?$/;
export const CURRENCY = /^[A-Z]{3}$/;
const HEADER = ["legacyid", "party", "account", "currency", "amount"];

export interface ParsedRecords {
  records: OpenItemRecord[];
  errors: string[];
}

/**
 * Parses pasted open items, one per line: legacyId,party,account,currency,amount. A first line
 * that names the columns is skipped. Amounts stay decimal strings. Problems are reported per line
 * so the user can fix the paste; the service applies the referential and domain rules itself.
 */
export function parseRecords(text: string): ParsedRecords {
  const records: OpenItemRecord[] = [];
  const errors: string[] = [];
  const lines = text.split(/\r?\n/);
  lines.forEach((raw, index) => {
    const line = raw.trim();
    if (line === "") return;
    const cells = line.split(/[,;\t]/).map((c) => c.trim());
    if (index === 0 && cells.map((c) => c.toLowerCase()).join(",") === HEADER.join(",")) return;
    const n = index + 1;
    if (cells.length !== 5) {
      errors.push(`Line ${n}: expected 5 values (legacyId, party, account, currency, amount), found ${cells.length}.`);
      return;
    }
    const [legacyId, party, account, currency, amount] = cells;
    if (!legacyId) {
      errors.push(`Line ${n}: the legacy id is blank.`);
      return;
    }
    if (!DECIMAL.test(amount)) {
      errors.push(`Line ${n}: amount "${amount}" is not a decimal such as 1250.00.`);
      return;
    }
    records.push({ legacyId, party, account, currency: currency.toUpperCase(), amount });
  });
  return { records, errors };
}

/** Splits a comma, space or newline separated list and drops blanks and repeats. */
export function splitList(text: string): string[] {
  return [...new Set(text.split(/[\s,;]+/).map((s) => s.trim()).filter(Boolean))];
}
