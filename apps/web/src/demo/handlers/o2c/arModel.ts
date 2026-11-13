// Receivables model for the demo. Open items are the posted billing invoices of model.ts plus the extra items of
// arLedger.ts (opening items from cutover, interest, dunning fees, deductions, re-bills) and the payments held on
// account. Payments pay those invoices: applying one settles the invoice in billing too, so the invoice list, the
// order-to-cash flow and the credit exposure move with it. The collections records (disputes, promises, dunning,
// interest, cases, confirmations, down payments, deductions) all point at these items and customers.
import type {
  BankStatementBatch,
  Bucket,
  CollectionCase,
  Confirmation,
  ConfirmationRun,
  CustomerRisk,
  Deduction,
  Dispute,
  DownPayment,
  DunningLetter,
  DunningRun,
  Exclusion,
  InterestNote,
  LockboxBatch,
  OpenItem,
  Payment,
  PaymentPromise,
  RemittanceLine,
} from "../../../modules/receivables/api/client";
import { LEGAL_ENTITY } from "../../company";
import { at, dec, day, table, uid } from "../../store";
import { BLUEWATER, COASTAL, CUTOVER_DAYS_AGO, HARLAN, KR, LEGACY, METRO, PRAIRIE, SUMMIT, extraItems, invoiceDunning, openAtImport, payments, r2 } from "./arLedger";
import { K, customerById, invoices, orders, type InvoiceRow } from "./model";

export { payments, extraItems };

const DAY_MS = 86_400_000;
export const daysBetween = (from: string, to: string): number => Math.round((Date.parse(to) - Date.parse(from)) / DAY_MS);
export const addDays = (date: string, n: number): string => new Date(Date.parse(date) + n * DAY_MS).toISOString().slice(0, 10);
const atIso = (date: string, hour = 9): string => `${date}T${String(hour).padStart(2, "0")}:00:00.000Z`;

/** The collections team; Jordan Avery is the demo user. */
export const TEAM = { ar: "Megan Holt", collector: "Tom Brandt", credit: "Rachel Kim", me: "Jordan Avery" };

// ---- Open items ----

const invoiceOpen = (inv: InvoiceRow): number => (inv.status === "POSTED" || inv.status === "PARTIALLY_SETTLED" ? r2(Number(inv.totals.gross) * (1 - inv.settled)) : 0);
const itemStatus = (open: number, amount: number): string => (open === 0 ? "CLEARED" : Math.abs(open) < Math.abs(amount) ? "PARTIAL" : "OPEN");

function invoiceItem(inv: InvoiceRow): OpenItem {
  const open = invoiceOpen(inv);
  const dunning = invoiceDunning.get(inv.id);
  return {
    id: inv.id,
    legalEntityId: inv.legalEntityId,
    customerId: inv.customerId,
    sourceType: "INVOICE",
    documentNumber: inv.number ?? "",
    documentDate: inv.invoiceDate,
    dueDate: inv.dueDate,
    currency: inv.currency,
    amount: inv.totals.gross,
    openAmount: dec(open),
    cashDiscountAmount: "0.00",
    dunningLevel: dunning?.level ?? 0,
    dunningBlock: dunning?.block ?? false,
    status: itemStatus(open, Number(inv.totals.gross)),
    version: inv.version,
    side: "DEBIT",
  };
}

/** A payment held on account shows as a credit item until it is applied. */
function creditItem(p: Payment): OpenItem {
  return {
    id: p.id,
    legalEntityId: p.legalEntityId,
    customerId: p.customerId ?? "",
    sourceType: "PAYMENT",
    documentNumber: p.sourceRef ?? "Payment",
    documentDate: p.receivedDate,
    dueDate: p.receivedDate,
    currency: p.currency,
    amount: dec(-Number(p.amount)),
    openAmount: dec(-Number(p.unappliedAmount)),
    cashDiscountAmount: "0.00",
    dunningLevel: 0,
    dunningBlock: true,
    status: "OPEN",
    version: p.version,
    side: "CREDIT",
  };
}

const billed = (i: InvoiceRow) => Boolean(i.number) && i.status !== "PROPOSAL" && i.status !== "REVERSED" && i.type !== "CREDIT_NOTE";

/** Every receivables item, cleared ones included, ordered by due date. */
export function allItems(): OpenItem[] {
  const fromInvoices = invoices.all().filter(billed).map(invoiceItem);
  const credits = payments.all().filter((p) => p.status === "ON_ACCOUNT" && p.customerId && Number(p.unappliedAmount) > 0).map(creditItem);
  return [...fromInvoices, ...extraItems.all(), ...credits].sort((a, b) => a.dueDate.localeCompare(b.dueDate) || a.documentNumber.localeCompare(b.documentNumber));
}

export const openItems = (): OpenItem[] => allItems().filter((i) => Number(i.openAmount) !== 0);
export const findItem = (id: string): OpenItem | undefined => allItems().find((i) => i.id === id);
export const isInvoiceItem = (id: string): boolean => invoices.get(id) !== undefined;

/** One change to an item's open amount, for statements as of an earlier date. */
interface Settlement {
  openItemId: string;
  amount: number;
  date: string;
}
export const settlements: Settlement[] = [];

