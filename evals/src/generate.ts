/**
 * Deterministic generator for the grown AP coding and cash application datasets (FR-47.9
 * shapes). A fixed seed and no clock or randomness source other than the PRNG below mean the
 * same bytes on every run; test/generate.test.ts fails when the committed files drift.
 * Data is fictional (Cedar Ridge Pumps).
 *
 * Usage: node dist/generate.js [outDir]   (default: evals/datasets)
 */
import { mkdirSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import type { ApCodingCase, CashAppCase } from "./datasets.js";
import { apPrompt, cashPrompt, DEFAULT_EVAL_MODEL, record, recordingToJsonl, type ModelProvider } from "./replay.js";

export const GENERATOR_SEED = 4701;
export const AP_FILE = "ap-coding-v1.jsonl";
export const CASH_FILE = "cash-app-v1.jsonl";
export const RECORDING_FILE = "recordings/simulated-v1.jsonl";
export const AP_CASES = 400;
export const CASH_CASES = 300;

/** mulberry32: small, fast, and fully determined by the seed. */
export function prng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

type Rand = () => number;
const int = (r: Rand, lo: number, hi: number): number => lo + Math.floor(r() * (hi - lo + 1));
const pick = <T>(r: Rand, xs: readonly T[]): T => xs[Math.floor(r() * xs.length)] as T;
const cents = (n: number): string => `${Math.trunc(n / 100)}.${String(n % 100).padStart(2, "0")}`;

const ACCOUNTS = ["5100", "5200", "5300", "6100", "6200", "6300", "6400", "6500", "6600", "6700", "6800", "7100"];
const VENDOR_STEMS = ["Ridgeline Steel", "Cedar Freight", "Millbrook Seals", "Harbor Bearings", "Northgate Castings", "Alder Packaging", "Summit Coatings", "Pine Valley Fasteners", "Brightwater Chemicals", "Foxglove Electrical"];
const NOTES = ["Q1 supply", "Q2 supply", "Q3 supply", "Q4 supply", "expedite", "monthly service", "surcharge", "bulk order", "replenishment", "annual maintenance", "sample lot", "freight and handling"];

/** 40 vendors over 3 entities; each has a primary and a secondary account. */
export function generateApCoding(seed = GENERATOR_SEED, n = AP_CASES): ApCodingCase[] {
  const r = prng(seed);
  const vendors = Array.from({ length: 40 }, (_, i) => {
    const primary = pick(r, ACCOUNTS);
    let secondary = pick(r, ACCOUNTS);
    if (secondary === primary) secondary = ACCOUNTS[(ACCOUNTS.indexOf(primary) + 1) % ACCOUNTS.length] as string;
    return {
      id: `V-${String(100 + i)}`,
      name: `${VENDOR_STEMS[i % VENDOR_STEMS.length]} ${["LLC", "Inc", "GmbH", "Ltd"][i % 4]}`,
      entity: `CRP-${["US", "DE", "MX"][i % 3]}`,
      primary,
      secondary,
      history: [] as string[],
    };
  });
  const out: ApCodingCase[] = [];
  for (let i = 0; i < n; i++) {
    const v = pick(r, vendors);
    const roll = r();
    // Realistic noise: most invoices follow the vendor's habit, some use the secondary account,
    // a few are one-offs the history cannot predict.
    const account = roll < 0.88 ? v.primary : roll < 0.96 ? v.secondary : pick(r, ACCOUNTS);
    out.push({
      invoiceId: `INV-${String(10001 + i)}`,
      vendorId: v.id,
      amount: cents(int(r, 2500, 4_500_000)),
      text: `${v.name} (${v.entity}) ${pick(r, NOTES)} ref ${int(r, 1000, 9999)}`,
      account,
      history: v.history.slice(-6),
    });
    v.history.push(account);
  }
  return out;
}

/** Mix: exact single, multi-invoice (6%), partial/short pay (12%), unreferenced wire. */
export function generateCashApp(seed = GENERATOR_SEED + 1, n = CASH_CASES): CashAppCase[] {
  const r = prng(seed);
  const out: CashAppCase[] = [];
  let invoiceNo = 50000;
  for (let i = 0; i < n; i++) {
    const roll = r();
    const count = roll < 0.06 ? int(r, 2, 4) : 1;
    const open: Array<{ invoiceId: string; amount: string; cents: number }> = [];
    for (let k = 0; k < count; k++) {
      const c = int(r, 5000, 900_000);
      open.push({ invoiceId: `INV-${invoiceNo++}`, amount: cents(c), cents: c });
    }
    const total = open.reduce((s, o) => s + o.cents, 0);
    const partial = roll >= 0.06 && roll < 0.18;
    const paid = partial ? total - int(r, 100, 2500) : total;
    const referenced = r() < 0.7;
    const names = open.map((o) => o.invoiceId).join(" and ");
    const remittance = referenced
      ? partial
        ? `${names} short paid by ${cents(total - paid)}`
        : `Payment for ${names}`
      : pick(r, ["wire, no reference", "ACH batch", "customer remittance", "check deposit"]);
    // Distractors: other open invoices of the same customer, never equal to the payment amount.
    const distractors = Array.from({ length: int(r, 1, 3) }, () => {
      const c = int(r, 5000, 900_000);
      return { invoiceId: `INV-${invoiceNo++}`, amount: cents(c === paid ? c + 1 : c) };
    });
    const all = [...open.map(({ invoiceId, amount }) => ({ invoiceId, amount })), ...distractors];
    // Deterministic shuffle (Fisher-Yates over the seeded PRNG).
    for (let k = all.length - 1; k > 0; k--) {
      const j = int(r, 0, k);
      [all[k], all[j]] = [all[j] as (typeof all)[number], all[k] as (typeof all)[number]];
    }
    out.push({ paymentId: `PAY-${String(7001 + i)}`, amount: cents(paid), remittance, openInvoices: all, truth: open.map((o) => o.invoiceId) });
  }
  return out;
}

export const toJsonl = (rows: readonly unknown[]): string => rows.map((x) => JSON.stringify(x)).join("\n") + "\n";

/**
 * A stand-in for a model, used only to produce the checked-in fixture recording: it answers from
 * the labels with seeded mistakes (about 5% wrong first account, 3% missed invoice, 4% extra
 * invoice). It proves the replay path and the gate wiring; a recording from a live model (#213)
 * replaces it and is the only thing that counts as agent accuracy.
 */
export function simulatedModel(ap: readonly ApCodingCase[], cash: readonly CashAppCase[], seed = GENERATOR_SEED + 2): ModelProvider {
  const r = prng(seed);
  const answers = new Map<string, string>();
  for (const c of ap) {
    const wrong = ACCOUNTS.find((a) => a !== c.account) as string;
    const accounts = r() < 0.05 ? [wrong, c.account] : [c.account, wrong];
    answers.set(apPrompt(c).user, JSON.stringify({ accounts }));
  }
  for (const c of cash) {
    let ids = [...c.truth];
    const roll = r();
    if (roll < 0.03 && ids.length > 1) ids = ids.slice(1);
    else if (roll < 0.07) ids = [...ids, (c.openInvoices.find((o) => !c.truth.includes(o.invoiceId)) as { invoiceId: string }).invoiceId];
    answers.set(cashPrompt(c).user, JSON.stringify({ invoiceIds: ids }));
  }
  return {
    async complete(req) {
      const text = answers.get(req.user);
      if (text === undefined) throw new Error("simulated model has no answer for this prompt");
      return { text };
    },
  };
}

async function main(argv: string[]): Promise<void> {
  const dir = argv[0] ?? fileURLToPath(new URL("../datasets/", import.meta.url));
  mkdirSync(`${dir}/recordings`, { recursive: true });
  const ap = generateApCoding();
  const cash = generateCashApp();
  writeFileSync(`${dir}/${AP_FILE}`, toJsonl(ap));
  writeFileSync(`${dir}/${CASH_FILE}`, toJsonl(cash));
  const rec = await record(simulatedModel(ap, cash), `${DEFAULT_EVAL_MODEL}-simulated`, { ap, cash });
  writeFileSync(`${dir}/${RECORDING_FILE}`, recordingToJsonl(rec));
}

if (process.argv[1] !== undefined && fileURLToPath(import.meta.url) === process.argv[1]) await main(process.argv.slice(2));
