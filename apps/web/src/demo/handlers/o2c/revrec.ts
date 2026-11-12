// Revenue recognition (services/revrec, /api/v1/revrec) for the demo: revenue contracts on Cedar Ridge Pumps' sales
// orders, where a pump ships at a point in time, commissioning or installation is a milestone and a service plan or
// warranty is recognized monthly over its term. Contracts, milestone satisfaction, recognition runs, the allocation
// preview and the deferred revenue roll-forward. Typed against the billing module's client so drift fails tsc.
import type {
  AllocatedObligation,
  AllocationPreview,
  AllocationPreviewInput,
  ContractObligation,
  ContractStatus,
  Modification,
  ModificationLine,
  ModifyContractInput,
  ModifyContractResult,
  RecognitionMethod,
  RecognitionRun,
  RevenueContract,
  RevenueContractInput,
  RevenueContractSummary,
  RollForward,
  ScheduleLine,
  SspSource,
} from "../../../modules/billing/api/client";
import { CUSTOMERS, ITEMS, LEGAL_ENTITY, SERVICES } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, newId, uid } from "../../store";
import { K as SALES } from "./model";

const BASE = "/api/v1/revrec";
const K_CONTRACT = 780;
const K_OBLIGATION = 781;
const K_SCHEDULE = 782;
const K_RUN = 783;

const productId = (number: string): string => (ITEMS.find((i) => i.number === number) ?? SERVICES.find((s) => s.number === number))?.id ?? number;
const cents = (n: number): number => Math.round(n * 100) / 100;

/** The last day of the month of a YYYY-MM-DD date. */
function monthEnd(date: string): string {
  const [y, m] = date.split("-").map(Number);
  return new Date(Date.UTC(y, m, 0)).toISOString().slice(0, 10);
}

/** The month ends from the month of start through the month of end. */
function monthEnds(start: string, end: string): string[] {
  const out: string[] = [];
  let [y, m] = start.split("-").map(Number);
  const last = end.slice(0, 7);
  for (let guard = 0; guard < 120; guard++) {
    const key = `${y}-${String(m).padStart(2, "0")}`;
    out.push(monthEnd(`${key}-01`));
    if (key >= last) break;
    m += 1;
    if (m > 12) {
      m = 1;
      y += 1;
    }
  }
  return out;
}

function addMonths(date: string, months: number): string {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCMonth(d.getUTCMonth() + months);
  d.setUTCDate(d.getUTCDate() - 1);
  return d.toISOString().slice(0, 10);
}

interface ObligationSpec {
  product: string;
  ssp: number;
  method: RecognitionMethod;
  sspSource?: SspSource;
  termStart?: string;
  months?: number;
  satisfiedOn?: string;
}

/** Relative-SSP allocation of the price, the rounding difference on the largest obligation (residual rule LARGEST). */
function allocate(price: number, ssps: number[]): number[] {
  const total = ssps.reduce((a, b) => a + b, 0);
  const out = ssps.map((s) => (total === 0 ? 0 : cents((price * s) / total)));
  const diff = cents(price - out.reduce((a, b) => a + b, 0));
  if (diff !== 0 && out.length > 0) {
    const largest = ssps.indexOf(Math.max(...ssps));
    out[largest] = cents(out[largest] + diff);
  }
  return out;
}

/** Schedule lines of one obligation; everything dated on or before `asOf` is recognized. */
function schedule(contractNo: number, lineNo: number, method: RecognitionMethod, allocated: number, asOf: string, o: { termStart?: string; termEnd?: string; satisfiedOn?: string; date?: string }): ScheduleLine[] {
  const id = (seq: number) => uid(K_SCHEDULE, contractNo * 1000 + lineNo * 100 + seq);
  if (method === "RATABLE" && o.termStart && o.termEnd) {
    const ends = monthEnds(o.termStart, o.termEnd);
    const each = cents(allocated / ends.length);
    return ends.map((end, i) => {
      const amount = i === ends.length - 1 ? cents(allocated - each * (ends.length - 1)) : each;
      const done = end <= asOf;
      return { id: id(i + 1), seq: i + 1, date: end, amount: dec(amount), status: done ? "RECOGNIZED" : "SCHEDULED", ...(done ? { recognizedOn: end } : {}) };
    });
  }
  const date = o.satisfiedOn ?? (method === "MILESTONE" ? undefined : o.date);
  const done = date !== undefined && date <= asOf;
  return [{ id: id(1), seq: 1, ...(date ? { date } : {}), amount: dec(allocated), status: done ? "RECOGNIZED" : "SCHEDULED", ...(done ? { recognizedOn: date } : {}) }];
}