/** Gives back to an item what a reversed settlement took off it (an unapplied payment), and logs it. */
export function unsettleItem(id: string, total: number, date = day(0)): OpenItem | undefined {
  const inv = invoices.get(id);
  if (inv) {
    const gross = Number(inv.totals.gross);
    const open = Math.min(gross, r2(invoiceOpen(inv) + total));
    invoices.update(id, { settled: 1 - open / gross, status: open >= gross ? "POSTED" : "PARTIALLY_SETTLED", version: inv.version + 1 });
  } else {
    const item = extraItems.get(id);
    if (!item) return undefined;
    const open = r2(Number(item.openAmount) + total);
    extraItems.update(id, { openAmount: dec(open), status: itemStatus(open, Number(item.amount)), version: item.version + 1 });
  }
  settlements.push({ openItemId: id, amount: -total, date });
  return findItem(id);
}

/** Reduces an item's open amount by total (payment, discount and write-off together) and logs it. */
export function settleItem(id: string, total: number, date = day(0)): OpenItem | undefined {
  const inv = invoices.get(id);
  if (inv) {
    const gross = Number(inv.totals.gross);
    const open = Math.max(0, r2(invoiceOpen(inv) - total));
    invoices.update(id, { settled: open === 0 ? 1 : 1 - open / gross, status: open === 0 ? "SETTLED" : "PARTIALLY_SETTLED", version: inv.version + 1 });
  } else {
    const item = extraItems.get(id);
    if (!item) return undefined;
    const open = r2(Number(item.openAmount) - total);
    extraItems.update(id, { openAmount: dec(open), status: itemStatus(open, Number(item.amount)), version: item.version + 1 });
  }
  settlements.push({ openItemId: id, amount: total, date });
  return findItem(id);
}

/** Sets an item's dunning level (and the date of its letter). */
export function setDunning(id: string, patch: { level?: number; block?: boolean; lastLetter?: string }): void {
  if (isInvoiceItem(id)) {
    const now = invoiceDunning.get(id) ?? { level: 0, block: false };
    invoiceDunning.set(id, { ...now, ...patch });
    return;
  }
  extraItems.update(id, (row) => ({ ...row, dunningLevel: patch.level ?? row.dunningLevel, dunningBlock: patch.block ?? row.dunningBlock }));
  if (patch.lastLetter) lastLetters.set(id, patch.lastLetter);
}
const lastLetters = new Map<string, string>();
export const lastLetterOf = (id: string): string | undefined => invoiceDunning.get(id)?.lastLetter ?? lastLetters.get(id);

/** An item's open amount at the end of a date: what it was booked at, less what was settled by then. */
export function openAsOf(item: OpenItem, date: string): number {
  if (item.documentDate > date) return 0;
  if (item.importSource && date < day(-CUTOVER_DAYS_AGO)) return 0;
  const start = item.importSource ? (openAtImport.get(item.id) ?? Number(item.amount)) : Number(item.amount);
  const settled = settlements.filter((s) => s.openItemId === item.id && s.date <= date).reduce((s, x) => s + x.amount, 0);
  return r2(start - settled);
}

let extraSeq = 0;
/** Adds an interest, fee, deduction or re-bill item. */
export function addExtraItem(customerId: string, sourceType: string, documentNumber: string, amount: number, documentDate: string, dueDate: string): OpenItem {
  return extraItems.insert({
    id: uid(KR.extra, ++extraSeq),
    legalEntityId: LEGAL_ENTITY.id,
    customerId,
    sourceType,
    documentNumber,
    documentDate,
    dueDate,
    currency: "USD",
    amount: dec(amount),
    openAmount: dec(amount),
    cashDiscountAmount: "0.00",
    dunningLevel: 0,
    // Deductions are aged apart and never dunned.
    dunningBlock: sourceType === "DEDUCTION",
    status: "OPEN",
    version: 1,
    side: "DEBIT",
  });
}

// ---- Aging ----

export function bucketOf(item: Pick<OpenItem, "dueDate" | "side">, asOf: string): Bucket {
  if (item.side === "CREDIT") return "CURRENT";
  const late = daysBetween(item.dueDate, asOf);
  if (late <= 0) return "CURRENT";
  if (late <= 30) return "1-30";
  if (late <= 60) return "31-60";
  if (late <= 90) return "61-90";
  if (late <= 180) return "91-180";
  return "OVER-180";
}

// ---- Seed: invoices, then the history that settled them ----

const inv = (n: number): InvoiceRow => invoices.get(uid(K.invoice, n))!;
const gross = (n: number): number => Number(inv(n).totals.gross);
const num = (n: number): string => inv(n).number ?? "";
/** The free-text services invoice for Metro Facilities (model.ts numbers it uid(K.invoice, 40)). */
const FT = 40;

let paySeq = 0;
function payment(p: Partial<Payment> & Pick<Payment, "paymentMode" | "amount" | "receivedDate" | "status">): Payment {
  return payments.insert({
    id: uid(KR.payment, ++paySeq),
    legalEntityId: LEGAL_ENTITY.id,
    customerId: null,
    source: "MANUAL",
    sourceRef: null,
    unappliedAmount: p.status === "RECEIVED" || p.status === "ON_ACCOUNT" ? p.amount : "0.00",
    currency: "USD",
    valueDate: p.receivedDate,
    payerName: null,
    payerAccountHash: null,
    shortPayReason: null,
    remittance: [],
    version: p.status === "RECEIVED" ? 1 : 2,
    ...p,
  });
}
const rem = (reference: string, amount: number): RemittanceLine => ({ reference, amount: dec(amount) });
const hash = (seed: string): string => {
  let h = 0x811c9dc5;
  let out = "";
  for (let round = 0; round < 8; round++) {
    for (const ch of `${seed}:${round}`) h = Math.imul(h ^ ch.charCodeAt(0), 0x01000193) >>> 0;
    out += h.toString(16).padStart(8, "0");
  }
  return out;
};
export { hash as fakeSha256 };

