// Intercompany service (/api/v1/interco) for the demo: posted IC journals, the charge log and stored reconciliation
// runs between the Cedar Ridge Pumps entities. Posting and classifying follow the service's rules on cents.
// Typed against the module's own client types so shape drift fails `tsc`.
import type { ChargePreviewRequest, JournalPreviewRequest, ReconStatus } from "../../modules/intercompany/api/client";
import type { ChargeRecord, EntityTarget, PostedEntry, PostedJournal, ReconRun, StoredReconItem } from "../../modules/intercompany/api/postings";
import { LEGAL_ENTITY, LEGAL_ENTITY_DE } from "../company";
import { reply, route } from "../backend";
import { at, day, newId, uid } from "../store";

const BASE = "/api/v1/interco";
const K_JOURNAL = 410;
const K_CHARGE = 411;
const K_RUN = 412;
const K_ITEM = 413;

const cents = (s: string): number => Math.round(Number(s) * 100);
const dec = (c: number): string => (c / 100).toFixed(2);

const entry = (entity: string, legalEntityId: string, ledgerId: string, currency: string, legs: [string, "debit" | "credit", string][], n: number): PostedEntry => ({
  entity,
  legalEntityId,
  ledgerId,
  currency,
  legs: legs.map(([account, side, amount]) => ({ account, side, amount })),
  ledgerEntryId: uid(414, n),
});

const journals: PostedJournal[] = [
  {
    id: uid(K_JOURNAL, 1),
    pairId: uid(415, 1),
    source: "journal",
    sourceRef: uid(415, 1),
    postingDate: day(-6),
    status: "posted",
    createdBy: "dev",
    createdAt: at(-6),
    entries: [
      entry("CRP-US", LEGAL_ENTITY.id, "CRP-US-GL", "USD", [["6100-EXPENSE", "debit", "10000.00"], ["1500-DUE-FROM-DE", "credit", "10000.00"]], 1),
      entry("CRP-DE", LEGAL_ENTITY_DE.id, "CRP-DE-GL", "EUR", [["2500-DUE-TO-US", "debit", "9200.00"], ["1000-CASH", "credit", "9200.00"]], 2),
    ],
  },
  {
    id: uid(K_JOURNAL, 2),
    pairId: uid(415, 2),
    source: "journal",
    sourceRef: uid(415, 2),
    postingDate: day(-3),
    status: "compensated",
    failureCode: "IC-0845",
    failureDetail: "the ledger of CRP-DE refused its side (LEDGER_PERIOD_CLOSED); the entries already posted were reversed",
    createdBy: "dev",
    createdAt: at(-3),
    entries: [
      { ...entry("CRP-US", LEGAL_ENTITY.id, "CRP-US-GL", "USD", [["6100-EXPENSE", "debit", "2400.00"], ["1500-DUE-FROM-DE", "credit", "2400.00"]], 3), reversalEntryId: uid(414, 4) },
      { ...entry("CRP-DE", LEGAL_ENTITY_DE.id, "CRP-DE-GL", "EUR", [["2500-DUE-TO-US", "debit", "2208.00"], ["1000-CASH", "credit", "2208.00"]], 5), ledgerEntryId: undefined },
    ],
  },
];

const charges: ChargeRecord[] = [
  { id: uid(K_CHARGE, 1), sourceId: "TS-2204", version: 1, lendingEntity: "CRP-DE", borrowingEntity: "CRP-US", category: "Hour", resourceRole: "Engineer", transactionDate: day(-9), quantity: "8", unitCost: "60.00", currency: "USD", status: "posted", transferPriceMethod: "CostPlus", cost: "480.00", price: "528.00", margin: "48.00", journalId: uid(K_JOURNAL, 1), createdBy: "dev", createdAt: at(-8) },
  { id: uid(K_CHARGE, 2), sourceId: "TS-2210", version: 1, lendingEntity: "CRP-DE", borrowingEntity: "CRP-US", category: "Hour", resourceRole: "Designer", transactionDate: day(-5), quantity: "6", unitCost: "55.00", currency: "USD", status: "suspense", failureCode: "IC-0821", createdBy: "dev", createdAt: at(-4) },
];

