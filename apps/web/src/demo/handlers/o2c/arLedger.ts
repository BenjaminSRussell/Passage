// Receivables state that is not a billing invoice: the opening items the migration loader brought over at cutover,
// interest and dunning-fee items, deductions being worked, customer payments and each item's dunning state.
// Billing invoices stay in model.ts; arModel.ts joins the two into open items. This file imports nothing from
// model.ts, so the credit exposure there can read it without an import cycle.
import type { OpenItem, Payment } from "../../../modules/receivables/api/client";
import { CUSTOMERS, LEGAL_ENTITY } from "../../company";
import { at, day, dec, table, uid } from "../../store";

// uid() kinds for receivables documents.
export const KR = {
  legacy: 130,
  payment: 131,
  extra: 132,
  dispute: 133,
  promise: 134,
  run: 135,
  letter: 136,
  interest: 137,
  caseFile: 138,
  confirmRun: 139,
  confirmation: 140,
  downPayment: 141,
  clearing: 142,
  deduction: 143,
  batch: 144,
} as const;

export const [BLUEWATER, HARLAN, SUMMIT, COASTAL, PRAIRIE, METRO] = CUSTOMERS;

/** The legacy system the opening items came from, and the cutover day. */
export const IMPORT_SOURCE = "LEGACY-ERP";
export const CUTOVER_DAYS_AGO = 122;

/** Open amount of each opening item when it was loaded, by id, for the import summary. */
export const openAtImport = new Map<string, number>();

function legacy(n: number, customerId: string, number: string, docAgo: number, termDays: number, amount: number, loaded: number, openNow = loaded, extra: Partial<OpenItem> = {}): OpenItem {
  openAtImport.set(uid(KR.legacy, n), loaded);
  return {
    id: uid(KR.legacy, n),
    legalEntityId: LEGAL_ENTITY.id,
    customerId,
    sourceType: "INVOICE",
    documentNumber: number,
    documentDate: day(-docAgo),
    dueDate: day(-docAgo + termDays),
    currency: "USD",
    amount: dec(amount),
    openAmount: dec(openNow),
    cashDiscountAmount: "0.00",
    dunningLevel: 0,
    dunningBlock: false,
    status: openNow > 0 ? (openNow < amount ? "PARTIAL" : "OPEN") : "CLEARED",
    version: 1,
    side: "DEBIT",
    importSource: IMPORT_SOURCE,
    legacyKey: `AR/${number.replace(/\D/g, "")}`,
    ...extra,
  };
}

/** Opening items as imported; their open amounts move as Passage settles them. */
export const LEGACY = {
  summit: uid(KR.legacy, 1),
  coastal: uid(KR.legacy, 2),
  metro: uid(KR.legacy, 3),
  bluewater: uid(KR.legacy, 4),
  prairie: uid(KR.legacy, 5),
};

/**
 * Every receivables item that is not a billing invoice. Bluewater and Prairie paid their opening items after
 * cutover (the payments are seeded in arModel.ts).
 */
export const extraItems = table<OpenItem>([
  // Dunning levels match the issued runs in arModel.ts: both reached level 2.
  legacy(1, SUMMIT.id, "INV-L48812", 128, 30, 46850, 46850, 46850, { dunningLevel: 2 }),
  // Coastal paid 80% on delivery; the 20% retention falls due on the plant's acceptance certificate.
  legacy(2, COASTAL.id, "INV-L48735", 141, 60, 92400, 18480, 18480, { dunningBlock: true }),
  legacy(3, METRO.id, "INV-L48790", 135, 30, 6940, 6940, 6940, { dunningLevel: 2 }),
  legacy(4, BLUEWATER.id, "INV-L48701", 150, 30, 21300, 21300, 0),
  legacy(5, PRAIRIE.id, "INV-L48722", 146, 30, 12615, 12615, 0),
]);

/** Customer payments, newest first when listed. Seeded by arModel.ts once invoice numbers exist. */
export const payments = table<Payment>([]);

/** Dunning state of billing-invoice items, by open item id; extra items keep theirs on the row. */
export interface DunningState {
  level: number;
  block: boolean;
  lastLetter?: string;
}
export const invoiceDunning = new Map<string, DunningState>();

export const r2 = (n: number): number => Math.round(n * 100) / 100;
const daysBetween = (from: string, to: string): number => Math.round((Date.parse(to) - Date.parse(from)) / 86_400_000);

/** What the receivables ledger adds to a customer's credit exposure beyond the billing invoices. */
export interface ArExtras {
  open: number;
  overdue: number;
  oldestDays: number;
  /** Payments on account; undefined until the payments are seeded. */
  unapplied?: number;
  dunningLevel: number;
}

export function arExtras(customerId: string, invoiceIds: string[] = []): ArExtras {
  const today = day(0);
  const mine = extraItems.all().filter((i) => i.customerId === customerId && i.side !== "CREDIT" && Number(i.openAmount) > 0);
  const onAccount = payments.all().filter((p) => p.customerId === customerId && p.status === "ON_ACCOUNT");
  const levels = [...mine.map((i) => i.dunningLevel), ...invoiceIds.map((id) => invoiceDunning.get(id)?.level ?? 0)];
  return {
    open: r2(mine.reduce((s, i) => s + Number(i.openAmount), 0)),
    overdue: r2(mine.filter((i) => i.dueDate < today).reduce((s, i) => s + Number(i.openAmount), 0)),
    oldestDays: Math.max(0, ...mine.map((i) => daysBetween(i.documentDate, today))),
    unapplied: payments.all().length === 0 ? undefined : r2(onAccount.reduce((s, p) => s + Number(p.unappliedAmount), 0)),
    dunningLevel: Math.max(0, ...levels),
  };
}

export const importedAt = at(-CUTOVER_DAYS_AGO);