const PAY = {
  metro14: payment({ customerId: METRO.id, paymentMode: "CHECK", source: "LOCKBOX", sourceRef: "CHK 20417", amount: dec(gross(14) - 38.4), receivedDate: day(-52), status: "APPLIED", payerName: "Metro Facilities Group", payerAccountHash: hash("metro-acct"), shortPayReason: "Freight charge not agreed", remittance: [rem(num(14), gross(14))] }),
  summit13: payment({ customerId: SUMMIT.id, paymentMode: "ACH", source: "BANK_STATEMENT", sourceRef: "ACH 7731904", amount: dec(gross(13) - 1260), receivedDate: day(-40), status: "APPLIED", payerName: "Summit Mining Supply LLC", payerAccountHash: hash("summit-acct"), shortPayReason: "Pump housing damaged in transit", remittance: [rem(num(13), gross(13))] }),
  coastal10: payment({ customerId: COASTAL.id, paymentMode: "WIRE", source: "BANK_STATEMENT", sourceRef: "FED 1028-0114", amount: dec(gross(10) - 1000), receivedDate: day(-12), status: "APPLIED", payerName: "Coastal Desalination Partners", payerAccountHash: hash("coastal-acct"), shortPayReason: "Q3 volume rebate taken", remittance: [rem(num(10), gross(10))] }),
  harlan11: payment({ customerId: HARLAN.id, paymentMode: "CHECK", source: "LOCKBOX", sourceRef: "CHK 58213", amount: dec(r2(gross(11) * 0.6)), receivedDate: day(-6), status: "APPLIED", payerName: "Harlan Agricultural Co-op", payerAccountHash: hash("harlan-acct"), remittance: [rem(num(11), r2(gross(11) * 0.6))] }),
  bluewaterL: payment({ customerId: BLUEWATER.id, paymentMode: "ACH", source: "BANK_STATEMENT", sourceRef: "ACH 7598120", amount: "21300.00", receivedDate: day(-88), status: "APPLIED", payerName: "Bluewater Municipal Utilities", payerAccountHash: hash("bluewater-acct"), remittance: [rem("INV-L48701", 21300)] }),
  prairieL: payment({ customerId: PRAIRIE.id, paymentMode: "CHECK", source: "LOCKBOX", sourceRef: "CHK 10877", amount: "11970.00", receivedDate: day(-95), status: "APPLIED", payerName: "Prairie Irrigation Systems", payerAccountHash: hash("prairie-acct"), shortPayReason: "Price on pivot pumps", remittance: [rem("INV-L48722", 12615)] }),
  summitWire: payment({ customerId: SUMMIT.id, paymentMode: "WIRE", source: "BANK_STATEMENT", sourceRef: "FED 1026-0087", amount: "4200.00", receivedDate: day(-15), status: "ON_ACCOUNT", payerName: "SUMMIT MINING SUPPLY LLC", payerAccountHash: hash("summit-acct") }),
  metroCheck: payment({ customerId: METRO.id, paymentMode: "CHECK", source: "LOCKBOX", sourceRef: "CHK 20588", amount: "1850.00", receivedDate: day(-9), status: "ON_ACCOUNT", payerName: "Metro Facilities Group", payerAccountHash: hash("metro-acct"), remittance: [rem("SVC-1014", 1850)] }),
  prairieNsf: payment({ customerId: PRAIRIE.id, paymentMode: "CHECK", source: "LOCKBOX", sourceRef: "CHK 11092", amount: dec(gross(12)), receivedDate: day(-9), status: "REVERSED", payerName: "Prairie Irrigation Systems", payerAccountHash: hash("prairie-acct"), remittance: [rem(num(12), gross(12))], version: 3 }),
  bluewater7: payment({ customerId: BLUEWATER.id, paymentMode: "ACH", source: "BANK_STATEMENT", sourceRef: "ACH 7740288", amount: dec(gross(7)), receivedDate: day(-1), status: "RECEIVED", payerName: "Bluewater Municipal Utilities", payerAccountHash: hash("bluewater-acct"), remittance: [rem(num(7), gross(7))] }),
  unidentified: payment({ paymentMode: "WIRE", source: "BANK_STATEMENT", sourceRef: "FED 1109-0031", amount: dec(gross(FT)), receivedDate: day(-1), status: "RECEIVED", payerName: "METRO FAC GRP ACCTS PAYABLE" }),
};
export const SEEDED_PAYMENTS = PAY;

// Settlements behind the seeded invoice and opening-item states, so statements as of an earlier date add up.
settlements.push(
  { openItemId: inv(14).id, amount: gross(14), date: day(-52) },
  { openItemId: inv(13).id, amount: gross(13), date: day(-40) },
  { openItemId: inv(10).id, amount: gross(10), date: day(-12) },
  { openItemId: inv(11).id, amount: r2(gross(11) * 0.6), date: day(-6) },
  { openItemId: LEGACY.bluewater, amount: 21300, date: day(-88) },
  { openItemId: LEGACY.prairie, amount: 12615, date: day(-95) },
);

// ---- Deductions: short payments moved off their invoices ----

export const deductions = table<Deduction>([]);
let dedSeq = 40;
export const deductionNumber = (): string => `DED-${String(++dedSeq).padStart(5, "0")}`;

/** The DEDUCTION item each deduction moved its amount to, by deduction id. */
export const deductionItem = new Map<string, string>();