function postJournal(req: JournalPreviewRequest & { postingDate: string; entities: EntityTarget[] }): PostedJournal {
  const row = req.matrix.find((m) => m.entityA === req.entity && m.entityB === req.offsetEntity);
  const amount = cents(req.amount);
  const counter = Math.round(amount * Number(req.rate ?? "1"));
  const opp = req.side === "debit" ? "credit" : "debit";
  const target = (e: string) => req.entities.find((t) => t.entity === e);
  const a = target(req.entity);
  const b = target(req.offsetEntity);
  const pair = newId();
  return {
    id: newId(),
    pairId: pair,
    source: "journal",
    sourceRef: pair,
    postingDate: req.postingDate,
    status: "posted",
    createdBy: "dev",
    createdAt: at(0),
    entries: [
      entry(req.entity, a?.legalEntityId ?? "", a?.ledgerId ?? "", req.currency, [[req.account, req.side, dec(amount)], [row?.dueFrom ?? "1500-DUE-FROM", opp, dec(amount)]], journals.length * 2 + 1),
      entry(req.offsetEntity, b?.legalEntityId ?? "", b?.ledgerId ?? "", req.counterCurrency ?? req.currency, [[row?.dueTo ?? "2500-DUE-TO", req.side, dec(counter)], [req.offsetAccount, opp, dec(counter)]], journals.length * 2 + 2),
    ],
  };
}

route("GET", `${BASE}/journals`, ({ query }) => ({
  items: journals.filter((j) => (!query.get("status") || j.status === query.get("status")) && (!query.get("source") || j.source === query.get("source"))),
}));
route("POST", `${BASE}/journals`, ({ body }) => {
  const j = postJournal(body as JournalPreviewRequest & { postingDate: string; entities: EntityTarget[] });
  journals.unshift(j);
  return reply.created(j, { Location: `${BASE}/journals/${j.id}` });
});
route("GET", `${BASE}/journals/{id}`, ({ params }) => journals.find((j) => j.id === params.id) ?? reply.problem(404, "IC-0844", "No intercompany journal with that id"));

route("GET", `${BASE}/charges`, ({ query }) => ({
  items: charges.filter((c) => (!query.get("status") || c.status === query.get("status")) && (!query.get("sourceId") || c.sourceId === query.get("sourceId"))),
}));
route("POST", `${BASE}/charges`, ({ body }) => {
  const b = body as ChargePreviewRequest & { sourceId: string; version: number; postingDate: string; entities: EntityTarget[] };
  const existing = charges.find((c) => c.sourceId === b.sourceId && c.version === b.version);
  if (existing) return reply.created(existing, { "Idempotent-Replayed": "true" });
  const price = b.transferPrices.find((p) => p.category === b.category && (p.resourceRole ?? "") === (b.resourceRole ?? "") && p.effectiveFrom <= b.transactionDate);
  const base: ChargeRecord = {
    id: newId(),
    sourceId: b.sourceId,
    version: b.version,
    lendingEntity: b.lendingEntity,
    borrowingEntity: b.borrowingEntity,
    category: b.category,
    resourceRole: b.resourceRole,
    transactionDate: b.transactionDate,
    quantity: b.quantity,
    unitCost: b.unitCost,
    currency: b.currency,
    status: "suspense",
    failureCode: "IC-0821",
    createdBy: "dev",
    createdAt: at(0),
  };
  if (!price) {
    charges.unshift(base);
    return reply.accepted(base);
  }
  const cost = Math.round(Number(b.quantity) * cents(b.unitCost));
  const priced = price.method === "Table" ? Math.round(Number(b.quantity) * cents(price.unitPrice ?? "0")) : Math.round(cost * (1 + (price.method === "CostPlus" ? Number(price.markupPct ?? "0") : 0) / 100));
  const posted: ChargeRecord = { ...base, status: "posted", failureCode: undefined, transferPriceMethod: price.method, cost: dec(cost), price: dec(priced), margin: dec(priced - cost), journalId: journals[0]?.id };
  charges.unshift(posted);
  return reply.created(posted, { Location: `${BASE}/charges/${posted.id}` });
});

// ---- reconciliation runs ----

interface RunRow extends ReconRun {
  items: StoredReconItem[];
}
const runs: RunRow[] = [];

interface Pair {
  ref: string;
  entityA: string;
  entityB: string;
  amountA: string;
  amountB: string;
  nextPeriodA?: string;
  nextPeriodB?: string;
  fxApplies?: boolean;
}

