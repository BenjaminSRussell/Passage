// Receivables collections for the demo (/api/v1/receivables): disputes, promises to pay, dunning runs, interest
// notes, collection cases, balance confirmations, down payments and deposits, and the deductions workbench.
// Every record points at the open items and customers of arModel.ts, and the commands move those items.
import type {
  CaseInput,
  CollectionCase,
  ConfirmationRunInput,
  Deduction,
  DeductionInput,
  Dispute,
  DisputeInput,
  DownPayment,
  DownPaymentInput,
  DunningRun,
  DunningRunInput,
  Exclusion,
  InterestCandidate,
  InterestInput,
  PromiseInput,
  ReplyInput,
  Resolution,
} from "../../../modules/receivables/api/client";
import { CUSTOMERS, LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { at, dec, day, uid } from "../../store";
import { KR, r2 } from "./arLedger";
import {
  SLA_HOURS,
  activeDispute,
  addDays,
  addExtraItem,
  allItems,
  cases,
  confirmationRuns,
  confirmations,
  daysBetween,
  deductionItem,
  deductionNumber,
  deductions,
  disputes,
  downPayments,
  findItem,
  heldByPromise,
  interestAmount,
  interestNotes,
  issueLetter,
  postInterestNote,
  priorMonthEnd,
  promises,
  proposeRun,
  runWithCounts,
  runs,
  selectConfirmations,
  settleItem,
} from "./arModel";
import { ifMatch, invalid, notFound, stale } from "./receivables";

const BASE = "/api/v1/receivables";
const limitOf = (query: URLSearchParams, fallback = 100) => Math.max(1, Math.min(500, Number(query.get("limit") ?? fallback) || fallback));
const DECIMAL = /^\d+(\.\d{1,6})?$/;
const positive = (s: unknown): s is string => typeof s === "string" && DECIMAL.test(s.trim()) && Number(s) > 0;
const conflict = (code: string, detail: string) => reply.problem(409, code, detail);

// ---- Disputes ----

route("GET", `${BASE}/disputes`, ({ query }) => {
  const status = query.get("status");
  const customer = query.get("customer");
  const item = query.get("openItem");
  const rows = disputes
    .all()
    .filter((d) => (!status || (status === "active" ? d.status === "OPEN" || d.status === "UNDER_REVIEW" : d.status === status)) && (!customer || d.customerId === customer) && (!item || d.openItemId === item))
    .sort((a, b) => a.slaDue.localeCompare(b.slaDue));
  // Open work first, nearest SLA first; closed disputes after, newest first.
  const active = rows.filter((d) => d.status === "OPEN" || d.status === "UNDER_REVIEW");
  const closed = rows.filter((d) => !active.includes(d)).reverse();
  return { items: [...active, ...closed].slice(0, limitOf(query)) };
});

route("GET", `${BASE}/disputes/{id}`, ({ params }) => disputes.get(params.id) ?? notFound("Dispute"));

let disputeSeq = 100;
route("POST", `${BASE}/disputes`, ({ body }) => {
  const input = body as DisputeInput;
  const item = findItem(input?.openItemId ?? "");
  if (!item) return notFound("Open item");
  if (!positive(input.disputedAmount)) return invalid("Enter an amount above zero.", { disputedAmount: "Enter an amount above zero, such as 500.00." });
  if (Number(input.disputedAmount) - Number(item.openAmount) > 0.005) return invalid(`The disputed amount is more than the ${item.openAmount} open.`, { disputedAmount: `At most the item's open amount, ${item.openAmount}.` });
  if (activeDispute(item.id)) return conflict("AR-0020", `${item.documentNumber} already has an open dispute.`);
  const created = at(0);
  const dispute: Dispute = {
    id: uid(KR.dispute, ++disputeSeq),
    legalEntityId: input.legalEntityId || LEGAL_ENTITY.id,
    customerId: item.customerId,
    openItemId: item.id,
    documentNumber: item.documentNumber,
    reasonCode: input.reasonCode,
    disputedAmount: dec(Number(input.disputedAmount)),
    currency: item.currency,
    owner: input.owner,
    status: "OPEN",
    slaDue: new Date(Date.parse(created) + 120 * 3_600_000).toISOString(),
    createdAt: created,
    version: 1,
  };
  return reply.created(disputes.insert(dispute));
});

route("POST", `${BASE}/disputes/{id}:review`, ({ params, headers }) => {
  const d = disputes.get(params.id);
  if (!d) return notFound("Dispute");
  const v = ifMatch(headers);
  if (v !== undefined && v !== d.version) return stale("dispute");
  if (d.status !== "OPEN") return conflict("AR-0021", "Only an open dispute can go under review.");
  return disputes.update(d.id, { status: "UNDER_REVIEW", version: d.version + 1 });
});

const RESOLVED: Record<Resolution, Dispute["status"]> = { CREDIT: "RESOLVED_CREDIT", PAID: "RESOLVED_PAID", WRITE_OFF: "RESOLVED_WRITE_OFF", REJECTED: "REJECTED" };
route("POST", `${BASE}/disputes/{id}:resolve`, ({ params, headers, body }) => {
  const d = disputes.get(params.id);
  if (!d) return notFound("Dispute");
  const v = ifMatch(headers);
  if (v !== undefined && v !== d.version) return stale("dispute");
  const { resolution, note } = (body ?? {}) as { resolution: Resolution; note: string | null };
  if (!RESOLVED[resolution]) return invalid("Choose a resolution.");
  if (resolution !== "REJECTED" && d.status !== "UNDER_REVIEW") return conflict("AR-0022", "Credit, paid and write-off resolutions need the dispute under review first.");
  if (d.status !== "OPEN" && d.status !== "UNDER_REVIEW") return conflict("AR-0023", "This dispute is already closed.");
  // A write-off clears the disputed amount from the item.
  if (resolution === "WRITE_OFF") settleItem(d.openItemId, Math.min(Number(d.disputedAmount), Number(findItem(d.openItemId)?.openAmount ?? 0)));
  return disputes.update(d.id, { status: RESOLVED[resolution], resolutionNote: note, resolvedAt: at(0), version: d.version + 1 });
});

// ---- Promises to pay ----

route("GET", `${BASE}/promises`, ({ query }) => {
  const customer = query.get("customer");
  const item = query.get("openItem");
  const rows = promises
    .all()
    .filter((p) => (!customer || p.customerId === customer) && (!item || p.openItemId === item))
    .sort((a, b) => b.promiseDate.localeCompare(a.promiseDate));
  return { items: rows.slice(0, limitOf(query)) };
});

let promiseSeq = 100;
route("POST", `${BASE}/promises`, ({ body }) => {
  const input = body as PromiseInput;
  const item = findItem(input?.openItemId ?? "");
  if (!item) return notFound("Open item");
  if (!positive(input.amount)) return invalid("Enter an amount above zero.", { amount: "Enter an amount above zero." });
  if (Number(input.amount) - Number(item.openAmount) > 0.005) return invalid(`The promise is more than the ${item.openAmount} open.`, { amount: `At most the item's open amount, ${item.openAmount}.` });
  if (!input.promiseDate || input.promiseDate < day(0)) return invalid("The promise date cannot be in the past.", { promiseDate: "Enter today or a later date." });
  return reply.created(
    promises.insert({ id: uid(KR.promise, ++promiseSeq), legalEntityId: input.legalEntityId || LEGAL_ENTITY.id, customerId: item.customerId, openItemId: item.id, documentNumber: item.documentNumber, currency: item.currency, amount: dec(Number(input.amount)), promiseDate: input.promiseDate, createdAt: at(0) }),
  );
});

// ---- Dunning runs ----

route("GET", `${BASE}/dunning-runs`, ({ query }) => {
  const status = query.get("status");
  return {
    items: runs
      .all()
      .filter((r) => !status || r.status === status)
      .sort((a, b) => b.runDate.localeCompare(a.runDate) || b.id.localeCompare(a.id))
      .map(({ letters, exclusions: _x, ...r }) => ({ ...r, letterCount: letters.filter((l) => l.status !== "SKIPPED").length })),
  };
});

route("GET", `${BASE}/dunning-runs/{id}`, ({ params }) => runs.get(params.id) ?? notFound("Dunning run"));

let runSeq = 10;
route("POST", `${BASE}/dunning-runs`, ({ body }) => {
  const input = body as DunningRunInput;
  if (!input?.runDate) return invalid("Enter the run date.", { runDate: "Enter the run date." });
  const grace = Number(input.graceDays ?? 5);
  const { letters, exclusions } = proposeRun(input.runDate, grace);
  const run: DunningRun = { id: uid(KR.run, ++runSeq), legalEntityId: input.legalEntityId || LEGAL_ENTITY.id, runDate: input.runDate, graceDays: grace, status: "DRAFT", exclusions, letters, issuedAt: null, version: 1 };
  return reply.created(runs.insert(run));
});

function moveRun(id: string, headers: Headers, action: "review" | "issue" | "cancel") {
  const run = runs.get(id);
  if (!run) return notFound("Dunning run");
  const v = ifMatch(headers);
  if (v !== undefined && v !== run.version) return stale("run");
  if (action === "review") {
    if (run.status !== "DRAFT") return conflict("AR-0030", "Only a draft run can be reviewed.");
    return runs.update(id, { status: "REVIEWED", version: run.version + 1 });
  }
  if (action === "cancel") {
    if (run.status !== "DRAFT" && run.status !== "REVIEWED") return conflict("AR-0031", "An issued run cannot be cancelled.");
    return runs.update(id, { status: "CANCELLED", version: run.version + 1 });
  }
  if (run.status !== "REVIEWED") return conflict("AR-0032", "Review the run before issuing it.");
  // Exclusions are checked again: a letter whose items were paid, disputed or promised since is skipped.
  const exclusions: Exclusion[] = [...run.exclusions];
  const letters = run.letters.map((l) => {
    const late = l.items.find((i) => {
      const item = findItem(i.openItemId);
      return !item || Number(item.openAmount) <= 0 || activeDispute(i.openItemId) || heldByPromise(i.openItemId, day(0));
    });
    if (late) {
      const item = findItem(late.openItemId);
      exclusions.push({ openItemId: late.openItemId, customerId: l.customerId, reason: item && activeDispute(item.id) ? "DISPUTE" : "PROMISE" });
      return { ...l, status: "SKIPPED" as const };
    }
    return issueLetter(l, run.runDate);
  });
  return runs.update(id, { status: "ISSUED", letters, exclusions, issuedAt: at(0), version: run.version + 1 });
}
route("POST", `${BASE}/dunning-runs/{id}:review`, ({ params, headers }) => moveRun(params.id, headers, "review"));
route("POST", `${BASE}/dunning-runs/{id}:issue`, ({ params, headers }) => moveRun(params.id, headers, "issue"));
route("POST", `${BASE}/dunning-runs/{id}:cancel`, ({ params, headers }) => moveRun(params.id, headers, "cancel"));

// ---- Interest ----

route("GET", `${BASE}/interest-notes`, ({ query }) => {
  const customer = query.get("customer");
  return { items: interestNotes.all().filter((n) => !customer || n.customerId === customer).sort((a, b) => b.calculationDate.localeCompare(a.calculationDate) || a.documentNumber.localeCompare(b.documentNumber)) };
});

function calculate(input: InterestInput): { candidates: InterestCandidate[]; exclusions: Exclusion[] } {
  const calc = input.calculationDate || day(0);
  const rate = Number(input.annualRate);
  const grace = Number(input.graceDays ?? 0);
  const period = calc.slice(0, 7);
  const candidates: InterestCandidate[] = [];
  const exclusions: Exclusion[] = [];
  for (const item of allItems().filter((i) => i.side !== "CREDIT" && (i.sourceType === "INVOICE" || i.sourceType === "REBILL") && Number(i.openAmount) > 0)) {
    const late = daysBetween(item.dueDate, calc);
    if (late <= grace) continue;
    if (interestNotes.all().some((n) => n.openItemId === item.id && n.period === period)) continue;
    const reason: Exclusion["reason"] | undefined = item.dunningBlock ? "BLOCKED" : activeDispute(item.id, calc) ? "DISPUTE" : heldByPromise(item.id, calc) ? "PROMISE" : undefined;
    if (reason) {
      exclusions.push({ openItemId: item.id, customerId: item.customerId, reason });
      continue;
    }
    const from = item.dueDate > priorMonthEnd(calc) ? item.dueDate : priorMonthEnd(calc);
    const days = daysBetween(from, calc);
    if (days <= 0) continue;
    const amount = interestAmount(Number(item.openAmount), rate, days, input.basis);
    if (amount < 1) {
      exclusions.push({ openItemId: item.id, customerId: item.customerId, reason: "BELOW_MINIMUM" });
      continue;
    }
    candidates.push({ openItemId: item.id, customerId: item.customerId, documentNumber: item.documentNumber, currency: item.currency, principal: item.openAmount, days, amount: dec(amount) });
  }
  return { candidates, exclusions };
}

route("POST", `${BASE}/interest-notes:calculate`, ({ body }) => ({ ...calculate(body as InterestInput), posted: [] }));

route("POST", `${BASE}/interest-notes:post`, ({ body }) => {
  const input = body as InterestInput;
  const { candidates, exclusions } = calculate(input);
  const calc = input.calculationDate || day(0);
  const posted = candidates.map((c) => postInterestNote({ ...c, principal: Number(c.principal), amount: Number(c.amount) }, calc, input.annualRate, input.basis));
  return { candidates, exclusions, posted };
});

// ---- Collection cases ----

route("GET", `${BASE}/cases`, ({ query }) => {
  const status = query.get("status");
  const category = query.get("category");
  const customer = query.get("customer");
  const rows = cases
    .all()
    .map((c) => ({ ...c, slaBreached: c.status === "OPEN" && c.slaDue < at(0) }))
    .filter((c) => (!status || c.status === status) && (!category || c.category === category) && (!customer || c.customerId === customer))
    .sort((a, b) => (a.status === b.status ? a.slaDue.localeCompare(b.slaDue) : a.status === "OPEN" ? -1 : 1));
  return { items: rows.slice(0, limitOf(query)) };
});

route("GET", `${BASE}/cases/{id}`, ({ params }) => {
  const c = cases.get(params.id);
  return c ? { ...c, slaBreached: c.status === "OPEN" && c.slaDue < at(0) } : notFound("Case");
});

let caseSeq = 100;
export function openCase(input: CaseInput): CollectionCase {
  const item = input.openItemId ? findItem(input.openItemId) : undefined;
  const created = at(0);
  const slaHours = SLA_HOURS[input.category];
  return cases.insert({
    id: uid(KR.caseFile, ++caseSeq),
    legalEntityId: input.legalEntityId || LEGAL_ENTITY.id,
    customerId: input.customerId,
    category: input.category,
    owner: input.owner,
    priority: input.priority,
    status: "OPEN",
    reason: input.reason,
    currency: input.currency || item?.currency || "USD",
    openItemId: item?.id ?? null,
    documentNumber: item?.documentNumber ?? null,
    attachedAmount: input.attachedAmount ?? null,
    slaHours,
    slaDue: new Date(Date.parse(created) + slaHours * 3_600_000).toISOString(),
    slaBreached: false,
    createdAt: created,
    version: 1,
  });
}

route("POST", `${BASE}/cases`, ({ body }) => {
  const input = body as CaseInput;
  if (!CUSTOMERS.some((c) => c.id === input?.customerId)) return invalid("Choose a customer.", { customerId: "Choose a customer." });
  if (input.openItemId && !findItem(input.openItemId)) return notFound("Open item");
  return reply.created(openCase(input));
});

route("POST", `${BASE}/cases/{id}:assign`, ({ params, headers, body }) => {
  const c = cases.get(params.id);
  if (!c) return notFound("Case");
  const v = ifMatch(headers);
  if (v !== undefined && v !== c.version) return stale("case");
  const owner = String((body as { owner?: string })?.owner ?? "").trim();
  if (!owner) return invalid("Enter the new owner.", { owner: "Enter the new owner." });
  return cases.update(c.id, { owner, version: c.version + 1 });
});

route("POST", `${BASE}/cases/{id}:close`, ({ params, headers, body }) => {
  const c = cases.get(params.id);
  if (!c) return notFound("Case");
  const v = ifMatch(headers);
  if (v !== undefined && v !== c.version) return stale("case");
  if (c.status === "CLOSED") return conflict("AR-0040", "The case is already closed.");
  return cases.update(c.id, { status: "CLOSED", closeNote: (body as { note?: string | null })?.note ?? null, closedAt: at(0), slaBreached: false, version: c.version + 1 });
});

// ---- Balance confirmations ----

route("GET", `${BASE}/balance-confirmations`, () => ({
  items: confirmationRuns
    .all()
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map((r) => {
      const { confirmations: _list, ...run } = runWithCounts(r.id)!;
      return run;
    }),
}));

route("GET", `${BASE}/balance-confirmations/{id}`, ({ params }) => runWithCounts(params.id) ?? notFound("Confirmation run"));

route("GET", `${BASE}/balance-confirmations/{id}/differences`, ({ params }) => {
  if (!confirmationRuns.get(params.id)) return notFound("Confirmation run");
  return { items: confirmations.all().filter((c) => c.runId === params.id && c.status === "DISAGREED") };
});

let confirmRunSeq = 10;
route("POST", `${BASE}/balance-confirmations`, ({ body }) => {
  const input = body as ConfirmationRunInput;
  if (!input?.asOfDate) return invalid("Enter the key date.", { asOfDate: "Enter the key date." });
  const id = uid(KR.confirmRun, ++confirmRunSeq);
  confirmationRuns.insert({ id, legalEntityId: input.legalEntityId || LEGAL_ENTITY.id, asOfDate: input.asOfDate, threshold: dec(Number(input.threshold || 0)), sampleSize: input.sampleSize ?? null, includeZeroBalances: Boolean(input.includeZeroBalances), reminderDays: Number(input.reminderDays || 14), createdAt: at(0), counts: { selected: 0, agreed: 0, disagreed: 0, noReply: 0 } });
  selectConfirmations(id, input.asOfDate, Number(input.threshold || 0), input.sampleSize ?? null, Boolean(input.includeZeroBalances));
  return reply.created(runWithCounts(id));
});

route("POST", `${BASE}/balance-confirmations/{id}:reply`, ({ params, body }) => {
  const run = confirmationRuns.get(params.id);
  if (!run) return notFound("Confirmation run");
  const input = body as ReplyInput;
  const c = confirmations.get(input?.confirmationId ?? "");
  if (!c || c.runId !== run.id) return notFound("Confirmation");
  if (c.status !== "NO_REPLY") return conflict("AR-0050", "This customer has already replied; replies cannot be changed.");
  if (input.reply === "AGREED") return confirmations.update(c.id, { status: "AGREED", customerBalance: c.ourBalance, difference: "0.00", comment: input.comment, repliedAt: at(0) });
  const theirs = Number(input.customerBalance ?? "0");
  const difference = r2(Number(c.ourBalance) - theirs);
  const opened = openCase({ legalEntityId: run.legalEntityId, customerId: c.customerId, category: "DISPUTE", owner: "Rachel Kim", priority: Math.abs(difference) >= 10000 ? "HIGH" : "NORMAL", reason: `Balance confirmation as of ${run.asOfDate}: customer states ${dec(theirs)}`, currency: c.currency, attachedAmount: dec(Math.abs(difference)) });
  return confirmations.update(c.id, { status: "DISAGREED", customerBalance: dec(theirs), difference: dec(difference), comment: input.comment, repliedAt: at(0), caseId: opened.id });
});

// ---- Down payments and deposits ----

route("GET", `${BASE}/down-payments`, ({ query }) => {
  const kind = query.get("kind");
  const status = query.get("status");
  const customer = query.get("customer");
  const rows = downPayments
    .all()
    .filter((d) => (!kind || d.kind === kind) && (!status || d.status === status) && (!customer || d.customerId === customer))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items: rows.slice(0, limitOf(query)) };
});

