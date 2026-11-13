// Receivables service (/api/v1/receivables) for the demo: open items and aging, customer payments with matching
// and cash application, lockbox and camt.053 imports, the collections worklist, customer statements, balance
// snapshots and the opening-item imports. The data lives in arModel.ts; collections.ts has disputes, promises,
// dunning, interest, cases, confirmations, down payments and deductions.
import type {
  AgingTotal,
  ApplyInput,
  BalanceSnapshot,
  BankStatementBatch,
  LockboxBatch,
  MatchCandidate,
  MatchResult,
  OpenItem,
  OpeningImportSummary,
  Payment,
  PaymentInput,
  SettlementRecord,
  StatementLine,
} from "../../../modules/receivables/api/client";
import { BUCKETS } from "../../../modules/receivables/api/client";
import { CUSTOMERS, LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { at, dec, day, matchesQ, uid } from "../../store";
import { IMPORT_SOURCE, KR, importedAt, openAtImport, r2 } from "./arLedger";
import {
  allItems,
  batchPayments,
  bucketOf,
  daysBetween,
  fakeSha256,
  findItem,
  lockboxBatches,
  nextBatchId,
  openAsOf,
  payments,
  paymentsOf,
  settleItem,
  unsettleItem,
  statementBatches,
  worklist,
} from "./arModel";

const BASE = "/api/v1/receivables";

export const notFound = (what: string) => reply.problem(404, "AR-0404", `${what} not found`);
export const invalid = (detail: string, errors?: Record<string, string>) => reply.problem(400, "AR-0001", detail, errors);
/** The version an If-Match header names, or undefined when there is none. */
export const ifMatch = (headers: Headers): number | undefined => {
  const v = Number(/\d+/.exec(headers.get("If-Match") ?? "")?.[0]);
  return Number.isFinite(v) && v > 0 ? v : undefined;
};
export const stale = (what: string) => reply.problem(412, "AR-0412", `The ${what} changed since you opened it.`);
const limitOf = (query: URLSearchParams, fallback = 100) => Math.max(1, Math.min(500, Number(query.get("limit") ?? fallback) || fallback));

// ---- Open items and aging ----

route("GET", `${BASE}/open-items`, ({ query }) => {
  const all = query.get("status") === "all";
  const customer = query.get("customer");
  const source = query.get("importSource");
  const items: OpenItem[] = allItems().filter(
    (i) => (all || Number(i.openAmount) !== 0) && (!customer || i.customerId === customer) && (!source || i.importSource === source) && matchesQ(query, i.documentNumber),
  );
  return { items: items.slice(0, limitOf(query, 500)) };
});

route("GET", `${BASE}/open-items/{id}`, ({ params }) => findItem(params.id) ?? notFound("Open item"));

route("GET", `${BASE}/aging`, ({ query }) => {
  const asOf = query.get("asOf") || day(0);
  const sums = new Map<string, number>();
  for (const item of allItems()) {
    const open = item.side === "CREDIT" ? (item.documentDate <= asOf ? Number(item.openAmount) : 0) : openAsOf(item, asOf);
    if (open === 0) continue;
    const key = `${item.currency}|${bucketOf(item, asOf)}`;
    sums.set(key, r2((sums.get(key) ?? 0) + open));
  }
  const totals: AgingTotal[] = [];
  for (const currency of new Set([...sums.keys()].map((k) => k.split("|")[0]))) {
    for (const bucket of BUCKETS) {
      const amount = sums.get(`${currency}|${bucket}`);
      if (amount !== undefined) totals.push({ currency, bucket, amount: dec(amount) });
    }
  }
  return { asOf, totals };
});

route("GET", `${BASE}/open-item-imports`, () => {
  const loaded = allItems().filter((i) => i.importSource === IMPORT_SOURCE);
  const debit = loaded.filter((i) => i.side !== "CREDIT");
  const credit = loaded.filter((i) => i.side === "CREDIT");
  const sum = (list: OpenItem[]) => r2(list.reduce((s, i) => s + (openAtImport.get(i.id) ?? Number(i.openAmount)), 0));
  const summary: OpeningImportSummary = {
    importSource: IMPORT_SOURCE,
    legalEntityId: LEGAL_ENTITY.id,
    currency: "USD",
    itemCount: loaded.length,
    debitCount: debit.length,
    creditCount: credit.length,
    debitOpen: dec(sum(debit)),
    creditOpen: dec(sum(credit)),
    netOpen: dec(sum(debit) + sum(credit)),
    lastImportedAt: importedAt,
  };
  return { items: [summary] };
});

// ---- Payments ----

route("GET", `${BASE}/payments`, ({ query }) => {
  const status = query.get("status");
  const customer = query.get("customer");
  const rows = payments
    .all()
    .filter((p) => (!status || p.status === status) && (!customer || p.customerId === customer) && matchesQ(query, p.payerName, p.sourceRef, p.amount))
    .sort((a, b) => b.receivedDate.localeCompare(a.receivedDate) || (b.sourceRef ?? "").localeCompare(a.sourceRef ?? ""));
  return { items: rows.slice(0, limitOf(query)) };
});

route("GET", `${BASE}/payments/{id}`, ({ params }) => payments.get(params.id) ?? notFound("Payment"));

let manualSeq = 0;
route("POST", `${BASE}/payments`, ({ body }) => {
  const input = body as PaymentInput;
  if (!input || !/^\d+(\.\d{1,6})?$/.test(input.amount ?? "") || Number(input.amount) <= 0) return invalid("Enter an amount above zero.", { amount: "Enter an amount above zero, such as 1250.00." });
  if (input.customerId && !CUSTOMERS.some((c) => c.id === input.customerId)) return invalid("Unknown customer.", { customerId: "Choose a customer or leave it empty." });
  const payment: Payment = {
    id: uid(KR.payment, 500 + ++manualSeq),
    legalEntityId: input.legalEntityId || LEGAL_ENTITY.id,
    customerId: input.customerId ?? null,
    paymentMode: input.paymentMode,
    source: "MANUAL",
    sourceRef: input.sourceRef,
    amount: dec(Number(input.amount)),
    unappliedAmount: dec(Number(input.amount)),
    currency: input.currency || "USD",
    receivedDate: input.receivedDate,
    valueDate: input.valueDate,
    payerName: input.payerName,
    payerAccountHash: input.payerAccount ? fakeSha256(input.payerAccount) : null,
    shortPayReason: input.shortPayReason,
    remittance: (input.remittance ?? []).map((r) => ({ reference: r.reference, amount: dec(Number(r.amount || 0)) })),
    status: "RECEIVED",
    version: 1,
  };
  return reply.created(payments.insert(payment));
});

const close = (a: number, b: number) => Math.abs(a - b) < 0.005;
const words = (s: string) => s.toUpperCase().replace(/[^A-Z0-9 ]/g, " ").split(/\s+/).filter((w) => w.length >= 4);

/** Scores open items against a payment (BR-30.1): R1 reference, R2 amount, R4/R5 payer seen before, R6 due date, R7 short pay with a reason. */
export function scorePayment(p: Payment): MatchResult {
  const items = allItems().filter((i) => i.side !== "CREDIT" && Number(i.openAmount) > 0 && i.currency === p.currency && (!p.customerId || i.customerId === p.customerId));
  const amount = Number(p.unappliedAmount);
  const refs = new Set(p.remittance.map((r) => r.reference.trim().toUpperCase()));
  const history = payments.all().filter((x) => x.id !== p.id && x.status === "APPLIED");
  const payerWords = words(p.payerName ?? "");
  const score = (group: OpenItem[]): MatchCandidate => {
    const rules: string[] = [];
    let total = 0;
    const open = r2(group.reduce((s, i) => s + Number(i.openAmount), 0));
    const customerId = group[0].customerId;
    if (group.every((i) => refs.has(i.documentNumber.toUpperCase()))) {
      total += 50;
      rules.push("R1");
    }
    if (close(open, amount)) {
      total += 35;
      rules.push("R2");
    }
    if (p.payerAccountHash && history.some((x) => x.customerId === customerId && x.payerAccountHash === p.payerAccountHash)) {
      total += 10;
      rules.push("R4");
    } else if (payerWords.some((w) => words(CUSTOMERS.find((c) => c.id === customerId)?.name ?? "").includes(w))) {
      total += 25;
      rules.push("R5");
    }
    if (group.every((i) => Math.abs(daysBetween(i.dueDate, p.receivedDate)) <= 30)) {
      total += 5;
      rules.push("R6");
    }
    if (amount < open && p.shortPayReason && rules.includes("R1")) {
      total += 15;
      rules.push("R7");
    }
    return { openItemIds: group.map((i) => i.id), score: Math.min(100, total), rules };
  };
  const candidates: MatchCandidate[] = items.map((i) => score([i]));
  const referenced = items.filter((i) => refs.has(i.documentNumber.toUpperCase()));
  if (referenced.length > 1) candidates.push(score(referenced));
  const ranked = candidates.filter((c) => c.score >= 40).sort((a, b) => b.score - a.score).slice(0, 5);
  const [top, next] = ranked;
  const decision: MatchResult["decision"] = top && top.score >= 85 && top.score - (next?.score ?? 0) >= 20 ? "AUTO_APPLY" : top && top.score >= 60 ? "SUGGESTED" : "ON_ACCOUNT";
  return { decision, candidates: ranked };
}

route("POST", `${BASE}/payments/{id}:match`, ({ params }) => {
  const p = payments.get(params.id);
  if (!p) return notFound("Payment");
  const result = scorePayment(p);
  if (result.decision === "SUGGESTED" && p.status === "RECEIVED") payments.update(p.id, { status: "SUGGESTED" });
  return result;
});

const APPLIABLE = new Set(["RECEIVED", "SUGGESTED", "ON_ACCOUNT"]);

/** The live applications of each payment applied in this session, so :unapply can give them back. */
const applications = new Map<string, { openItemId: string; amount: number; total: number }[]>();

route("POST", `${BASE}/payments/{id}:apply`, ({ params, headers, body }) => {
  const p = payments.get(params.id);
  if (!p) return notFound("Payment");
  const v = ifMatch(headers);
  if (v !== undefined && v !== p.version) return stale("payment");
  if (!APPLIABLE.has(p.status)) return reply.problem(409, "AR-0010", "This payment has nothing left to apply.");
  const input = body as ApplyInput;
  const lines = input?.settlements ?? [];
  const applied = r2(lines.reduce((s, l) => s + Number(l.amount), 0));
  const onAccount = Number(input?.onAccount ?? 0);
  if (!close(applied + onAccount, Number(p.unappliedAmount))) return invalid(`The settlements and the amount on account must add up to the ${p.unappliedAmount} unapplied.`);
  let customerId = p.customerId ?? null;
  for (const l of lines) {
    const item = findItem(l.openItemId);
    if (!item) return notFound(`Open item ${l.openItemId}`);
    if (item.side === "CREDIT") return reply.problem(422, "AR-CREDIT-ITEM", `${item.documentNumber} is a credit item; a payment applies to debits only.`);
    if (customerId && item.customerId !== customerId) return reply.problem(422, "AR-0011", `${item.documentNumber} belongs to another customer.`);
    const total = Number(l.amount) + Number(l.cashDiscount ?? 0) + Number(l.writeOff ?? 0);
    if (total - Number(item.openAmount) > 0.005) return reply.problem(422, "AR-0012", `${item.documentNumber} has only ${item.openAmount} open.`);
    customerId = item.customerId;
  }
  const records: SettlementRecord[] = lines.map((l) => {
    const total = r2(Number(l.amount) + Number(l.cashDiscount ?? 0) + Number(l.writeOff ?? 0));
    const after = settleItem(l.openItemId, total, day(0));
    applications.set(p.id, [...(applications.get(p.id) ?? []), { openItemId: l.openItemId, amount: Number(l.amount), total }]);
    return { openItemId: l.openItemId, amount: dec(Number(l.amount)), cashDiscount: dec(Number(l.cashDiscount ?? 0)), writeOff: dec(Number(l.writeOff ?? 0)), openAfter: after?.openAmount ?? "0.00", itemStatus: after?.status ?? "CLEARED" };
  });
  const next = payments.update(p.id, {
    customerId,
    unappliedAmount: dec(onAccount),
    status: onAccount > 0.005 ? "ON_ACCOUNT" : "APPLIED",
    remittance: p.remittance.length > 0 ? p.remittance : records.map((r) => ({ reference: findItem(r.openItemId)?.documentNumber ?? r.openItemId, amount: r.amount })),
    version: p.version + 1,
  })!;
  return reply.ok({ payment: next, settlements: records }, { ETag: `"${next.version}"` });
});

route("POST", `${BASE}/payments/{id}:unapply`, ({ params, headers }) => {
  const p = payments.get(params.id);
  if (!p) return notFound("Payment");
  const v = ifMatch(headers);
  if (v !== undefined && v !== p.version) return stale("payment");
  const live = applications.get(p.id) ?? [];
  if (live.length === 0) return reply.problem(409, "AR-0010", `The payment has no application to reverse (status ${p.status}).`);
  const reversed = live.map((a, i) => {
    const after = unsettleItem(a.openItemId, a.total, day(0));
    return { settlementId: uid(KR.payment, 900 + i), openItemId: a.openItemId, amount: dec(a.amount), itemAmount: dec(a.total), openAfter: after?.openAmount ?? dec(a.total), itemStatus: after?.status ?? "OPEN" };
  });
  applications.delete(p.id);
  const back = r2(live.reduce((sum, a) => sum + a.amount, 0));
  const next = payments.update(p.id, { unappliedAmount: dec(r2(Number(p.unappliedAmount) + back)), status: "RECEIVED", version: p.version + 1 })!;
  return reply.ok({ payment: next, reversed }, { ETag: `"${next.version}"` });
});

// ---- Lockbox (BAI2) and bank statements (camt.053) ----

const knownFiles = new Set<string>();

/** The customer a payer name or reference points at: an account number, or a distinctive word of the name. */
function identify(text: string): string | null {
  const upper = text.toUpperCase();
  const byNumber = CUSTOMERS.find((c) => upper.includes(c.number));
  if (byNumber) return byNumber.id;
  const t = words(text);
  return CUSTOMERS.find((c) => words(c.name).some((w) => t.includes(w) && !["SUPPLY", "GROUP", "PARTNERS", "SYSTEMS"].includes(w)))?.id ?? null;
}

function newPayment(n: number, p: Pick<Payment, "paymentMode" | "source" | "sourceRef" | "amount" | "receivedDate" | "payerName" | "customerId" | "remittance">): Payment {
  return payments.insert({ id: uid(KR.payment, 700 + n), legalEntityId: LEGAL_ENTITY.id, unappliedAmount: p.amount, currency: "USD", valueDate: p.receivedDate, payerAccountHash: null, shortPayReason: null, status: "RECEIVED", version: 1, ...p });
}
let importSeq = 0;

route("GET", `${BASE}/lockbox/imports`, () => ({
  items: lockboxBatches
    .all()
    .sort((a, b) => (b.importedAt ?? "").localeCompare(a.importedAt ?? ""))
    .map((b): LockboxBatch => ({ ...b, payments: paymentsOf(b.id) })),
}));

route("POST", `${BASE}/lockbox/imports`, async ({ body }) => {
  const input = body as { legalEntityId: string; receivedDate: string; content: string; typeCodes?: string[] };
  const content = String(input?.content ?? "");
  const sha = fakeSha256(content);
  if (knownFiles.has(sha) || lockboxBatches.all().some((b) => b.fileSha256 === sha)) return reply.problem(409, "AR-0003", "This file was already imported.");
  const lines = content.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  if (!lines[0]?.startsWith("01,")) return reply.problem(400, "AR-0004", "Line 1: a BAI2 file starts with a 01 file header record.");
  const codes = input.typeCodes && input.typeCodes.length > 0 ? input.typeCodes : ["115", "301"];
  const created: Payment[] = [];
  for (const [index, line] of lines.entries()) {
    if (!line.startsWith("16,")) continue;
    const [, code, cents, , bankRef, customerRef, ...text] = line.replace(/\/$/, "").split(",");
    if (!/^\d+$/.test(cents ?? "")) return reply.problem(400, "AR-0004", `Line ${index + 1}: malformed BAI2 record type 16`);
    if (!codes.includes(code)) continue;
    const note = text.join(",");
    created.push(newPayment(++importSeq, { paymentMode: "CHECK", source: "LOCKBOX", sourceRef: bankRef || customerRef || null, amount: dec(Number(cents) / 100), receivedDate: input.receivedDate || day(0), payerName: note || null, customerId: identify(`${customerRef} ${note}`), remittance: customerRef ? [{ reference: customerRef, amount: dec(Number(cents) / 100) }] : [] }));
  }
  knownFiles.add(sha);
  const id = nextBatchId();
  batchPayments.set(id, created.map((p) => p.id));
  const batch = lockboxBatches.insert({ id, legalEntityId: input.legalEntityId || LEGAL_ENTITY.id, fileSha256: sha, receivedDate: input.receivedDate || day(0), accountCount: new Set(created.map((p) => p.customerId ?? "")).size, paymentCount: created.length, importedAt: at(0) });
  return reply.created({ ...batch, payments: created } satisfies LockboxBatch);
});

route("GET", `${BASE}/bank-statements/imports`, () => ({
  items: statementBatches
    .all()
    .sort((a, b) => (b.importedAt ?? "").localeCompare(a.importedAt ?? ""))
    .map((b): BankStatementBatch => ({ ...b, payments: paymentsOf(b.id) })),
}));

const tag = (xml: string, name: string): string | undefined => new RegExp(`<${name}[^>]*>([^<]*)</${name}>`).exec(xml)?.[1]?.trim();

route("POST", `${BASE}/bank-statements/imports`, ({ body }) => {
  const input = body as { legalEntityId: string; content: string };
  const content = String(input?.content ?? "");
  const sha = fakeSha256(content);
  if (knownFiles.has(sha) || statementBatches.all().some((b) => b.fileSha256 === sha)) return reply.problem(409, "AR-0003", "This file was already imported.");
  if (!/<Document[\s>]/.test(content) || !/<Stmt>/.test(content)) return reply.problem(400, "AR-0004", "Line 1: not a camt.053 document (no BkToCstmrStmt/Stmt element).");
  const statements = content.split("<Stmt>").length - 1;
  const entries = content.split("<Ntry>").slice(1).map((e) => e.split("</Ntry>")[0]);
  const seenRefs = new Set(payments.all().map((p) => p.sourceRef));
  const created: Payment[] = [];
  let duplicates = 0;
  let skipped = 0;
  for (const e of entries) {
    const booked = (tag(e, "Cd") ?? tag(e, "Sts") ?? "BOOK") === "BOOK";
    if (tag(e, "CdtDbtInd") !== "CRDT" || !booked || /<RvslInd>true/.test(e)) {
      skipped++;
      continue;
    }
    const ref = tag(e, "AcctSvcrRef") ?? tag(e, "EndToEndId") ?? null;
    if (ref && seenRefs.has(ref)) {
      duplicates++;
      continue;
    }
    const amount = Number(tag(e, "Amt") ?? "0");
    const payer = tag(e, "Nm") ?? null;
    const remittance = tag(e, "Ustrd");
    const date = tag(e, "Dt") ?? day(0);
    created.push(newPayment(++importSeq, { paymentMode: "WIRE", source: "BANK_STATEMENT", sourceRef: ref, amount: dec(amount), receivedDate: date, payerName: payer, customerId: identify(`${payer ?? ""} ${remittance ?? ""}`), remittance: remittance ? [{ reference: remittance, amount: dec(amount) }] : [] }));
  }
  knownFiles.add(sha);
  const id = nextBatchId();
  batchPayments.set(id, created.map((p) => p.id));
  const batch = statementBatches.insert({ id, legalEntityId: input.legalEntityId || LEGAL_ENTITY.id, fileSha256: sha, statementCount: Math.max(1, statements), paymentCount: created.length, duplicateCount: duplicates, skippedCount: skipped, importedAt: at(0) });
  return reply.created({ ...batch, payments: created } satisfies BankStatementBatch);
});

// ---- Worklist, statements and balance snapshots ----

route("GET", `${BASE}/worklist`, ({ query }) => {
  const asOf = query.get("asOf") || day(0);
  return { asOf, items: worklist(asOf) };
});

route("GET", `${BASE}/statements`, ({ query }) => {
  const customer = query.get("customer") ?? "";
  if (!CUSTOMERS.some((c) => c.id === customer)) return notFound("Customer");
  const asOf = query.get("asOf") || day(0);
  const mine = allItems()
    .filter((i) => i.customerId === customer && i.side !== "CREDIT" && openAsOf(i, asOf) !== 0)
    .sort((a, b) => a.documentDate.localeCompare(b.documentDate) || a.documentNumber.localeCompare(b.documentNumber));
  const onAccount = payments.all().filter((p) => p.customerId === customer && p.status === "ON_ACCOUNT" && p.receivedDate <= asOf);
  let running = 0;
  const lines: StatementLine[] = [];
  const buckets = new Map<string, number>();
  for (const i of mine) {
    const open = openAsOf(i, asOf);
    running = r2(running + open);
    const bucket = bucketOf(i, asOf);
    buckets.set(bucket, r2((buckets.get(bucket) ?? 0) + open));
    lines.push({ sourceType: i.sourceType, documentNumber: i.documentNumber, documentDate: i.documentDate, dueDate: i.dueDate, amount: i.amount, openAmount: dec(open), runningBalance: dec(running) });
  }
  for (const p of onAccount) {
    running = r2(running - Number(p.unappliedAmount));
    buckets.set("CURRENT", r2((buckets.get("CURRENT") ?? 0) - Number(p.unappliedAmount)));
    lines.push({ sourceType: "PAYMENT", documentNumber: p.sourceRef ?? "Payment", documentDate: p.receivedDate, dueDate: null, amount: dec(-Number(p.amount)), openAmount: dec(-Number(p.unappliedAmount)), runningBalance: dec(running) });
  }
  const aging = BUCKETS.filter((b) => buckets.has(b)).map((b) => ({ currency: "USD", bucket: b, amount: dec(buckets.get(b)!) }));
  return { legalEntityId: query.get("legalEntityId") ?? LEGAL_ENTITY.id, customerId: customer, asOf, sections: lines.length > 0 ? [{ currency: "USD", lines, closingBalance: dec(running), aging }] : [] };
});

let snapshotAt = `${day(0)}T02:00:00.000Z`;
function snapshots(asOf: string): BalanceSnapshot[] {
  return worklist(asOf).map((r) => ({ legalEntityId: LEGAL_ENTITY.id, customerId: r.customerId, currency: r.currency, asOf, totalOpen: r.totalOpen, overdue: r.overdue, oldestItemDays: r.oldestDays, dunningLevel: r.dunningLevel, riskScore: r.riskScore, takenAt: snapshotAt }));
}

route("GET", `${BASE}/aging-snapshots`, ({ query }) => {
  const asOf = query.get("asOf") || day(0);
  const customer = query.get("customer");
  return { items: snapshots(asOf).filter((s) => !customer || s.customerId === customer) };
});

route("POST", `${BASE}/aging-snapshots`, ({ body }) => {
  const asOf = (body as { asOf?: string })?.asOf || day(0);
  const before = JSON.stringify(snapshots(asOf).map((s) => [s.customerId, s.totalOpen]));
  snapshotAt = at(0);
  const rows = snapshots(asOf);
  return { legalEntityId: LEGAL_ENTITY.id, asOf, rows: rows.length, changed: before === JSON.stringify(rows.map((s) => [s.customerId, s.totalOpen])) ? 0 : rows.length };
});