function statusOf(obligations: ContractObligation[]): ContractStatus {
  return obligations.every((o) => o.schedule.every((l) => l.status === "RECOGNIZED")) ? "COMPLETE" : "OPEN";
}

function totals(obligations: ContractObligation[]): { recognized: number; price: number } {
  let recognized = 0;
  let price = 0;
  for (const o of obligations) {
    price += Number(o.allocated);
    for (const l of o.schedule) if (l.status === "RECOGNIZED") recognized += Number(l.amount);
  }
  return { recognized: cents(recognized), price: cents(price) };
}

function refresh(c: RevenueContract): RevenueContract {
  const obligations = c.obligations.map((o) => ({ ...o, recognized: dec(o.schedule.filter((l) => l.status === "RECOGNIZED").reduce((s, l) => s + Number(l.amount), 0)) }));
  const t = totals(obligations);
  return { ...c, obligations, recognized: dec(t.recognized), remaining: dec(cents(Number(c.transactionPrice) - t.recognized)), status: c.status === "CANCELLED" ? c.status : statusOf(obligations) };
}

function contract(n: number, order: number, customer: number, price: number, contractAgo: number, specs: ObligationSpec[]): RevenueContract {
  const contractDate = day(-contractAgo);
  const asOf = day(0);
  const alloc = allocate(price, specs.map((s) => s.ssp));
  const obligations: ContractObligation[] = specs.map((s, i) => {
    const termStart = s.months ? (s.termStart ?? contractDate) : undefined;
    const termEnd = termStart && s.months ? addMonths(termStart, s.months) : undefined;
    return {
      id: uid(K_OBLIGATION, n * 10 + i + 1),
      lineNo: i + 1,
      productId: productId(s.product),
      ssp: dec(s.ssp),
      sspSource: s.sspSource ?? "LIST",
      allocated: dec(alloc[i]),
      recognized: "0.00",
      method: s.method,
      ...(termStart ? { termStart, termEnd } : {}),
      // A delivery still ahead is a scheduled date, not a satisfaction.
      ...(s.satisfiedOn && s.satisfiedOn <= asOf ? { satisfiedOn: s.satisfiedOn } : {}),
      schedule: schedule(n, i + 1, s.method, alloc[i], asOf, {
        termStart,
        termEnd,
        satisfiedOn: s.satisfiedOn && s.satisfiedOn <= asOf ? s.satisfiedOn : undefined,
        date: s.method === "POINT_IN_TIME" ? (s.satisfiedOn ?? contractDate) : undefined,
      }),
    };
  });
  return refresh({
    id: uid(K_CONTRACT, n),
    legalEntityId: LEGAL_ENTITY.id,
    orderId: uid(SALES.order, order),
    customerId: CUSTOMERS[customer].id,
    currency: "USD",
    transactionPrice: dec(price),
    recognized: "0.00",
    remaining: dec(price),
    residualRule: "LARGEST",
    ratableBasis: "MONTHLY",
    contractDate,
    status: "OPEN",
    version: 1,
    obligations,
    warnings: [],
  });
}

