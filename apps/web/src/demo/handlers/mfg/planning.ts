// Planning service (/api/v1/planning) for the demo: the MAIN plan's planned orders and action messages.
// Items, vendors and warehouses carry the real ids of company.ts. Demand references are the sales orders
// of the order-to-cash demo (SO-0241xx), and the supply references are purchase and production orders.
import type { FirmBatch, Message, PegNode, PlanVersion, PlannedOrder, PlannedOrderPatch } from "../../../modules/planning/api/client";
import { reply, route } from "../../backend";
import { ITEMS, USER, PLANT, VENDORS, WH_DAL, itemByNumber } from "../../company";
import { at, day, dec, table, uid } from "../../store";
import { newOrder } from "./orders";
import { orders as productionOrders } from "./production";

const BASE = "/api/v1/planning";
const KIND_ORDER = 210;
const KIND_MSG = 211;
const KIND_VERSION = 212;
const VERSION_ID = uid(KIND_VERSION, 3);

const vendor = (i: number) => VENDORS[i].id;
const itemId = (number: string) => itemByNumber(number).id;

let seq = 0;
function planned(number: number, item: string, orderType: PlannedOrder["orderType"], qty: number, orderIn: number, deliverIn: number, rest: Partial<PlannedOrder> = {}): PlannedOrder {
  seq += 1;
  return {
    id: uid(KIND_ORDER, seq),
    versionId: VERSION_ID,
    number: `PL-${number}`,
    itemId: itemId(item),
    orderType,
    qty: dec(qty, 0),
    orderDate: day(orderIn),
    deliveryDate: day(deliverIn),
    delayDays: Math.max(0, -orderIn),
    lotRule: "lot-for-lot",
    status: "unprocessed",
    version: 1,
    ...rest,
  };
}

const plannedOrders = table<PlannedOrder>([
  planned(30411, "CP-400", "production", 10, 2, 8, { lotRule: "lot-for-lot" }),
  planned(30412, "CP-200", "production", 40, 1, 7, { lotRule: "min 25, multiple of 5" }),
  planned(30413, "SA-IMP-06", "production", 50, 0, 4, { status: "approved", version: 2 }),
  planned(30414, "DP-100", "production", 30, 3, 10, { lotRule: "fixed 30" }),
  planned(30415, "CST-IMP-06", "purchase", 120, -2, 28, { vendorId: vendor(0), lotRule: "min 100" }),
  planned(30416, "MTR-15HP-TEFC", "purchase", 12, -3, 25, { vendorId: vendor(1), lotRule: "multiple of 6" }),
  planned(30417, "CST-VOL-04", "purchase", 40, 4, 39, { vendorId: vendor(0) }),
  planned(30418, "SEAL-MK-125", "purchase", 200, 6, 16, { vendorId: vendor(2), lotRule: "multiple of 50" }),
  planned(30419, "CP-200", "transfer", 20, 2, 4, { warehouseId: WH_DAL.id }),
  planned(30420, "MTR-5HP-TEFC", "purchase", 30, 9, 30, { vendorId: vendor(1), status: "approved", version: 2 }),
  planned(30421, "CST-IMP-06", "purchase", 80, -12, 18, { vendorId: vendor(0), status: "firmed", version: 3, delayDays: 0, firmedTargetType: "purchase-order", firmedTargetId: "PO-52340" }),
  planned(30422, "CP-200", "production", 30, -3, 9, { status: "firmed", version: 3, delayDays: 0, firmedTargetType: "production-order", firmedTargetId: "MO-24621" }),
]);