route("GET", `${BASE}/down-payments/{id}`, ({ params }) => downPayments.get(params.id) ?? notFound("Down payment"));

let dpSeq = 100;
let clearingSeq = 0;
route("POST", `${BASE}/down-payments`, ({ body }) => {
  const input = body as DownPaymentInput;
  if (!CUSTOMERS.some((c) => c.id === input?.customerId)) return invalid("Choose a customer.", { customerId: "Choose a customer." });
  if (!positive(input.amount)) return invalid("Enter an amount above zero.", { amount: "Enter an amount above zero." });
  const amount = dec(Number(input.amount));
  const dp: DownPayment = { id: uid(KR.downPayment, ++dpSeq), legalEntityId: input.legalEntityId || LEGAL_ENTITY.id, customerId: input.customerId, salesOrderId: input.salesOrderId, kind: input.kind, currency: input.currency || "USD", amount, clearedAmount: "0.00", remaining: amount, status: "REQUESTED", receivedDate: null, createdAt: at(0), version: 1, clearings: [] };
  return reply.created(downPayments.insert(dp));
});

function moveDownPayment(id: string, headers: Headers, action: string, body: Record<string, unknown>) {
  const dp = downPayments.get(id);
  if (!dp) return notFound("Down payment");
  const v = ifMatch(headers);
  if (v !== undefined && v !== dp.version) return stale("down payment");
  const next = (patch: Partial<DownPayment>) => downPayments.update(id, { ...patch, version: dp.version + 1 });
  switch (action) {
    case "receive":
      if (dp.status !== "REQUESTED") return conflict("AR-0060", "Only a requested down payment can be received.");
      return next({ status: "RECEIVED", receivedDate: typeof body.receivedDate === "string" && body.receivedDate ? body.receivedDate : day(0) });
    case "cancel":
      if (dp.status !== "REQUESTED") return conflict("AR-0061", "Only a requested down payment can be cancelled.");
      return next({ status: "CANCELLED", remaining: "0.00" });
    case "release":
      if (dp.kind !== "SECURITY_DEPOSIT" || dp.status !== "RECEIVED") return conflict("AR-0062", "Only a received security deposit can be released.");
      return next({ status: "RELEASED", remaining: "0.00" });
    case "clear": {
      if (dp.status !== "RECEIVED") return conflict("AR-0063", "Receive the down payment before clearing it.");
      const item = findItem(String(body.openItemId ?? ""));
      if (!item || item.customerId !== dp.customerId || item.side === "CREDIT") return notFound("Open item of this customer");
      const amount = r2(Math.min(Number(dp.remaining), Number(item.openAmount), typeof body.amount === "string" && body.amount ? Number(body.amount) : Infinity));
      if (amount <= 0) return conflict("AR-0064", `${item.documentNumber} has nothing open to clear.`);
      settleItem(item.id, amount);
      const remaining = r2(Number(dp.remaining) - amount);
      return next({
        clearedAmount: dec(Number(dp.clearedAmount) + amount),
        remaining: dec(remaining),
        status: remaining === 0 ? "CLEARED" : "RECEIVED",
        clearings: [...(dp.clearings ?? []), { id: uid(KR.clearing, ++clearingSeq), openItemId: item.id, documentNumber: item.documentNumber, amount: dec(amount), clearedAt: at(0) }],
      });
    }
  }
  return notFound("Command");
}
for (const action of ["receive", "cancel", "clear", "release"]) {
  route("POST", `${BASE}/down-payments/{id}:${action}`, ({ params, headers, body }) => moveDownPayment(params.id, headers, action, (body ?? {}) as Record<string, unknown>));
}

