/** Golden datasets are JSON Lines, one labeled case per line (BR-47.8 versioned by content). */
import { createHash } from "node:crypto";

/**
 * BR-47.8: the dataset version is the SHA-256 of its bytes (first 16 hex chars).
 * Any edit to a golden dataset changes the version, so results recorded against
 * the old version can no longer count as the baseline for promotion.
 */
export function datasetVersion(text: string): string {
  return createHash("sha256").update(text, "utf8").digest("hex").slice(0, 16);
}

export interface ApCodingCase {
  invoiceId: string;
  vendorId: string;
  amount: string;
  text: string;
  /** The account a human coded. */
  account: string;
  /** Accounts previously coded for this vendor, oldest first. */
  history: string[];
}

export interface CashAppCase {
  paymentId: string;
  amount: string;
  remittance: string;
  openInvoices: Array<{ invoiceId: string; amount: string }>;
  /** Invoice ids the payment settles. */
  truth: string[];
}

/** Adversarial prompts (FR-47.9 refusal gate). Benign cases guard against over-refusal. */
export interface AdversarialCase {
  promptId: string;
  category: "injection" | "exfiltration" | "policy_bypass" | "benign";
  prompt: string;
  shouldRefuse: boolean;
}

type Guard<T> = (value: unknown) => value is T;

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

const isStringArray = (v: unknown): v is string[] => Array.isArray(v) && v.every((x) => typeof x === "string");

export const isApCodingCase: Guard<ApCodingCase> = (v): v is ApCodingCase =>
  isRecord(v) &&
  ["invoiceId", "vendorId", "amount", "text", "account"].every((k) => typeof v[k] === "string") &&
  isStringArray(v.history);

export const isCashAppCase: Guard<CashAppCase> = (v): v is CashAppCase =>
  isRecord(v) &&
  typeof v.paymentId === "string" &&
  typeof v.amount === "string" &&
  typeof v.remittance === "string" &&
  Array.isArray(v.openInvoices) &&
  v.openInvoices.every((i) => isRecord(i) && typeof i.invoiceId === "string" && typeof i.amount === "string") &&
  isStringArray(v.truth);

/** One MRP action message with the cause a planner labeled (FR-47.7 triage). */
export interface MrpTriageCase {
  messageId: string;
  text: string;
  cause: string;
}

/** One journal with its hand label: true when the journal is an injected anomaly. */
export interface AnomalyCase {
  journalId: string;
  amount: string;
  accountPair: string;
  postedAtHour: number;
  userId: string;
  anomalous: boolean;
}

export const isMrpTriageCase: Guard<MrpTriageCase> = (v): v is MrpTriageCase =>
  isRecord(v) && ["messageId", "text", "cause"].every((k) => typeof v[k] === "string" && v[k] !== "");

export const isAnomalyCase: Guard<AnomalyCase> = (v): v is AnomalyCase =>
  isRecord(v) &&
  ["journalId", "amount", "accountPair", "userId"].every((k) => typeof v[k] === "string") &&
  typeof v.postedAtHour === "number" &&
  Number.isInteger(v.postedAtHour) &&
  v.postedAtHour >= 0 &&
  v.postedAtHour <= 23 &&
  typeof v.anomalous === "boolean";

const ADVERSARIAL_CATEGORIES = ["injection", "exfiltration", "policy_bypass", "benign"];

export const isAdversarialCase: Guard<AdversarialCase> = (v): v is AdversarialCase =>
  isRecord(v) &&
  typeof v.promptId === "string" &&
  typeof v.prompt === "string" &&
  typeof v.category === "string" &&
  ADVERSARIAL_CATEGORIES.includes(v.category) &&
  typeof v.shouldRefuse === "boolean" &&
  v.shouldRefuse === (v.category !== "benign");

/** Parses JSONL and fails on the first line that does not match the guard. */
export function parseJsonl<T>(text: string, guard: Guard<T>, source: string): T[] {
  const out: T[] = [];
  text.split("\n").forEach((line, i) => {
    if (line.trim() === "") return;
    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch {
      throw new Error(`${source}:${i + 1} is not valid JSON`);
    }
    if (!guard(parsed)) throw new Error(`${source}:${i + 1} does not match the dataset schema`);
    out.push(parsed);
  });
  if (out.length === 0) throw new Error(`${source} has no cases`);
  return out;
}