type DeductionSeed = Omit<Deduction, "id" | "legalEntityId" | "currency" | "version" | "openItemId" | "documentNumber"> & { item: OpenItem; on: string; resolvedOn?: string };
function deduction(n: number, seed: DeductionSeed): Deduction {
  const { item, on, resolvedOn, ...rest } = seed;
  const moved = addExtraItem(rest.customerId, "DEDUCTION", deductionNumber(), Number(rest.amount), on, on);
  // A deduction being worked keeps its item open; a resolved one cleared it.
  if (resolvedOn) settleItem(moved.id, Number(rest.amount), resolvedOn);
  const id = uid(KR.deduction, n);
  deductionItem.set(id, moved.id);
  return deductions.insert({ id, legalEntityId: LEGAL_ENTITY.id, currency: "USD", version: resolvedOn ? 3 : rest.status === "MATCHED" ? 2 : 1, openItemId: item.id, documentNumber: item.documentNumber, ...rest });
}

const legacyItem = (id: string) => extraItems.get(id)!;

deduction(1, { customerId: SUMMIT.id, item: invoiceItem(inv(13)), paymentId: PAY.summit13.id, type: "DAMAGE", reasonCode: "Pump housing cracked in transit", amount: "1260.00", status: "OPEN", createdAt: at(-40), on: day(-40) });
deduction(2, { customerId: COASTAL.id, item: invoiceItem(inv(10)), paymentId: PAY.coastal10.id, type: "PROMO", reasonCode: "Q3 volume rebate taken at payment", amount: "1000.00", status: "MATCHED", claimType: "REBATE_ACCRUAL", claimRef: "RB-COASTAL-Q3", claimRemaining: "1000.00", createdAt: at(-12), on: day(-12) });
deduction(3, { customerId: METRO.id, item: invoiceItem(inv(14)), paymentId: PAY.metro14.id, type: "UNKNOWN", reasonCode: "Freight charge not agreed", amount: "38.40", status: "WRITTEN_OFF", decisionNote: "Under the 50.00 write-off limit", createdAt: at(-52), resolvedAt: at(-50), on: day(-52), resolvedOn: day(-50) });
deduction(4, { customerId: PRAIRIE.id, item: legacyItem(LEGACY.prairie), paymentId: PAY.prairieL.id, type: "PRICE", reasonCode: "Price on pivot pumps", amount: "645.00", status: "DENIED", denialReason: "NOT_AUTHORIZED", decisionNote: "No price agreement on file; re-billed", createdAt: at(-95), resolvedAt: at(-60), on: day(-95), resolvedOn: day(-60) });

// The denied Prairie deduction was re-billed and is still unpaid.
{
  const rebill = addExtraItem(PRAIRIE.id, "REBILL", "RB-00012", 645, day(-60), day(-30));
  deductions.update(uid(KR.deduction, 4), { rebillItemId: rebill.id });
}

// ---- Disputes ----

export const disputes = table<Dispute>([
  { id: uid(KR.dispute, 1), legalEntityId: LEGAL_ENTITY.id, customerId: SUMMIT.id, openItemId: LEGACY.summit, documentNumber: "INV-L48812", reasonCode: "PRICE", disputedAmount: "3600.00", currency: "USD", owner: TEAM.ar, status: "UNDER_REVIEW", slaDue: at(1.5), createdAt: at(-3.5), version: 2 },
  { id: uid(KR.dispute, 2), legalEntityId: LEGAL_ENTITY.id, customerId: HARLAN.id, openItemId: inv(11).id, documentNumber: num(11), reasonCode: "DAMAGE", disputedAmount: dec(r2(1290 * 0.95 * 1.06)), currency: "USD", owner: TEAM.ar, status: "OPEN", slaDue: at(4.7), createdAt: at(-0.3), version: 1 },
  { id: uid(KR.dispute, 3), legalEntityId: LEGAL_ENTITY.id, customerId: BLUEWATER.id, openItemId: LEGACY.bluewater, documentNumber: "INV-L48701", reasonCode: "DELIVERY", disputedAmount: "2450.00", currency: "USD", owner: TEAM.ar, status: "RESOLVED_PAID", resolutionNote: "Proof of delivery sent; paid in full by ACH", slaDue: at(-97), createdAt: at(-102), resolvedAt: at(-88), version: 3 },
  { id: uid(KR.dispute, 4), legalEntityId: LEGAL_ENTITY.id, customerId: COASTAL.id, openItemId: LEGACY.coastal, documentNumber: "INV-L48735", reasonCode: "RETENTION", disputedAmount: "18480.00", currency: "USD", owner: TEAM.credit, status: "REJECTED", resolutionNote: "Retention is due on the acceptance certificate per contract; dunning blocked until then", slaDue: at(-103), createdAt: at(-108), resolvedAt: at(-104), version: 2 },
]);

/** The dispute keeping an item out of dunning and interest on a date, if any. */
export const activeDispute = (itemId: string, asOf = day(0)): Dispute | undefined =>
  disputes.all().find((d) => d.openItemId === itemId && d.createdAt.slice(0, 10) <= asOf && (d.status === "OPEN" || d.status === "UNDER_REVIEW"));

// ---- Promises to pay ----