// ---- Deductions ----

const isWorked = (d: Deduction) => d.status === "OPEN" || d.status === "MATCHED";
const withChildren = (d: Deduction): Deduction => ({ ...d, children: deductions.all().filter((c) => c.parentId === d.id) });

route("GET", `${BASE}/deductions`, ({ query }) => {
  const status = query.get("status");
  const type = query.get("type");
  const customer = query.get("customer");
  const rows = deductions
    .all()
    .filter((d) => (!status || d.status === status) && (!type || d.type === type) && (!customer || d.customerId === customer))
    .sort((a, b) => (isWorked(a) === isWorked(b) ? a.createdAt.localeCompare(b.createdAt) : isWorked(a) ? -1 : 1));
  return { items: rows.slice(0, limitOf(query)) };
});

route("GET", `${BASE}/deductions/{id}`, ({ params }) => {
  const d = deductions.get(params.id);
  return d ? withChildren(d) : notFound("Deduction");
});

let dedSeq = 100;
function newDeduction(d: Omit<Deduction, "id" | "version" | "createdAt">): Deduction {
  const moved = addExtraItem(d.customerId, "DEDUCTION", deductionNumber(), Number(d.amount), day(0), day(0));
  const id = uid(KR.deduction, ++dedSeq);
  deductionItem.set(id, moved.id);
  return deductions.insert({ ...d, id, createdAt: at(0), version: 1 });
}