const version: PlanVersion = {
  id: VERSION_ID,
  planCode: "MAIN",
  number: 3,
  status: "current",
  kind: "static",
  asOf: at(-0.3),
  itemsPlanned: 17,
  plannedOrders: 12,
  approvedFirmed: 1,
  messages: 7,
  durationMs: 1840,
  planHash: "9f3ac71e04b2d85c6a10f7e92b3d4c58",
};
const versions: PlanVersion[] = [
  version,
  { ...version, id: uid(KIND_VERSION, 2), number: 2, status: "complete", asOf: at(-1.3), plannedOrders: 11, messages: 9, durationMs: 1795, planHash: "1c8e5b02f97d34a6e0b81d7c5f2a9e43", approvedFirmed: 0 },
  { ...version, id: uid(KIND_VERSION, 1), number: 1, status: "complete", asOf: at(-2.3), plannedOrders: 14, messages: 11, durationMs: 2210, planHash: "d47a90c3e5128bf6a3d09e71c4b85f20", approvedFirmed: 2 },
];

let mseq = 0;
function msg(action: Message["action"], item: string, qty: number, rest: Partial<Message>): Message {
  mseq += 1;
  return { id: uid(KIND_MSG, mseq), status: "open", version: 1, action, itemId: itemId(item), delayDays: 0, qty: dec(qty, 0), ...rest };
}

const messages = table<Message>([
  msg("advance", "CST-IMP-06", 80, {
    supplyRef: "PO-52317",
    demandRef: "MO-24618",
    suggestedDate: day(3),
    delayDays: 6,
    code: "PLAN-M01",
    text: "Receipt of PO-52317 is needed 6 days earlier: impeller assembly order MO-24618 is already running short of castings.",
  }),
  msg("delay", "MTR-15HP-TEFC", 6, {
    supplyRef: "PO-52331",
    demandRef: "SO-024184",
    suggestedDate: day(9),
    delayDays: 5,
    code: "PLAN-M02",
    text: "PO-52331 arrives 5 days after the CP-400 for Coastal Desalination Partners must start.",
  }),
  msg("postpone", "CST-VOL-04", 40, {
    supplyRef: "PO-52305",
    demandRef: "MO-24624",
    suggestedDate: day(16),
    delayDays: 7,
    code: "PLAN-M03",
    text: "Volute castings on PO-52305 are not needed until MO-24624 moves; receipt can slip a week.",
  }),
  msg("cancel", "SEAL-MK-125", 200, {
    supplyRef: "PO-52298",
    code: "PLAN-M04",
    text: "PO-52298 duplicates PO-52322; the 200 seal kits are not needed against current demand.",
  }),
  msg("shortage", "DP-100", 15, {
    demandRef: "SAFETY-DP-100",
    suggestedDate: day(10),
    delayDays: 0,
    code: "PLAN-M05",
    text: "On hand 6 ea is below safety stock 15 ea and no supply is planned before PL-30414.",
  }),
  msg("decrease", "RM-EPOXY-BLU", 12, {
    supplyRef: "PO-52290",
    demandRef: "MO-24621",
    code: "PLAN-M06",
    text: "Reduce PO-52290 by 12 gal: the coating requirement of the next two CP-200 orders is lower than ordered.",
  }),
  msg("increase", "BRG-6206", 300, {
    supplyRef: "PO-52326",
    demandRef: "SO-024181",
    code: "PLAN-M07",
    text: "Increase PO-52326 by 300 bearings to cover the CP-200 batch for Bluewater Municipal Utilities.",
  }),
  msg("advance", "SA-MTR-05", 30, { status: "applied", version: 2, decidedBy: USER.name, decidedAt: at(-1), decisionNote: "Pulled in with the customer's agreement.", supplyRef: "MO-24627", demandRef: "SO-024181", suggestedDate: day(4), delayDays: 2, code: "PLAN-M01", text: "Start MO-24627 two days earlier to protect SO-024181." }),
  msg("cancel", "FST-KIT-A", 100, { status: "dismissed", version: 2, decidedBy: "Priya Natarajan", decidedAt: at(-2), decisionNote: "Kits are reserved for the Dallas build.", supplyRef: "PO-52284", code: "PLAN-M04", text: "PO-52284 is not needed against current demand." }),
]);