const contracts: RevenueContract[] = [
  // Booster skids for the desalination plant: delivered in about two weeks, then commissioned, with a two-year service plan.
  contract(1, 4, 3, 61_400, 3, [
    { product: "BP-300", ssp: 37_800, method: "POINT_IN_TIME", satisfiedOn: day(15) },
    { product: "CP-400", ssp: 23_560, method: "POINT_IN_TIME", satisfiedOn: day(15) },
    { product: "SVC-COMM", ssp: 6_500, method: "MILESTONE" },
    { product: "SVC-PLAN-24", ssp: 9_600, method: "RATABLE", termStart: day(15), months: 24 },
  ]),
  // Twenty pumps shipped and invoiced last month, with a 12-month extended warranty running since delivery.
  contract(2, 7, 0, 49_000, 34, [
    { product: "CP-200", ssp: 49_000, method: "POINT_IN_TIME", satisfiedOn: day(-25) },
    { product: "SVC-WTY-12", ssp: 2_400, method: "RATABLE", sspSource: "EXPLICIT", termStart: day(-25), months: 12 },
  ]),
  // A mine dewatering order: pumps, bearings and installation, all delivered and installed.
  contract(3, 13, 2, 41_920, 82, [
    { product: "CP-200", ssp: 39_200, method: "POINT_IN_TIME", satisfiedOn: day(-73) },
    { product: "BRG-6206", ssp: 1_920, method: "POINT_IN_TIME", satisfiedOn: day(-73) },
    { product: "SVC-INST", ssp: 1_800, method: "MILESTONE", satisfiedOn: day(-68) },
  ]),
  // Ten pumps with a three-year service plan; the plan is a few months in.
  contract(4, 10, 3, 66_500, 60, [
    { product: "CP-400", ssp: 58_900, method: "POINT_IN_TIME", satisfiedOn: day(-48) },
    { product: "SVC-PLAN-36", ssp: 13_800, method: "RATABLE", termStart: day(-48), months: 36 },
  ]),
];

const summary = (c: RevenueContract): RevenueContractSummary => {
  const { obligations: _o, warnings: _w, ...rest } = c;
  return rest;
};

route("GET", `${BASE}/contracts`, ({ query }) => {
  const status = query.get("status");
  const customer = query.get("customerId");
  const method = query.get("method");
  const items = contracts
    .filter((c) => (!status || c.status === status) && (!customer || c.customerId === customer) && (!method || c.obligations.some((o) => o.method === method)))
    .sort((a, b) => b.contractDate.localeCompare(a.contractDate))
    .map(summary);
  return { items };
});

route("GET", `${BASE}/contracts/{id}`, ({ params }) => contracts.find((c) => c.id === params.id) ?? reply.problem(404, "REV-0100", "revenue contract not found"));

route("POST", `${BASE}/contracts`, ({ body }) => {
  const input = body as RevenueContractInput;
  const price = Number(input.transactionPrice);
  if (!Number.isFinite(price) || price <= 0 || !Array.isArray(input.obligations) || input.obligations.length === 0) {
    return reply.problem(400, "COM-0001", "request failed validation", { transactionPrice: "must be a positive amount" });
  }
  const n = contracts.length + 1;
  const ssps = input.obligations.map((o) => Number(o.ssp ?? 0));
  const alloc = allocate(price, ssps);
  const asOf = day(0);
  const obligations: ContractObligation[] = input.obligations.map((o, i) => ({
    id: uid(K_OBLIGATION, n * 10 + i + 1),
    lineNo: i + 1,
    productId: o.productId,
    ssp: dec(ssps[i]),
    sspSource: o.sspSource,
    allocated: dec(alloc[i]),
    recognized: "0.00",
    method: o.method,
    ...(o.termStart ? { termStart: o.termStart, termEnd: o.termEnd } : {}),
    ...(o.satisfiedOn ? { satisfiedOn: o.satisfiedOn } : {}),
    schedule: schedule(n, i + 1, o.method, alloc[i], asOf, { termStart: o.termStart, termEnd: o.termEnd, satisfiedOn: o.satisfiedOn, date: o.method === "POINT_IN_TIME" ? (o.satisfiedOn ?? input.contractDate) : undefined }),
  }));
  const created = refresh({ ...input, id: uid(K_CONTRACT, n), transactionPrice: dec(price), recognized: "0.00", remaining: dec(price), status: "OPEN", version: 1, obligations, warnings: [] });
  contracts.push(created);
  return reply.created(created);
});