route("POST", `${BASE}/deductions`, ({ body }) => {
  const input = body as DeductionInput;
  const item = findItem(input?.openItemId ?? "");
  if (!item) return notFound("Open item");
  if (!positive(input.amount)) return invalid("Enter an amount above zero.", { amount: "Enter an amount above zero." });
  if (Number(input.amount) - Number(item.openAmount) > 0.005) return invalid(`The deduction is more than the ${item.openAmount} open.`, { amount: `At most the item's open amount, ${item.openAmount}.` });
  // The short-paid amount moves off the invoice to a deduction item of its own.
  settleItem(item.id, Number(input.amount));
  return reply.created(newDeduction({ legalEntityId: input.legalEntityId || LEGAL_ENTITY.id, customerId: item.customerId, openItemId: item.id, documentNumber: item.documentNumber, paymentId: input.paymentId, type: input.type, reasonCode: input.reasonCode, currency: item.currency, amount: dec(Number(input.amount)), status: "OPEN" }));
});

const WRITE_OFF_LIMIT = 50;
let rebillSeq = 12;
function moveDeduction(id: string, headers: Headers, action: string, body: Record<string, unknown>) {
  const d = deductions.get(id);
  if (!d) return notFound("Deduction");
  const v = ifMatch(headers);
  if (v !== undefined && v !== d.version) return stale("deduction");
  const itemId = deductionItem.get(d.id);
  const clearItem = () => itemId && settleItem(itemId, Number(findItem(itemId)?.openAmount ?? 0));
  const note = typeof body.note === "string" && body.note.trim() ? body.note.trim() : null;
  const done = (patch: Partial<Deduction>) => withChildren(deductions.update(d.id, { ...patch, version: d.version + 1 })!);
  switch (action) {
    case "match":
      if (d.status !== "OPEN") return conflict("AR-0070", "Only an open deduction can be matched.");
      return done({ status: "MATCHED", claimType: body.claimType as Deduction["claimType"], claimRef: String(body.claimRef ?? ""), claimRemaining: String(body.claimRemaining ?? "") });
    case "approve":
      if (d.status !== "MATCHED") return conflict("AR-0071", "Match the deduction to a claim before approving it.");
      if (Number(d.claimRemaining ?? 0) + 0.005 < Number(d.amount)) return conflict("AR-0072", `The claim has only ${d.claimRemaining} left.`);
      clearItem();
      return done({ status: "APPROVED", decisionNote: note, resolvedAt: at(0), claimRemaining: dec(Number(d.claimRemaining ?? 0) - Number(d.amount)) });
    case "deny": {
      if (!isWorked(d)) return conflict("AR-0073", "This deduction is already resolved.");
      clearItem();
      const rebill = addExtraItem(d.customerId, "REBILL", `RB-${String(++rebillSeq).padStart(5, "0")}`, Number(d.amount), day(0), addDays(day(0), 30));
      return done({ status: "DENIED", denialReason: body.reason as Deduction["denialReason"], decisionNote: note, rebillItemId: rebill.id, resolvedAt: at(0) });
    }
    case "write-off":
      if (!isWorked(d)) return conflict("AR-0073", "This deduction is already resolved.");
      if (Number(d.amount) > WRITE_OFF_LIMIT) return reply.problem(403, "AR-0074", `Writing off more than ${dec(WRITE_OFF_LIMIT)} USD needs the receivables.write-off.approve duty.`);
      clearItem();
      return done({ status: "WRITTEN_OFF", decisionNote: note, resolvedAt: at(0) });
    case "split": {
      if (d.status !== "OPEN") return conflict("AR-0075", "Only an open deduction can be split.");
      const parts = (Array.isArray(body.parts) ? body.parts : []).map((p) => Number(p));
      if (parts.length < 2 || parts.some((p) => !(p > 0)) || Math.abs(parts.reduce((s, p) => s + p, 0) - Number(d.amount)) > 0.005) return invalid(`The parts must be two or more amounts that add up to ${d.amount}.`, { parts: `Enter amounts that add up to ${d.amount}.` });
      clearItem();
      for (const part of parts) newDeduction({ legalEntityId: d.legalEntityId, customerId: d.customerId, openItemId: d.openItemId, documentNumber: d.documentNumber, paymentId: d.paymentId, parentId: d.id, type: d.type, reasonCode: d.reasonCode, currency: d.currency, amount: dec(part), status: "OPEN" });
      return done({ status: "SPLIT", resolvedAt: at(0) });
    }
  }
  return notFound("Command");
}
for (const action of ["match", "approve", "deny", "split", "write-off"]) {
  route("POST", `${BASE}/deductions/{id}:${action}`, ({ params, headers, body }) => moveDeduction(params.id, headers, action, (body ?? {}) as Record<string, unknown>));
}