const findNumber = (id: string) => ITEMS.find((i) => i.id === id)?.number ?? id;
const norm = (s: string | null) => (s ? s : undefined);

route("GET", `${BASE}/planned-orders`, (req) => {
  let rows = plannedOrders.all();
  const status = norm(req.query.get("status"));
  const item = norm(req.query.get("itemId"));
  rows = status ? rows.filter((o) => o.status === status) : rows.filter((o) => o.status !== "deleted");
  if (item) rows = rows.filter((o) => o.itemId === item);
  rows.sort((a, b) => a.deliveryDate.localeCompare(b.deliveryDate) || a.number.localeCompare(b.number));
  return { items: rows };
});

route("POST", `${BASE}/planned-orders:firm`, (req) => {
  const { plannedOrderIds, grouping } = req.body as { plannedOrderIds: string[]; grouping: FirmBatch["grouping"] };
  const groups: FirmBatch["groups"] = [];
  let n = 52360;
  for (const id of plannedOrderIds) {
    const o = plannedOrders.get(id);
    if (!o) return reply.problem(404, "PLAN-0404", `Planned order ${id} was not found.`);
    if (o.status === "firmed") return reply.problem(409, "PLAN-0002", `Planned order ${o.number} is already firmed.`);
    let targetId = "";
    if (o.orderType === "production") {
      const created = newOrder(o.itemId, Number(o.qty), Math.round((Date.parse(o.deliveryDate) - Date.now()) / 86_400_000));
      productionOrders.insert(created);
      targetId = created.id;
    } else {
      targetId = o.orderType === "purchase" ? `PO-${n++}` : `TO-${n++}`;
    }
    const targetType = o.orderType === "production" ? "production-order" : o.orderType === "purchase" ? "purchase-order" : "transfer-order";
    plannedOrders.update(id, { status: "firmed", version: o.version + 1, firmedTargetType: targetType, firmedTargetId: targetId });
    groups.push({ targetType, targetId, orderType: o.orderType, siteId: PLANT.id, vendorId: o.vendorId, deliveryDate: o.deliveryDate, qty: o.qty, plannedOrderIds: [id] });
  }
  return reply.created({ id: crypto.randomUUID(), grouping, firmed: plannedOrderIds.length, groups });
});

route("GET", `${BASE}/planned-orders/{id}/pegging`, (req) => {
  const o = plannedOrders.get(req.params.id);
  if (!o) return reply.problem(404, "PLAN-0404", "Planned order not found.");
  const qty = Number(o.qty);
  const demands: Record<string, string[]> = {
    "CP-400": ["SO-024184", "SO-024188"],
    "CP-200": ["SO-024181", "SO-024189"],
    "SA-IMP-06": ["MO-24615", "MO-24605"],
    "CST-IMP-06": ["MO-24618", "MO-24630"],
    "MTR-15HP-TEFC": ["MO-24615", "SO-024184"],
  };
  const number = itemByNumber(findNumber(o.itemId)).number;
  const refs = demands[number] ?? ["SAFETY-" + number];
  const parts = refs.map((_, i) => Math.round(i === refs.length - 1 ? qty - Math.round((qty * 0.6) * (refs.length - 1)) : qty * 0.6));
  const children: PegNode[] = refs.map((ref, i) => ({ ref, refType: ref.startsWith("SO-") ? "sales-order" : ref.startsWith("MO-") ? "production-order" : "safety-stock", qty: dec(Math.max(parts[i], 1), 0), children: [] }));
  const covered = children.reduce((n, c) => n + Number(c.qty), 0);
  if (covered < qty) children.push({ ref: "surplus", refType: "surplus", qty: dec(qty - covered, 0), children: [] });
  return { orderId: o.id, number: o.number, qty: o.qty, tree: { ref: o.number, refType: "planned-order", qty: o.qty, children } };
});