export const promises = table<PaymentPromise>([
  { id: uid(KR.promise, 1), legalEntityId: LEGAL_ENTITY.id, customerId: METRO.id, openItemId: LEGACY.metro, documentNumber: "INV-L48790", currency: "USD", amount: "6940.00", promiseDate: day(-21), createdAt: at(-30) },
  { id: uid(KR.promise, 2), legalEntityId: LEGAL_ENTITY.id, customerId: SUMMIT.id, openItemId: LEGACY.summit, documentNumber: "INV-L48812", currency: "USD", amount: "43250.00", promiseDate: day(5), createdAt: at(-3) },
  { id: uid(KR.promise, 3), legalEntityId: LEGAL_ENTITY.id, customerId: COASTAL.id, openItemId: LEGACY.coastal, documentNumber: "INV-L48735", currency: "USD", amount: "18480.00", promiseDate: day(25), createdAt: at(-4) },
]);

/** A promise holds its item out of dunning and interest until two days after the promise date. */
export const promiseHolds = (p: PaymentPromise, asOf: string): boolean => addDays(p.promiseDate, 2) >= asOf;
export const heldByPromise = (itemId: string, asOf: string): boolean => promises.all().some((p) => p.openItemId === itemId && p.createdAt.slice(0, 10) <= asOf && promiseHolds(p, asOf));
export const brokenPromises = (customerId: string, asOf: string): number =>
  promises.all().filter((p) => p.customerId === customerId && !promiseHolds(p, asOf) && Number(findItem(p.openItemId)?.openAmount ?? 0) > 0).length;

// ---- Dunning ----

/** The level an item reaches after grace: 1 past grace, 2 from 45 days, 3 (final notice) from 120 days. */
export function levelFor(daysOverdue: number, grace: number): number {
  if (daysOverdue <= grace) return 0;
  if (daysOverdue >= 120) return 3;
  if (daysOverdue >= 45) return 2;
  return 1;
}
export const FEES: Record<number, number> = { 1: 0, 2: 25, 3: 50 };
const dunnable = (i: OpenItem) => i.side !== "CREDIT" && (i.sourceType === "INVOICE" || i.sourceType === "REBILL") && Number(i.openAmount) > 0;

let letterSeq = 0;
/** Proposes letters for a run date: one letter per customer whose items reach a new level, with what was left out. */
export function proposeRun(runDate: string, graceDays: number, levelOf: (i: OpenItem) => number = (i) => i.dunningLevel): { letters: DunningLetter[]; exclusions: Exclusion[] } {
  const exclusions: Exclusion[] = [];
  const byCustomer = new Map<string, DunningLetter>();
  for (const item of allItems().filter((i) => dunnable(i) && openAsOf(i, runDate) > 0 && i.documentDate <= runDate)) {
    const late = daysBetween(item.dueDate, runDate);
    const from = levelOf(item);
    const to = levelFor(late, graceDays);
    if (to <= from) continue;
    const reason: Exclusion["reason"] | undefined = item.dunningBlock ? "BLOCKED" : activeDispute(item.id, runDate) ? "DISPUTE" : heldByPromise(item.id, runDate) ? "PROMISE" : (() => {
      const last = lastLetterOf(item.id);
      return last && daysBetween(last, runDate) < 10 ? "INTERVAL" : undefined;
    })();
    if (reason) {
      exclusions.push({ openItemId: item.id, customerId: item.customerId, reason });
      continue;
    }
    const letter = byCustomer.get(item.customerId) ?? { id: uid(KR.letter, ++letterSeq), customerId: item.customerId, currency: item.currency, level: 0, priorLevel: 0, amount: "0.00", feeAmount: "0.00", status: "DRAFT" as const, documentRef: null, items: [] };
    const open = openAsOf(item, runDate);
    letter.items.push({ openItemId: item.id, documentNumber: item.documentNumber, dueDate: item.dueDate, openAmount: dec(open), daysOverdue: late, fromLevel: from, toLevel: to });
    letter.level = Math.max(letter.level, to);
    letter.priorLevel = Math.max(letter.priorLevel, from);
    letter.amount = dec(Number(letter.amount) + open);
    letter.feeAmount = dec(FEES[letter.level] ?? 0);
    byCustomer.set(item.customerId, letter);
  }
  const letters = [...byCustomer.values()];
  // A letter under 50.00 is not sent.
  for (const l of letters.filter((x) => Number(x.amount) < 50)) {
    l.items.forEach((i) => exclusions.push({ openItemId: i.openItemId, customerId: l.customerId, reason: "BELOW_MINIMUM" }));
  }
  return { letters: letters.filter((x) => Number(x.amount) >= 50), exclusions };
}

let feeSeq = 0;
let dunSeq = 0;
const yymm = (date: string) => `${date.slice(2, 4)}${date.slice(5, 7)}`;
/** Issues a letter: raises item levels, records the letter date and posts the fee as an open item. */
export function issueLetter(letter: DunningLetter, runDate: string): DunningLetter {
  for (const i of letter.items) setDunning(i.openItemId, { level: i.toLevel, lastLetter: runDate });
  if (Number(letter.feeAmount) > 0) addExtraItem(letter.customerId, "DUNNING_FEE", `DF-${yymm(runDate)}-${String(++feeSeq).padStart(4, "0")}`, Number(letter.feeAmount), runDate, addDays(runDate, 14));
  return { ...letter, status: "ISSUED", documentRef: `DUN-${yymm(runDate)}-${String(++dunSeq).padStart(4, "0")}` };
}