function classify(p: Pair, tolerance: number): { status: ReconStatus; difference: number; tolerant: boolean } {
  const a = cents(p.amountA);
  const b = cents(p.amountB);
  const within = (x: number, y: number): { ok: boolean; fx: boolean } => {
    const abs = Math.abs(x - y);
    if (abs <= 1) return { ok: true, fx: false };
    if (!p.fxApplies) return { ok: false, fx: false };
    return { ok: abs <= (Math.max(Math.abs(x), Math.abs(y)) * tolerance) / 100, fx: true };
  };
  const direct = within(a, b);
  if (direct.ok) return { status: "Matched", difference: a - b, tolerant: direct.fx };
  if (p.nextPeriodA || p.nextPeriodB) {
    const later = within(a + cents(p.nextPeriodA ?? "0"), b + cents(p.nextPeriodB ?? "0"));
    if (later.ok) return { status: "Timing", difference: a - b, tolerant: later.fx };
  }
  return { status: "Mismatch", difference: a - b, tolerant: false };
}

function makeRun(per: string, currency: string, tolerance: string, pairs: Pair[], n: number): RunRow {
  const id = n > 0 ? uid(K_RUN, n) : newId();
  const items: StoredReconItem[] = pairs.map((p, i) => {
    const c = classify(p, Number(tolerance));
    return {
      id: n > 0 ? uid(K_ITEM, n * 100 + i) : newId(),
      runId: id,
      ref: p.ref,
      entityA: p.entityA,
      entityB: p.entityB,
      amountA: dec(cents(p.amountA)),
      amountB: dec(cents(p.amountB)),
      nextPeriodA: dec(cents(p.nextPeriodA ?? "0")),
      nextPeriodB: dec(cents(p.nextPeriodB ?? "0")),
      fxApplies: Boolean(p.fxApplies),
      difference: dec(c.difference),
      status: c.status,
      toleranceApplied: c.tolerant,
      resolution: c.status === "Matched" ? "none" : "open",
    };
  });
  return {
    id,
    period: per,
    currency,
    fxTolerancePct: tolerance,
    matched: items.filter((i) => i.status === "Matched").length,
    timing: items.filter((i) => i.status === "Timing").length,
    mismatch: items.filter((i) => i.status === "Mismatch").length,
    open: items.filter((i) => i.resolution === "open").length,
    blocking: items.some((i) => i.status === "Mismatch"),
    createdBy: "dev",
    createdAt: at(-2),
    items,
  };
}

const lastMonth = new Date(Date.now() - 30 * 86_400_000).toISOString().slice(0, 7);
runs.push(
  makeRun(
    lastMonth,
    "USD",
    "0.5",
    [
      { ref: "IC-1001", entityA: "CRP-US", entityB: "CRP-DE", amountA: "10000.00", amountB: "10000.00" },
      { ref: "IC-1002", entityA: "CRP-US", entityB: "CRP-DE", amountA: "4200.00", amountB: "4190.00", fxApplies: true },
      { ref: "IC-1003", entityA: "CRP-US", entityB: "CRP-ES", amountA: "2500.00", amountB: "0.00", nextPeriodB: "2500.00" },
      { ref: "IC-1004", entityA: "CRP-DE", entityB: "CRP-ES", amountA: "1800.00", amountB: "1950.00" },
    ],
    1,
  ),
);

route("GET", `${BASE}/reconciliations`, () => ({ items: runs.map(({ items: _items, ...run }) => run) }));
route("POST", `${BASE}/reconciliations/run`, ({ body }) => {
  const b = body as { period: string; currency: string; fxTolerancePct?: string; items: (Omit<Pair, "ref"> & { id?: string; icPairId?: string })[] };
  const run = makeRun(b.period, b.currency, b.fxTolerancePct ?? "0.5", b.items.map((i) => ({ ...i, ref: i.icPairId ?? i.id ?? "" })), 0);
  runs.unshift(run);
  const { items: _items, ...head } = run;
  return reply.created(head, { Location: `${BASE}/reconciliations/${run.id}` });
});
route("POST", `${BASE}/reconciliations/items/{id}/resolve`, ({ params, body }) => {
  for (const run of runs) {
    const item = run.items.find((i) => i.id === params.id);
    if (!item) continue;
    if (item.resolution !== "open") return reply.problem(409, "IC-0852", `The item is ${item.resolution} and has nothing left to resolve`);
    item.resolution = "resolved";
    item.note = (body as { note: string }).note;
    item.resolvedBy = "dev";
    item.resolvedAt = at(0);
    run.open = run.items.filter((i) => i.resolution === "open").length;
    return item;
  }
  return reply.problem(404, "IC-0851", "No reconciliation item with that id");
});
route("GET", `${BASE}/reconciliations/{id}`, ({ params, query }) => {
  const run = runs.find((r) => r.id === params.id);
  if (!run) return reply.problem(404, "IC-0850", "No reconciliation run with that id");
  const status = query.get("status");
  return { ...run, items: run.items.filter((i) => !status || i.status === status) };
});