route("GET", `${BASE}/planned-orders/{id}`, (req) => {
  const o = plannedOrders.get(req.params.id);
  return o ? reply.versioned(o, o.version) : reply.problem(404, "PLAN-0404", "Planned order not found.");
});

route("PATCH", `${BASE}/planned-orders/{id}`, (req) => {
  const o = plannedOrders.get(req.params.id);
  if (!o) return reply.problem(404, "PLAN-0404", "Planned order not found.");
  const patch = req.body as PlannedOrderPatch;
  return reply.versioned(plannedOrders.update(o.id, { ...patch, version: o.version + 1 }), o.version + 1);
});

route("DELETE", `${BASE}/planned-orders/{id}`, (req) => {
  const o = plannedOrders.get(req.params.id);
  if (!o) return reply.problem(404, "PLAN-0404", "Planned order not found.");
  plannedOrders.update(o.id, { status: "deleted", version: o.version + 1 });
  return reply.noContent();
});

route("GET", `${BASE}/messages`, (req) => {
  const status = req.query.get("status") ?? "open";
  const action = req.query.get("action");
  let rows = messages.all();
  if (status !== "all") rows = rows.filter((m) => m.status === status);
  if (action) rows = rows.filter((m) => m.action === action);
  return { items: rows };
});

route("GET", `${BASE}/messages/{id}`, (req) => {
  const m = messages.get(req.params.id);
  return m ? reply.versioned(m, m.version) : reply.problem(404, "PLAN-0404", "Message not found.");
});

for (const decision of ["apply", "dismiss"] as const) {
  route("POST", `${BASE}/messages/{id}:${decision}`, (req) => {
    const m = messages.get(req.params.id);
    if (!m) return reply.problem(404, "PLAN-0404", "Message not found.");
    if (m.status !== "open") return reply.problem(409, "PLAN-0003", `The message is already ${m.status}.`);
    const note = (req.body as { note?: string } | undefined)?.note;
    const next = messages.update(m.id, { status: decision === "apply" ? "applied" : "dismissed", version: m.version + 1, decidedBy: USER.name, decidedAt: at(0), decisionNote: note ?? null });
    return reply.versioned(next, m.version + 1);
  });
}

route("GET", `${BASE}/plans/{code}/versions/{n}`, (req) => versions.find((v) => String(v.number) === req.params.n) ?? reply.problem(404, "PLAN-0404", "Version not found."));
route("GET", `${BASE}/plans/{code}/versions`, (req) => ({ items: versions.slice(0, Number(req.query.get("limit") ?? 50)) }));

// The BASE demand forecast: six months of the pumps at the Cedar Rapids plant. The next quarter is the consensus
// the planner submitted, waiting to be published; later months are the statistical baseline. Open sales reduce it.
type ForecastLineRow = { itemId: string; siteId: string; periodStart: string; periodEnd: string; source: "statistical" | "manual" | "consensus"; qty: string; salesQty: string; reducedQty: string };
const forecastModel = {
  code: "BASE",
  version: 7,
  reductionPrinciple: "transactions" as const,
  reductionPercent: "0",
  status: "submitted" as "draft" | "submitted" | "published",
  submittedBy: "Aisha Grant" as string | null,
  decidedBy: USER.name as string | null,
  publishedAt: at(-31) as string | null,
};
const MONTHLY: Record<string, number[]> = {
  "CP-200": [118, 124, 96, 132, 140, 151],
  "CP-400": [34, 38, 30, 41, 44, 46],
  "SP-150": [210, 185, 160, 240, 265, 280],
  "DP-100": [42, 45, 40, 48, 52, 50],
  "BP-300": [3, 2, 4, 3, 5, 4],
};
const OPEN_SALES: Record<string, number[]> = { "CP-200": [64, 22, 0], "CP-400": [18, 6, 0], "SP-150": [95, 30, 8], "DP-100": [25, 4, 0], "BP-300": [2, 1, 0] };
const monthStart = (offset: number) => {
  const d = new Date(`${day(0).slice(0, 7)}-01T00:00:00Z`);
  d.setUTCMonth(d.getUTCMonth() + offset);
  return d.toISOString().slice(0, 10);
};
const monthEnd = (offset: number) => new Date(Date.parse(`${monthStart(offset + 1)}T00:00:00Z`) - 86_400_000).toISOString().slice(0, 10);
const forecastLines: ForecastLineRow[] = Object.entries(MONTHLY).flatMap(([number, qty]) =>
  qty.map((q, m) => {
    const sales = OPEN_SALES[number][m] ?? 0;
    return { itemId: itemId(number), siteId: PLANT.id, periodStart: monthStart(m), periodEnd: monthEnd(m), source: m < 3 ? ("consensus" as const) : ("statistical" as const), qty: dec(q, 0), salesQty: dec(sales, 0), reducedQty: dec(Math.max(0, q - sales), 0) };
  }),
);