route("POST", `${BASE}/milestones/{obligationId}:satisfy`, ({ params, body, headers }) => {
  const at0 = contracts.findIndex((c) => c.obligations.some((o) => o.id === params.obligationId));
  if (at0 < 0) return reply.problem(404, "REV-0100", "obligation not found");
  const c = contracts[at0];
  const expected = (headers.get("If-Match") ?? "").replace(/"/g, "");
  if (expected && expected !== String(c.version)) return reply.problem(412, "REV-0103", "the contract changed since you opened it");
  const o = c.obligations.find((x) => x.id === params.obligationId)!;
  const line = o.schedule.find((l) => l.status === "SCHEDULED");
  if (o.method !== "MILESTONE" || !line) return reply.problem(409, "REV-0004", "the milestone is already satisfied on another date");
  const on = String((body as { satisfiedOn?: unknown } | undefined)?.satisfiedOn ?? day(0));
  const obligations = c.obligations.map((x) =>
    x.id === o.id ? { ...x, satisfiedOn: on, schedule: x.schedule.map((l) => (l.id === line.id ? { ...l, date: on, status: "RECOGNIZED" as const, recognizedOn: on } : l)) } : x,
  );
  contracts[at0] = refresh({ ...c, obligations, version: c.version + 1 });
  return { contractId: c.id, contractVersion: c.version + 1, obligationId: o.id, scheduleId: line.id, amount: line.amount, recognizedOn: on };
});

// Recognition runs: the last three month ends, each recognizing that month's ratable lines.
function ratableLinesIn(start: string, end: string): number {
  return contracts.flatMap((c) => c.obligations.filter((o) => o.method === "RATABLE").flatMap((o) => o.schedule)).filter((l) => l.date !== undefined && l.date >= start && l.date <= end).length;
}

const runs: RecognitionRun[] = [1, 2, 3].map((back) => {
  const end = monthEnd(day(-30 * back));
  return { id: uid(K_RUN, back), legalEntityId: LEGAL_ENTITY.id, periodEnd: end, lineCount: ratableLinesIn(`${end.slice(0, 7)}-01`, end), createdAt: at(-30 * back + 1) };
});

route("GET", `${BASE}/recognition-runs`, ({ query }) => {
  const entity = query.get("legalEntityId");
  return { items: runs.filter((r) => !entity || r.legalEntityId === entity).sort((a, b) => b.periodEnd.localeCompare(a.periodEnd)) };
});

route("POST", `${BASE}/recognition-runs`, ({ body }) => {
  const input = (body ?? {}) as { legalEntityId?: string; periodEnd?: string };
  const end = input.periodEnd ?? monthEnd(day(0));
  const existing = runs.find((r) => r.periodEnd === end && r.legalEntityId === input.legalEntityId);
  if (existing) return existing;
  let lineCount = 0;
  for (let i = 0; i < contracts.length; i++) {
    const c = contracts[i];
    const obligations = c.obligations.map((o) =>
      o.method !== "RATABLE"
        ? o
        : {
            ...o,
            schedule: o.schedule.map((l) => {
              if (l.status !== "SCHEDULED" || !l.date || l.date > end) return l;
              lineCount += 1;
              return { ...l, status: "RECOGNIZED" as const, recognizedOn: l.date };
            }),
          },
    );
    contracts[i] = refresh({ ...c, obligations });
  }
  const run: RecognitionRun = { id: newId(), legalEntityId: input.legalEntityId ?? LEGAL_ENTITY.id, periodEnd: end, lineCount, createdAt: at(0) };
  runs.push(run);
  return reply.created(run);
});

/** Opening, additions, recognized and closing deferred revenue of some contracts over one month. */
function rollForward(rows: RevenueContract[], period: string): Omit<RollForward, "legalEntityId" | "currency" | "period"> {
  const start = `${period}-01`;
  const end = monthEnd(start);
  let opening = 0;
  let additions = 0;
  let recognized = 0;
  for (const c of rows) {
    const lines = c.obligations.flatMap((o) => o.schedule).filter((l) => l.status === "RECOGNIZED" && l.recognizedOn);
    const before = lines.filter((l) => l.recognizedOn! < start).reduce((s, l) => s + Number(l.amount), 0);
    const during = lines.filter((l) => l.recognizedOn! >= start && l.recognizedOn! <= end).reduce((s, l) => s + Number(l.amount), 0);
    if (c.contractDate < start) opening += Number(c.transactionPrice) - before;
    else if (c.contractDate <= end) additions += Number(c.transactionPrice) - before;
    recognized += c.contractDate <= end ? during : 0;
  }
  opening = cents(opening);
  additions = cents(additions);
  recognized = cents(recognized);
  return { start, end, opening: dec(opening), additions: dec(additions), recognized: dec(recognized), adjustments: "0.00", closing: dec(cents(opening + additions - recognized)) };
}

route("GET", `${BASE}/roll-forward`, ({ query }) => {
  const period = query.get("period") ?? day(0).slice(0, 7);
  const contractId = query.get("contractId");
  if (contractId) {
    const c = contracts.find((x) => x.id === contractId);
    if (!c) return reply.problem(404, "REV-0100", "revenue contract not found");
    return { legalEntityId: c.legalEntityId, contractId, currency: c.currency, period, ...rollForward([c], period) };
  }
  const entity = query.get("legalEntityId") ?? LEGAL_ENTITY.id;
  const currency = query.get("currency") ?? "USD";
  return { legalEntityId: entity, currency, period, ...rollForward(contracts.filter((c) => c.legalEntityId === entity && c.currency === currency), period) };
});

route("POST", `${BASE}/allocations:preview`, ({ body }) => {
  const input = body as AllocationPreviewInput;
  const price = Number(input.transactionPrice);
  const listed = input.obligations.map((o) => (o.sspSource === "RESIDUAL" ? 0 : Number(o.ssp ?? 0)));
  const residual = input.obligations.findIndex((o) => o.sspSource === "RESIDUAL");
  if (residual >= 0) listed[residual] = Math.max(0, cents(price - listed.reduce((a, b) => a + b, 0)));
  const alloc = allocate(price, listed);
  const obligations: AllocatedObligation[] = input.obligations.map((o, i) => {
    const out: AllocatedObligation = { id: o.id, ssp: dec(listed[i]), sspSource: o.sspSource, method: o.method, allocated: dec(alloc[i]) };
    if (o.method === "RATABLE" && o.termStart && o.termEnd) {
      const ends = monthEnds(o.termStart, o.termEnd);
      const each = cents(alloc[i] / ends.length);
      out.schedule = ends.map((end, k) => ({ start: k === 0 ? o.termStart! : `${end.slice(0, 7)}-01`, end: k === ends.length - 1 ? o.termEnd! : end, amount: dec(k === ends.length - 1 ? cents(alloc[i] - each * (ends.length - 1)) : each) }));
    }
    return out;
  });
  const preview: AllocationPreview = { currency: input.currency, transactionPrice: dec(price), residualRule: input.residualRule, ratableBasis: input.ratableBasis, obligations };
  return preview;
});

// Contract modifications (#29): the demo re-allocates prospectively for both types (the service also does the cumulative
// catch-up); a price below recognized revenue takes the difference back out of revenue in proportion to what each obligation
// recognized. A removed obligation's open lines are cancelled.
route("POST", `${BASE}/contracts/{id}:modify`, ({ params, body, headers }) => {
  const at0 = contracts.findIndex((c) => c.id === params.id);
  if (at0 < 0) return reply.problem(404, "REV-0100", "revenue contract not found");
  const c = contracts[at0];
  const expected = (headers.get("If-Match") ?? "").replace(/"/g, "");
  if (!expected) return reply.problem(428, "COM-0428", "If-Match with the contract version is required to modify a contract");
  if (expected !== String(c.version)) return reply.problem(412, "REV-0103", "the contract changed since it was read; reload and review it");
  const input = body as ModifyContractInput;
  const change = Number(input.transactionPriceChange);
  if (!Number.isFinite(change)) return reply.problem(400, "COM-0001", "request failed validation", { transactionPriceChange: "must be a signed decimal" });
  if (input.effectiveDate < c.contractDate) return reply.problem(422, "REV-0401", `the effective date is before the contract date ${c.contractDate}`);
  const revised = new Map((input.obligations ?? []).map((o) => [o.obligationId, o]));
  const price = cents(Number(c.transactionPrice) + change);
  const recognizedOf = (o: ContractObligation) => cents(o.schedule.filter((l) => l.status === "RECOGNIZED").reduce((s, l) => s + Number(l.amount), 0));
  const open = (o: ContractObligation) => o.schedule.filter((l) => l.status === "SCHEDULED");
  const deferred = c.obligations.filter((o) => o.method === "RATABLE" || o.method === "MILESTONE");
  let left = price;
  for (const o of c.obligations) left -= o.method === "RATABLE" || o.method === "MILESTONE" ? recognizedOf(o) : Number(o.allocated);
  left = cents(left);
  const recognizedTotal = deferred.reduce((s, o) => s + recognizedOf(o), 0);
  if (left < 0 && -left > recognizedTotal) return reply.problem(422, "REV-0402", "the new price is below what the point-in-time obligations were billed at; credit the invoice instead");
  const weights = deferred.map((o) => {
    const total = o.schedule.reduce((s, l) => s + Number(l.amount), 0);
    const share = total > 0 ? open(o).reduce((s, l) => s + Number(l.amount), 0) / total : 0;
    const r = revised.get(o.id);
    const scope = r?.remainingScope !== undefined ? Number(r.remainingScope) : 1;
    return Number(r?.ssp ?? o.ssp) * share * scope;
  });
  const shares = left > 0 ? allocate(left, weights) : weights.map(() => 0);
  const lines: ModificationLine[] = [];
  let reversalTotal = 0;
  const obligations = c.obligations.map((o) => {
    const i = deferred.indexOf(o);
    if (i < 0) return o;
    const recognized = recognizedOf(o);
    const reversal = left < 0 && recognizedTotal > 0 ? cents((-left * recognized) / recognizedTotal) : 0;
    reversalTotal = cents(reversalTotal + reversal);
    const r = revised.get(o.id);
    const removed = r?.remainingScope !== undefined && Number(r.remainingScope) === 0;
    const openLines = open(o);
    const weight = openLines.reduce((s, l) => s + Number(l.amount), 0);
    let given = 0;
    const schedule = o.schedule.map((l) => {
      if (l.status !== "SCHEDULED") return l;
      const last = l.id === openLines[openLines.length - 1]?.id;
      const amount = last ? cents(shares[i] - given) : weight > 0 ? cents((shares[i] * Number(l.amount)) / weight) : 0;
      given = cents(given + amount);
      return { ...l, amount: dec(amount), status: amount === 0 ? ("REVERSED" as const) : l.status };
    });
    const after = cents(recognized - reversal + shares[i]);
    lines.push({ obligationId: o.id, allocatedBefore: o.allocated, allocatedAfter: dec(after), sspBefore: o.ssp, sspAfter: dec(Number(r?.ssp ?? o.ssp)), catchUp: "0.00", reversal: dec(reversal), ...(removed ? { removed: true } : {}) });
    return { ...o, allocated: dec(after), ssp: dec(Number(r?.ssp ?? o.ssp)), schedule };
  });
  const updated = refresh({ ...c, transactionPrice: dec(price), version: c.version + 1, obligations });
  contracts[at0] = updated;
  const modification: Modification = {
    id: newId(),
    type: input.type,
    effectiveDate: input.effectiveDate,
    priceBefore: c.transactionPrice,
    priceAfter: dec(price),
    catchUp: "0.00",
    reversal: dec(reversalTotal),
    billingStatus: change !== 0 ? "PENDING" : "NONE",
    obligations: lines,
  };
  const result: ModifyContractResult = { ...updated, modification };
  return result;
});