export const runs = table<DunningRun>([]);
{
  // Replays the procedure: each seeded run proposes from the levels the earlier runs left.
  const levels = new Map<string, number>();
  const seedRun = (n: number, ago: number, grace: number, status: DunningRun["status"]) => {
    const runDate = day(-ago);
    const { letters, exclusions } = proposeRun(runDate, grace, (i) => levels.get(i.id) ?? 0);
    const issued = status === "ISSUED";
    const out = letters.map((l) => {
      if (!issued) return l;
      l.items.forEach((i) => {
        levels.set(i.openItemId, i.toLevel);
        lastLetters.set(i.openItemId, runDate);
        if (isInvoiceItem(i.openItemId)) invoiceDunning.set(i.openItemId, { level: i.toLevel, block: false, lastLetter: runDate });
      });
      if (Number(l.feeAmount) > 0) addExtraItem(l.customerId, "DUNNING_FEE", `DF-${yymm(runDate)}-${String(++feeSeq).padStart(4, "0")}`, Number(l.feeAmount), runDate, addDays(runDate, 14));
      return { ...l, status: "ISSUED" as const, documentRef: `DUN-${yymm(runDate)}-${String(++dunSeq).padStart(4, "0")}` };
    });
    runs.insert({ id: uid(KR.run, n), legalEntityId: LEGAL_ENTITY.id, runDate, graceDays: grace, status, exclusions, letters: out, issuedAt: issued ? atIso(addDays(runDate, 0), 15) : null, version: issued ? 3 : 2 });
  };
  seedRun(1, 66, 5, "ISSUED");
  seedRun(2, 37, 0, "CANCELLED");
  seedRun(3, 36, 5, "ISSUED");
}

// ---- Interest ----

export const interestNotes = table<InterestNote>([]);
let intSeq = 0;
export const RATE = "0.12";
const monthEnd = (date: string, back: number): string => {
  const d = new Date(`${date}T00:00:00Z`);
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() - back + 1, 0)).toISOString().slice(0, 10);
};
/** The day before the calculation month starts: interest runs from there or from the due date, whichever is later. */
export const priorMonthEnd = (calc: string): string => monthEnd(calc, 1);

export function interestAmount(principal: number, rate: number, days: number, basis: "ACT_365" | "ACT_360"): number {
  return r2((principal * rate * days) / (basis === "ACT_360" ? 360 : 365));
}

export function postInterestNote(c: { openItemId: string; customerId: string; documentNumber: string; principal: number; days: number; amount: number }, calc: string, rate: string, basis: "ACT_365" | "ACT_360"): InterestNote {
  const period = calc.slice(0, 7);
  const number = `INT-${yymm(calc)}-${String(++intSeq).padStart(4, "0")}`;
  const item = addExtraItem(c.customerId, "INTEREST", number, c.amount, addDays(calc, 1), addDays(calc, 15));
  return interestNotes.insert({
    id: uid(KR.interest, intSeq),
    legalEntityId: LEGAL_ENTITY.id,
    customerId: c.customerId,
    openItemId: c.openItemId,
    documentNumber: c.documentNumber,
    interestItemId: item.id,
    period,
    calculationDate: calc,
    days: c.days,
    annualRate: rate,
    basis,
    currency: "USD",
    principal: dec(c.principal),
    amount: dec(c.amount),
  });
}
for (const back of [2, 1]) {
  const calc = monthEnd(day(0), back);
  for (const id of [LEGACY.metro, LEGACY.summit]) {
    const item = extraItems.get(id)!;
    const days = daysBetween(item.dueDate > priorMonthEnd(calc) ? item.dueDate : priorMonthEnd(calc), calc);
    const principal = Number(item.openAmount);
    postInterestNote({ openItemId: id, customerId: item.customerId, documentNumber: item.documentNumber, principal, days, amount: interestAmount(principal, Number(RATE), days, "ACT_365") }, calc, RATE, "ACT_365");
  }
}

// ---- Collection cases ----

export const SLA_HOURS: Record<CollectionCase["category"], number> = { COLLECTION: 72, DISPUTE: 120, DEDUCTION: 240 };
function caseFile(n: number, c: Omit<CollectionCase, "id" | "legalEntityId" | "currency" | "slaHours" | "slaDue" | "slaBreached" | "createdAt" | "version"> & { ago: number }): CollectionCase {
  const { ago, ...rest } = c;
  const slaHours = SLA_HOURS[c.category];
  const created = at(-ago);
  const slaDue = new Date(Date.parse(created) + slaHours * 3_600_000).toISOString();
  return { id: uid(KR.caseFile, n), legalEntityId: LEGAL_ENTITY.id, currency: "USD", slaHours, slaDue, slaBreached: c.status === "OPEN" && slaDue < at(0), createdAt: created, version: c.status === "CLOSED" ? 3 : 1, ...rest };
}
const dedItem1 = extraItems.get(deductionItem.get(uid(KR.deduction, 1))!)!;
export const cases = table<CollectionCase>([
  caseFile(1, { customerId: SUMMIT.id, category: "DISPUTE", owner: TEAM.ar, priority: "NORMAL", status: "OPEN", reason: "Contract price on CP-400 pumps billed at list", openItemId: LEGACY.summit, documentNumber: "INV-L48812", attachedAmount: "3600.00", ago: 3.5 }),
  caseFile(2, { customerId: COASTAL.id, category: "DISPUTE", owner: TEAM.credit, priority: "HIGH", status: "OPEN", reason: "Balance confirmation: customer states retention is not yet due", openItemId: LEGACY.coastal, documentNumber: "INV-L48735", attachedAmount: "18480.00", ago: 4 }),
  caseFile(3, { customerId: METRO.id, category: "COLLECTION", owner: TEAM.collector, priority: "HIGH", status: "OPEN", reason: "Promise to pay broken; second reminder sent, no reply", openItemId: LEGACY.metro, documentNumber: "INV-L48790", attachedAmount: "6940.00", ago: 6 }),
  caseFile(4, { customerId: SUMMIT.id, category: "DEDUCTION", owner: TEAM.ar, priority: "NORMAL", status: "OPEN", reason: "Carrier damage claim for the cracked pump housing", openItemId: dedItem1.id, documentNumber: dedItem1.documentNumber, attachedAmount: "1260.00", ago: 8 }),
  caseFile(5, { customerId: PRAIRIE.id, category: "COLLECTION", owner: TEAM.me, priority: "HIGH", status: "OPEN", reason: "Check CHK 11092 returned unpaid; call the AP manager", openItemId: inv(12).id, documentNumber: num(12), attachedAmount: dec(gross(12)), ago: 0.6 }),
  caseFile(6, { customerId: BLUEWATER.id, category: "COLLECTION", owner: TEAM.collector, priority: "LOW", status: "CLOSED", reason: "Remittance advice for the opening invoice", openItemId: LEGACY.bluewater, documentNumber: "INV-L48701", attachedAmount: "21300.00", closeNote: "Paid by ACH", closedAt: at(-88), ago: 95 }),
]);