route("GET", `${BASE}/forecasts/{code}/lines`, (req) => {
  if (req.params.code !== "BASE") return reply.problem(404, "PLAN-FORECAST-NOT-FOUND", `Forecast model ${req.params.code} has no lines yet.`);
  let rows = forecastLines;
  if (req.query.get("itemId")) rows = rows.filter((l) => l.itemId === req.query.get("itemId"));
  if (req.query.get("siteId")) rows = rows.filter((l) => l.siteId === req.query.get("siteId"));
  return { model: forecastModel, items: rows.slice(0, Number(req.query.get("limit") ?? 500)) };
});
route("PUT", `${BASE}/forecasts/{code}/lines`, (req) => {
  if (forecastModel.status !== "draft") return reply.problem(409, "PLAN-CONSENSUS-STATE", "Only a draft forecast accepts changes; reject the submitted consensus first.");
  const body = req.body as { lines: { itemId: string; siteId: string; periodStart: string; periodEnd: string; qty: string; salesQty?: string }[] };
  for (const cell of body.lines ?? []) {
    const row = forecastLines.find((l) => l.itemId === cell.itemId && l.siteId === cell.siteId && l.periodStart === cell.periodStart);
    const sales = Number(cell.salesQty ?? row?.salesQty ?? 0);
    const next = { itemId: cell.itemId, siteId: cell.siteId, periodStart: cell.periodStart, periodEnd: cell.periodEnd, source: "manual" as const, qty: dec(Number(cell.qty), 0), salesQty: dec(sales, 0), reducedQty: dec(Math.max(0, Number(cell.qty) - sales), 0) };
    if (row) Object.assign(row, next);
    else forecastLines.push(next);
  }
  forecastModel.version += 1;
  return { model: forecastModel, updated: body.lines?.length ?? 0 };
});
for (const step of ["submit", "publish", "reject"] as const) {
  route("POST", `${BASE}/forecasts/{code}:${step}`, () => {
    const need = step === "submit" ? "draft" : "submitted";
    if (forecastModel.status !== need) return reply.problem(409, "PLAN-CONSENSUS-STATE", `The forecast is ${forecastModel.status}; it must be ${need} to ${step}.`);
    forecastModel.version += 1;
    if (step === "submit") Object.assign(forecastModel, { status: "submitted", submittedBy: USER.name });
    if (step === "publish") Object.assign(forecastModel, { status: "published", decidedBy: USER.name, publishedAt: at(0) });
    if (step === "reject") Object.assign(forecastModel, { status: "draft", decidedBy: USER.name });
    return { model: forecastModel, publication: null };
  });
}

