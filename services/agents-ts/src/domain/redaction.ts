import { AgentError } from "../errors.js";
import { ibanChecksumValid, luhnValid, validTaxId } from "./identifiers.js";

/**
 * PII redaction before any provider call (FR-47.12). Categories here are the
 * global core; country packs extend them later (PRD 9.47.20).
 */
export type PiiCategory = "EMAIL" | "IBAN" | "CARD" | "SSN" | "TAXID" | "BANKACCT" | "PAY";

interface Pattern {
  re: RegExp;
  valid?: (value: string) => boolean;
  longestValidPrefix?: boolean;
}

function acceptedPrefix({ valid, longestValidPrefix }: Pattern, value: string): string | null {
  if (valid === undefined) return value;
  if (!longestValidPrefix) return valid(value) ? value : null;
  for (let end = value.length; end > 0; end--) {
    const candidate = value.slice(0, end);
    if (candidate.trimEnd() === candidate && valid(candidate)) return candidate;
  }
  return null;
}

const AFTER_LABEL = String.raw`["']?\s{0,3}[:=#]?\s{0,3}"?`;

const PATTERNS: Readonly<Record<PiiCategory, Pattern>> = {
  // The lookbehind starts a match only at the beginning of a local part, so a long
  // run of word characters is scanned once, not once per position (9.47.19 budget).
  EMAIL: { re: /(?<![A-Za-z0-9._%+-])[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g },
  IBAN: { re: /\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]{4}){2,7}(?: ?[A-Z0-9]{1,3})?\b/gi, valid: ibanChecksumValid, longestValidPrefix: true },
  CARD: { re: /\b(?:\d{13,19}|\d{4}([ -])\d{4}\1\d{4}\1\d{1,7}|\d{4}([ -])\d{6}\2\d{4,5})\b/g, valid: luhnValid },
  SSN: { re: new RegExp(String.raw`\b\d{3}-\d{2}-\d{4}\b|(?<=\bssn\w{0,12}${AFTER_LABEL})\d{9}\b`, "gi") },
  TAXID: {
    re: new RegExp(String.raw`(?<=\b(?:(?:EIN|VAT|GSTIN|RFC|TIN)(?:[ _]?(?:id|number|no|num)\.?)?|tax[ _]?(?:id|number|no|num)\.?)${AFTER_LABEL}(?:[A-Z]{2} )?)[A-Z0-9&][A-Z0-9&.-]{4,20}[A-Z0-9]`, "gi"),
    valid: validTaxId,
  },
  BANKACCT: { re: new RegExp(String.raw`(?<=(?:account|acct)\w{0,12}(?: (?:number|no|num)\.?)?${AFTER_LABEL})\d(?:[ -]?\d){5,16}\b`, "gi") },
  PAY: {
    re: new RegExp(String.raw`(?<=(?:salary|wage|pay[ _-]?rate|base[ _-]?pay|hourly[ _-]?rate|bonus|compensation)\w{0,12}["']?(?:\s{0,3}[:=]\s{0,3}|\s(?:is|of)\s)(?:\{[^{}]{0,80}?"(?:amount|value|gross|net|base)"\s{0,3}:\s{0,3})?"?(?:[A-Za-z]{3}\s?|[$€£]\s?)?)\d(?:[\d,.]*\d)?`, "gi"),
  },
};

export const PSEUDONYM_RE = /\[(?:EMAIL|IBAN|CARD|SSN|TAXID|BANKACCT|PAY|AMOUNT)-\d+\]/g;

export const P3_CATEGORIES: readonly PiiCategory[] = ["IBAN", "CARD", "SSN", "TAXID", "BANKACCT", "PAY"];

// payroll is PRD ViewSensitivePayroll, worker is PRD ViewSensitiveWorker
export const SENSITIVE_DUTY = { payroll: "agents.sensitive-payroll.view", worker: "agents.sensitive-worker.view" } as const;

const P3_DOMAIN: Partial<Record<PiiCategory, keyof typeof SENSITIVE_DUTY>> = { PAY: "payroll", SSN: "worker", TAXID: "worker" };

const IDENTIFIER_CATEGORIES: readonly (PiiCategory | "AMOUNT")[] = ["SSN", "TAXID", "CARD", "IBAN", "BANKACCT"];
const MIN_SIGNIFICANT_FOR_SUFFIX = 8;

export function maskValue(value: string, category: PiiCategory | "AMOUNT"): string {
  const significant = value.replace(/[^A-Za-z0-9]/g, "");
  if (!IDENTIFIER_CATEGORIES.includes(category) || significant.length < MIN_SIGNIFICANT_FOR_SUFFIX) return "****";
  return `****${significant.slice(-4)}`;
}

export function mayViewValue(category: PiiCategory | "AMOUNT", duties: ReadonlySet<string>): boolean {
  if (category === "AMOUNT" || !P3_CATEGORIES.includes(category)) return true;
  const domain = P3_DOMAIN[category];
  return domain !== undefined && duties.has(SENSITIVE_DUTY[domain]);
}