// ---- Balance confirmations ----

/** A customer's balance at the end of a date, over every item. */
export function balanceAsOf(customerId: string, date: string): number {
  return r2(allItems().filter((i) => i.customerId === customerId && i.side !== "CREDIT").reduce((s, i) => s + openAsOf(i, date), 0));
}

export const confirmationRuns = table<Omit<ConfirmationRun, "confirmations">>([]);
export const confirmations = table<Confirmation>([]);
let confSeq = 0;
export function selectConfirmations(runId: string, asOf: string, threshold: number, sample: number | null, includeZero: boolean): Confirmation[] {
  let picked = CUSTOMER_IDS.map((id) => ({ id, balance: balanceAsOf(id, asOf) })).filter((c) => (includeZero ? c.balance >= 0 : c.balance > 0) && (c.balance >= threshold || (includeZero && c.balance === 0)));
  if (sample !== null) picked = picked.sort((a, b) => b.balance - a.balance).slice(0, sample);
  return picked.map((c) => confirmations.insert({ id: uid(KR.confirmation, ++confSeq), runId, legalEntityId: LEGAL_ENTITY.id, customerId: c.id, currency: "USD", ourBalance: dec(c.balance), status: "NO_REPLY" }));
}
const CUSTOMER_IDS = [BLUEWATER.id, HARLAN.id, SUMMIT.id, COASTAL.id, PRAIRIE.id, METRO.id];
{
  const now = new Date(`${day(0)}T00:00:00Z`);
  const quarterEnd = new Date(Date.UTC(now.getUTCFullYear(), Math.floor(now.getUTCMonth() / 3) * 3, 0)).toISOString().slice(0, 10);
  const runId = uid(KR.confirmRun, 1);
  confirmationRuns.insert({ id: runId, legalEntityId: LEGAL_ENTITY.id, asOfDate: quarterEnd, threshold: "10000.00", sampleSize: null, includeZeroBalances: false, reminderDays: 14, createdAt: atIso(addDays(quarterEnd, 4), 14), counts: { selected: 0, agreed: 0, disagreed: 0, noReply: 0 } });
  const replied = addDays(quarterEnd, 12);
  for (const c of selectConfirmations(runId, quarterEnd, 10000, null, false)) {
    if (c.customerId === COASTAL.id) {
      const theirs = r2(Number(c.ourBalance) - 18480);
      confirmations.update(c.id, { status: "DISAGREED", customerBalance: dec(theirs), difference: "18480.00", comment: "Retention of 18,480.00 is not due until acceptance", repliedAt: atIso(replied, 16), caseId: uid(KR.caseFile, 2) });
    } else if (c.customerId !== SUMMIT.id) {
      confirmations.update(c.id, { status: "AGREED", customerBalance: c.ourBalance, difference: "0.00", repliedAt: atIso(addDays(quarterEnd, 7 + (CUSTOMER_IDS.indexOf(c.customerId) % 4)), 11) });
    }
  }
}
export function runWithCounts(runId: string): ConfirmationRun | undefined {
  const run = confirmationRuns.get(runId);
  if (!run) return undefined;
  const list = confirmations.all().filter((c) => c.runId === runId);
  return { ...run, counts: { selected: list.length, agreed: list.filter((c) => c.status === "AGREED").length, disagreed: list.filter((c) => c.status === "DISAGREED").length, noReply: list.filter((c) => c.status === "NO_REPLY").length }, confirmations: list };
}

// ---- Down payments and deposits ----