// What-if scenarios of the MAIN plan: a demand surge from the utilities, a motor supplier slipping, a quiet winter.
interface ScenarioRow {
  id: string;
  code: string;
  version: number;
  planCode: string;
  name: string;
  demandFactor: string;
  itemFactors: { itemId: string; factor: string }[];
  addedDemand: { itemId: string; date: string; qty: string }[];
  leadTimeOverrides: { itemId: string; leadTimeDays: number }[];
  active: boolean;
}
const scenarios = table<ScenarioRow>([
  { id: "SC-UTILITY-SURGE", code: "UTILITY-SURGE", version: 3, planCode: "MAIN", name: "Municipal stimulus orders: CP-400 demand up 25 %", demandFactor: "1", itemFactors: [{ itemId: itemId("CP-400"), factor: "1.25" }, { itemId: itemId("CP-200"), factor: "1.10" }], addedDemand: [{ itemId: itemId("BP-300"), date: day(45), qty: "4" }], leadTimeOverrides: [], active: true },
  { id: "SC-MOTOR-DELAY", code: "MOTOR-DELAY", version: 2, planCode: "MAIN", name: "Volta Motor Works lead time slips to six weeks", demandFactor: "1", itemFactors: [], addedDemand: [], leadTimeOverrides: [{ itemId: itemId("MTR-15HP-TEFC"), leadTimeDays: 42 }, { itemId: itemId("MTR-5HP-TEFC"), leadTimeDays: 35 }], active: true },
  { id: "SC-SECOND-SHIFT", code: "BAL-2ND-SHIFT", version: 1, planCode: "MAIN", name: "Second balancing shift: impeller lead time down to 2 days", demandFactor: "1", itemFactors: [], addedDemand: [], leadTimeOverrides: [{ itemId: itemId("SA-IMP-06"), leadTimeDays: 2 }], active: true },
  { id: "SC-SLOW-WINTER", code: "SLOW-WINTER", version: 4, planCode: "MAIN", name: "Quiet winter: all demand down 15 %", demandFactor: "0.85", itemFactors: [], addedDemand: [], leadTimeOverrides: [], active: false },
]);
let simulationSeq = 100;
route("GET", `${BASE}/scenarios`, (req) => {
  const plan = req.query.get("planCode");
  return { items: scenarios.all().filter((s) => !plan || s.planCode === plan) };
});
route("POST", `${BASE}/scenarios`, (req) => {
  const body = req.body as Omit<ScenarioRow, "id" | "version">;
  if (scenarios.all().some((s) => s.code === body.code)) return reply.problem(409, "PLAN-SCENARIO-EXISTS", `Scenario ${body.code} already exists.`);
  return reply.created(scenarios.insert({ ...body, id: `SC-${body.code}`, version: 1 }));
});
route("POST", `${BASE}/scenarios/{code}:run`, (req) => {
  const s = scenarios.all().find((x) => x.code === req.params.code);
  if (!s) return reply.problem(404, "PLAN-0404", "Scenario not found.");
  simulationSeq += 1;
  const extra = s.itemFactors.length * 2 + s.addedDemand.length + s.leadTimeOverrides.length * 3;
  const run: PlanVersion = { ...version, id: `SIM-${simulationSeq}`, number: simulationSeq, status: "complete", kind: "simulation", scenarioCode: s.code, asOf: at(0), plannedOrders: version.plannedOrders + extra, messages: version.messages + Math.ceil(extra / 2), durationMs: 1650 + extra * 40, approvedFirmed: 0 };
  return reply.created(run);
});
route("GET", `${BASE}/scenarios/{code}`, (req) => {
  const s = scenarios.all().find((x) => x.code === req.params.code);
  return s ? reply.versioned(s, s.version) : reply.problem(404, "PLAN-0404", "Scenario not found.");
});
route("PUT", `${BASE}/scenarios/{code}`, (req) => {
  const s = scenarios.all().find((x) => x.code === req.params.code);
  if (!s) return reply.problem(404, "PLAN-0404", "Scenario not found.");
  const next = scenarios.update(s.id, { ...(req.body as Partial<ScenarioRow>), id: s.id, code: s.code, version: s.version + 1 })!;
  return reply.versioned(next, next.version);
});