const matchesOf = (category: PiiCategory, text: string): string[] =>
  [...text.matchAll(PATTERNS[category].re)].flatMap((m) => acceptedPrefix(PATTERNS[category], m[0]) ?? []);

export interface PiiFinding {
  category: PiiCategory;
  value: string;
}

/** Returns every PII match in the text. Used as a write-time scanner. */
export function scanPii(text: string): PiiFinding[] {
  const findings: PiiFinding[] = [];
  for (const category of Object.keys(PATTERNS) as PiiCategory[]) {
    for (const value of matchesOf(category, text)) findings.push({ category, value });
  }
  return findings;
}

/** BR/invariant: agent_action.input_json never contains raw PII. */
export function assertNoPii(text: string): void {
  if (scanPii(text).length > 0) {
    throw new AgentError("AGT-0210", "unredacted PII found in text bound for storage or provider");
  }
}

interface Entry {
  pseudonym: string;
  expiresAt: number;
}

/** The pseudonym map as stored with a run that waits for approval, so a resumed run restores values the same way (FR-47.4). */
export interface PseudonymSnapshot {
  entries: Array<{ category: PiiCategory | "AMOUNT"; value: string; pseudonym: string; expiresAt: number }>;
  counters: Partial<Record<PiiCategory | "AMOUNT", number>>;
}

/**
 * Replaces PII with stable pseudonyms per (tenant, value) for `ttlMs`
 * (BR-47.7). The mapping lives only in this in-memory instance; the caller
 * decides whether to persist it (Vault transit-encrypted, per the PRD).
 */
export class Pseudonymizer {
  private readonly byValue = new Map<string, Entry>();
  private readonly byPseudonym = new Map<string, string>();
  private readonly counters: Partial<Record<PiiCategory | "AMOUNT", number>> = {};

  constructor(
    private readonly tenantId: string,
    private readonly ttlMs: number,
    private readonly now: () => number,
  ) {}

  /** The live mappings and counters, for persisting between the steps of a resumable run. */
  snapshot(): PseudonymSnapshot {
    this.sweep();
    const entries: PseudonymSnapshot["entries"] = [];
    for (const [key, entry] of this.byValue) {
      const [, category = "", value = ""] = key.split("\u0000");
      entries.push({ category: category as PiiCategory | "AMOUNT", value, pseudonym: entry.pseudonym, expiresAt: entry.expiresAt });
    }
    return { entries, counters: { ...this.counters } };
  }

  /** Loads a snapshot taken by `snapshot()`; mappings already past their TTL are dropped by the next sweep. */
  load(snapshot: PseudonymSnapshot): void {
    for (const e of snapshot.entries) {
      this.byValue.set(`${this.tenantId}\u0000${e.category}\u0000${e.value}`, { pseudonym: e.pseudonym, expiresAt: e.expiresAt });
      this.byPseudonym.set(e.pseudonym, e.value);
    }
    Object.assign(this.counters, snapshot.counters);
  }

  redact(text: string): string {
    let out = text;
    for (const category of Object.keys(PATTERNS) as PiiCategory[]) {
      const pattern = PATTERNS[category];
      out = out.replace(pattern.re, (value) => {
        const accepted = acceptedPrefix(pattern, value);
        return accepted === null ? value : this.pseudonymFor(category, accepted) + value.slice(accepted.length);
      });
    }
    return out;
  }

  /** Resolves pseudonyms in a model response back to real values for the UI only. */
  restore(text: string): string {
    this.sweep();
    let out = text;
    for (const [pseudonym, value] of this.byPseudonym) {
      out = out.split(pseudonym).join(value);
    }
    return out;
  }

  restoreFor(text: string, duties: ReadonlySet<string>): string {
    this.sweep();
    let out = text;
    for (const [pseudonym, value] of this.byPseudonym) {
      const category = pseudonym.slice(1, pseudonym.indexOf("-")) as PiiCategory;
      out = out.split(pseudonym).join(mayViewValue(category, duties) ? value : maskValue(value, category));
    }
    return out;
  }

  /** Drops mappings past their TTL. */
  sweep(): void {
    const t = this.now();
    for (const [key, entry] of this.byValue) {
      if (entry.expiresAt <= t) {
        this.byValue.delete(key);
        this.byPseudonym.delete(entry.pseudonym);
      }
    }
  }

  tokenize(category: "AMOUNT", value: string): string {
    return this.pseudonymFor(category, value);
  }

  private pseudonymFor(category: PiiCategory | "AMOUNT", value: string): string {
    this.sweep();
    const key = `${this.tenantId}\u0000${category}\u0000${value}`;
    const existing = this.byValue.get(key);
    if (existing) return existing.pseudonym;
    const n = (this.counters[category] ?? 0) + 1;
    this.counters[category] = n;
    const pseudonym = `[${category}-${n}]`;
    this.byValue.set(key, { pseudonym, expiresAt: this.now() + this.ttlMs });
    this.byPseudonym.set(pseudonym, value);
    return pseudonym;
  }
}