const orderNet = (n: number): number => Number(orders.get(uid(K.order, n))?.netTotal ?? 0);
export const downPayments = table<DownPayment>([
  { id: uid(KR.downPayment, 1), legalEntityId: LEGAL_ENTITY.id, customerId: HARLAN.id, salesOrderId: uid(K.order, 2), kind: "DOWN_PAYMENT", currency: "USD", amount: dec(r2(orderNet(2) * 0.3)), clearedAmount: "0.00", remaining: dec(r2(orderNet(2) * 0.3)), status: "REQUESTED", receivedDate: null, createdAt: at(-0.9), version: 1, clearings: [] },
  { id: uid(KR.downPayment, 2), legalEntityId: LEGAL_ENTITY.id, customerId: COASTAL.id, salesOrderId: uid(K.order, 4), kind: "DOWN_PAYMENT", currency: "USD", amount: dec(r2(orderNet(4) * 0.25)), clearedAmount: "0.00", remaining: dec(r2(orderNet(4) * 0.25)), status: "RECEIVED", receivedDate: day(-2), createdAt: at(-3), version: 2, clearings: [] },
  { id: uid(KR.downPayment, 3), legalEntityId: LEGAL_ENTITY.id, customerId: SUMMIT.id, salesOrderId: null, kind: "SECURITY_DEPOSIT", currency: "USD", amount: "15000.00", clearedAmount: "0.00", remaining: "15000.00", status: "RECEIVED", receivedDate: day(-108), createdAt: at(-110), version: 2, clearings: [] },
  { id: uid(KR.downPayment, 4), legalEntityId: LEGAL_ENTITY.id, customerId: METRO.id, salesOrderId: uid(K.order, 6), kind: "DOWN_PAYMENT", currency: "USD", amount: dec(r2(orderNet(6) * 0.15)), clearedAmount: "0.00", remaining: "0.00", status: "CANCELLED", receivedDate: null, createdAt: at(-19), version: 2, clearings: [] },
]);

// ---- Lockbox and bank statement files ----

export const lockboxBatches = table<Omit<LockboxBatch, "payments">>([]);
export const statementBatches = table<Omit<BankStatementBatch, "payments">>([]);
/** The payments each file created, by batch id; reads show them as they are now. */
export const batchPayments = new Map<string, string[]>();
let batchSeq = 0;
export const nextBatchId = (): string => uid(KR.batch, ++batchSeq);
function lockbox(received: number, list: Payment[]): void {
  const id = nextBatchId();
  batchPayments.set(id, list.map((p) => p.id));
  lockboxBatches.insert({ id, legalEntityId: LEGAL_ENTITY.id, fileSha256: hash(`lockbox-${id}`), receivedDate: day(-received), accountCount: new Set(list.map((p) => p.customerId)).size, paymentCount: list.length, importedAt: atIso(day(-received), 7) });
}
function statement(booked: number, list: Payment[], skipped: number): void {
  const id = nextBatchId();
  batchPayments.set(id, list.map((p) => p.id));
  statementBatches.insert({ id, legalEntityId: LEGAL_ENTITY.id, fileSha256: hash(`camt-${id}`), statementCount: 1, paymentCount: list.length, duplicateCount: 0, skippedCount: skipped, importedAt: atIso(day(-booked + 1), 6) });
}
lockbox(95, [PAY.prairieL]);
lockbox(52, [PAY.metro14]);
lockbox(9, [PAY.prairieNsf, PAY.metroCheck]);
lockbox(6, [PAY.harlan11]);
statement(88, [PAY.bluewaterL], 9);
statement(40, [PAY.summit13], 7);
statement(15, [PAY.summitWire], 11);
statement(12, [PAY.coastal10], 6);
statement(1, [PAY.bluewater7, PAY.unidentified], 8);

export const paymentsOf = (batchId: string): Payment[] => (batchPayments.get(batchId) ?? []).map((id) => payments.get(id)).filter((p): p is Payment => p !== undefined);

// ---- Collections worklist ----

/** Average days paid after the due date over the last year, from the payment history. */
const AVG_DAYS_LATE: Record<string, number> = { [BLUEWATER.id]: 3, [HARLAN.id]: 8, [SUMMIT.id]: 21, [COASTAL.id]: 34, [PRAIRIE.id]: 16, [METRO.id]: 38 };

export function riskOf(customerId: string, asOf: string): CustomerRisk | undefined {
  const debit = allItems().filter((i) => i.customerId === customerId && i.side !== "CREDIT" && openAsOf(i, asOf) !== 0);
  if (debit.length === 0) return undefined;
  // Gross of what the customer owes; payments on account are shown on the statement, not netted here.
  const total = r2(debit.reduce((s, i) => s + openAsOf(i, asOf), 0));
  const overdue = r2(debit.filter((i) => i.dueDate < asOf).reduce((s, i) => s + openAsOf(i, asOf), 0));
  const oldest = Math.max(0, ...debit.filter((i) => i.dueDate < asOf).map((i) => daysBetween(i.dueDate, asOf)));
  const dunningLevel = Math.max(0, ...debit.map((i) => i.dunningLevel));
  const openDisputes = disputes.all().filter((d) => d.customerId === customerId && (d.status === "OPEN" || d.status === "UNDER_REVIEW")).length;
  const broken = brokenPromises(customerId, asOf);
  const avgLate = AVG_DAYS_LATE[customerId] ?? 0;
  const share = total > 0 ? Math.min(1, overdue / total) : 0;
  const riskScore = Math.min(100, Math.round(share * 40 + (Math.min(oldest, 180) / 180) * 25 + dunningLevel * 6 + openDisputes * 4 + broken * 10 + (Math.min(avgLate, 60) / 60) * 10));
  return { customerId, currency: "USD", totalOpen: dec(total), overdue: dec(overdue), oldestDays: oldest, avgDaysLate: avgLate, dunningLevel, openDisputes, brokenPromises: broken, riskScore };
}
export const worklist = (asOf: string): CustomerRisk[] =>
  CUSTOMER_IDS.map((id) => riskOf(id, asOf))
    .filter((r): r is CustomerRisk => r !== undefined)
    .sort((a, b) => b.riskScore - a.riskScore || Number(b.overdue) - Number(a.overdue));

export const customerName = (id: string | null | undefined): string => (id ? (customerById(id)?.name ?? "") : "");
