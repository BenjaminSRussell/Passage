// Lean manufacturing (/api/v1/lean) for the demo: two production flows feeding the pump assembly line, the kanban
// rules that pull impeller and motor assemblies through them and replenish seal kits, bearings and fasteners at line
// side, and the kanban board. Items, sites and work cells carry the real ids of company.ts.
import type { Activity, Board, Flow, Kanban, KanbanJob, KanbanState, Rule } from "../../../modules/lean/api/client";
import { KANBAN_STATES } from "../../../modules/lean/api/client";
import { reply, route } from "../../backend";
import { PLANT, itemByNumber, resourceByCode } from "../../company";
import { at, day, dec, table, uid } from "../../store";

const BASE = "/api/v1/lean";
const KIND_ACTIVITY = 270;
const VALUE_STREAM = uid(271, 1);

function activity(flow: number, sequence: number, name: string, kind: Activity["kind"], cycleTimeS: number, resource: string | null, predecessors: number[]): Activity {
  return {
    id: uid(KIND_ACTIVITY, flow * 100 + sequence),
    sequence,
    name,
    kind,
    cycleTimeS,
    workCellResourceId: resource ? resourceByCode(resource).id : null,
    inputLocationId: null,
    outputLocationId: null,
    predecessors,
    pickingRequired: sequence === 10,
  };
}

const flows = table<Flow>([
  {
    id: "FLOW-IMP-2",
    valueStreamId: VALUE_STREAM,
    siteId: PLANT.id,
    name: "Impeller cell: machine and balance",
    taktTimeS: 720,
    status: "active",
    activeFrom: day(-95),
    activeTo: null,
    version: 4,
    flowVersion: 2,
    previousFlowId: "FLOW-IMP-1",
    activityCount: 3,
    activities: [activity(1, 10, "Machine bore and hub", "process", 690, "MCH-01", []), activity(1, 20, "Dynamic balance", "process", 790, "BAL-01", [10]), activity(1, 30, "Move to the assembly supermarket", "transfer", 240, null, [20])],
    createdAt: at(-120),
    updatedAt: at(-95),
  },
  {
    id: "FLOW-MTR-1",
    valueStreamId: VALUE_STREAM,
    siteId: PLANT.id,
    name: "Motor and coupling cell",
    taktTimeS: 900,
    status: "active",
    activeFrom: day(-260),
    activeTo: null,
    version: 2,
    flowVersion: 1,
    previousFlowId: null,
    activityCount: 2,
    activities: [activity(2, 10, "Mount motor and coupling", "process", 840, "ASM-01", []), activity(2, 20, "Deliver to line side", "transfer", 180, null, [10])],
    createdAt: at(-270),
    updatedAt: at(-260),
  },
  {
    id: "FLOW-IMP-1",
    valueStreamId: VALUE_STREAM,
    siteId: PLANT.id,
    name: "Impeller cell: machine and balance",
    taktTimeS: 840,
    status: "expired",
    activeFrom: day(-400),
    activeTo: day(-96),
    version: 3,
    flowVersion: 1,
    previousFlowId: null,
    activityCount: 2,
    activities: [activity(3, 10, "Machine bore and hub", "process", 750, "MCH-01", []), activity(3, 20, "Dynamic balance", "process", 820, "BAL-01", [10])],
    createdAt: at(-410),
    updatedAt: at(-96),
  },
]);

interface RuleSpec {
  code: string;
  item: string;
  type: Rule["ruleType"];
  qty: number;
  cards: number;
  leadTimeH: number;
  flowId: string | null;
}
const RULES: RuleSpec[] = [
  { code: "KR-IMP-06", item: "SA-IMP-06", type: "manufacturing", qty: 10, cards: 6, leadTimeH: 16, flowId: "FLOW-IMP-2" },
  { code: "KR-MTR-05", item: "SA-MTR-05", type: "manufacturing", qty: 5, cards: 4, leadTimeH: 8, flowId: "FLOW-MTR-1" },
  { code: "KR-SEAL-125", item: "SEAL-MK-125", type: "withdrawal", qty: 20, cards: 5, leadTimeH: 4, flowId: null },
  { code: "KR-BRG-6206", item: "BRG-6206", type: "withdrawal", qty: 50, cards: 4, leadTimeH: 4, flowId: null },
  { code: "KR-FST-A", item: "FST-KIT-A", type: "withdrawal", qty: 25, cards: 3, leadTimeH: 2, flowId: null },
];
const ruleId = (code: string) => `RULE-${code.slice(3)}`;

// Card states per rule, in card order: the impeller loop is starved behind the balancer, the rest flow.
const STATES: Record<string, KanbanState[]> = {
  "KR-IMP-06": ["in-progress", "prepared", "planned", "not-planned", "not-planned", "emptied"],
  "KR-MTR-05": ["completed", "in-progress", "planned", "completed"],
  "KR-SEAL-125": ["completed", "completed", "emptied", "planned", "completed"],
  "KR-BRG-6206": ["completed", "emptied", "completed", "prepared"],
  "KR-FST-A": ["completed", "not-planned", "completed"],
};
const DUE_HOURS: Record<KanbanState, number> = { "not-planned": 30, planned: 14, prepared: 6, "in-progress": -2, completed: -10, emptied: -20 };

const kanbans = table<Kanban>([]);
const kanbanJobs = table<KanbanJob>([]);
RULES.forEach((r, ri) => {
  const flow = r.flowId ? flows.get(r.flowId) : undefined;
  STATES[r.code].forEach((state, i) => {
    const id = `K-${r.code.slice(3)}-${String(i + 1).padStart(2, "0")}`;
    const due = state === "completed" || state === "emptied" ? null : at((DUE_HOURS[state] + i * 1.5 - ri) / 24);
    const card: Kanban = {
      id,
      ruleId: ruleId(r.code),
      ruleCode: r.code,
      itemId: itemByNumber(r.item).id,
      ruleType: r.type,
      replenishment: "fixed",
      cardNumber: `${r.code.slice(3)}-${String(i + 1).padStart(3, "0")}`,
      qty: dec(r.qty, 0),
      state,
      due,
      eventRefType: null,
      eventRefId: null,
      handlingUnits: state === "completed" ? Math.ceil(r.qty / (r.type === "manufacturing" ? 5 : 25)) : null,
      jobCount: flow?.activities?.length ?? 0,
      version: 3 + i,
      createdAt: at(-60 - ri),
      updatedAt: at(-(i + 1) * 0.15),
    };
    kanbans.insert(card);
    (flow?.activities ?? []).forEach((a, ai) => {
      const doneBefore = state === "completed" || state === "emptied";
      const running = state === "in-progress";
      const jobState: KanbanJob["state"] = doneBefore || (running && ai === 0) ? "completed" : running && ai === 1 ? "in-progress" : state === "not-planned" ? "not-planned" : state === "planned" ? "planned" : "prepared";
      kanbanJobs.insert({
        id: `${id}-J${a.sequence}`,
        kanbanId: id,
        cardNumber: card.cardNumber,
        ruleId: card.ruleId,
        ruleCode: card.ruleCode,
        itemId: card.itemId,
        ruleType: card.ruleType,
        cardState: state,
        cardQty: card.qty,
        due,
        activityId: a.id,
        activityName: a.name,
        activityKind: a.kind,
        sequence: a.sequence,
        workCellResourceId: a.workCellResourceId ?? null,
        pickingRequired: a.pickingRequired,
        state: jobState,
        startedAt: jobState === "completed" || jobState === "in-progress" ? at(-(4 - ai) / 24 - i * 0.1) : null,
        completedAt: jobState === "completed" ? at(-(3 - ai) / 24 - i * 0.1) : null,
        reportedQty: jobState === "completed" ? card.qty : null,
        scrapQty: jobState === "completed" ? (a.sequence === 10 && i === 0 && r.code === "KR-IMP-06" ? "1" : "0") : null,
        handlingUnits: jobState === "completed" && ai === (flow?.activities?.length ?? 1) - 1 ? card.handlingUnits : null,
        version: 2,
        updatedAt: at(-0.1),
      });
    });
  });
});

const rules = table<Rule>(
  RULES.map((r, i) => ({
    id: ruleId(r.code),
    code: r.code,
    itemId: itemByNumber(r.item).id,
    siteId: PLANT.id,
    flowId: r.flowId,
    ruleType: r.type,
    replenishment: "fixed",
    fixedQty: dec(r.qty, 0),
    numberOfKanbans: r.cards,
    leadTimeH: r.leadTimeH,
    eventSource: null,
    exactQty: false,
    effectiveFrom: day(-200 + i * 20),
    effectiveTo: null,
    version: 2,
    activeKanbans: STATES[r.code].filter((s) => s !== "emptied").length,
    createdAt: at(-210 + i * 20),
  })),
);

const listedFlow = ({ activities: _a, ...f }: Flow): Flow => (void _a, f);
route("GET", `${BASE}/flows`, (req) => {
  const status = req.query.get("status");
  return { items: flows.all().filter((f) => !status || f.status === status).map(listedFlow) };
});
route("GET", `${BASE}/flows/{id}/costing-periods`, () => ({ items: [] }));
route("GET", `${BASE}/flows/{id}`, (req) => flows.get(req.params.id) ?? reply.problem(404, "LEAN-0404", "Flow not found."));
route("GET", `${BASE}/kanban-rules`, (req) => {
  const flowId = req.query.get("flowId");
  return { items: rules.all().filter((r) => !flowId || r.flowId === flowId) };
});
route("GET", `${BASE}/kanban-rules/{id}`, (req) => rules.get(req.params.id) ?? reply.problem(404, "LEAN-0404", "Kanban rule not found."));

const withJobs = (k: Kanban): Kanban => ({ ...k, jobs: kanbanJobs.all().filter((j) => j.kanbanId === k.id).sort((a, b) => a.sequence - b.sequence) });

route("GET", `${BASE}/board`, (req) => {
  const q = req.query;
  const workCell = q.get("workCellId");
  if (workCell) {
    const jobs = kanbanJobs.all().filter((j) => j.workCellResourceId === workCell && (!q.get("ruleId") || j.ruleId === q.get("ruleId")));
    return { workCellId: workCell, columns: (["not-planned", "planned", "prepared", "in-progress", "completed"] as const).map((state) => ({ state, jobs: jobs.filter((j) => j.state === state) })), truncated: false, takt: null };
  }
  const flowId = q.get("flowId");
  const rid = q.get("ruleId");
  const ruleIds = new Set(rules.all().filter((r) => (!flowId || r.flowId === flowId) && (!rid || r.id === rid)).map((r) => r.id));
  const cards = kanbans.all().filter((k) => ruleIds.has(k.ruleId));
  const flow = flowId ? flows.get(flowId) : undefined;
  const slowest = flow?.activities?.reduce((a, b) => (b.cycleTimeS > a.cycleTimeS ? b : a));
  const board: Board = {
    columns: KANBAN_STATES.map((state) => ({ state, kanbans: cards.filter((k) => k.state === state).sort((a, b) => (a.due ?? "~").localeCompare(b.due ?? "~")) })),
    truncated: false,
    takt: flow?.taktTimeS && slowest ? { taktTimeS: flow.taktTimeS, cycleTimeS: slowest.cycleTimeS, bottleneckActivity: slowest.name, withinTakt: slowest.cycleTimeS <= flow.taktTimeS } : null,
  };
  return board;
});
route("GET", `${BASE}/kanbans/{id}`, (req) => {
  const k = kanbans.get(req.params.id);
  return k ? withJobs(k) : reply.problem(404, "LEAN-0404", "Kanban not found.");
});
route("POST", `${BASE}/kanbans/{id}:move`, (req) => {
  const k = kanbans.get(req.params.id);
  if (!k) return reply.problem(404, "LEAN-0404", "Kanban not found.");
  const { to, version } = req.body as { to: KanbanState; version: number };
  if (version !== undefined && version !== k.version) return reply.problem(412, "LEAN-0412", "Someone moved this card in the meantime; refresh the board.");
  const patch: Partial<Kanban> = { state: to, version: k.version + 1, updatedAt: at(0) };
  if (to === "planned") patch.due = at(k.ruleType === "manufacturing" ? 0.6 : 0.2);
  if (to === "completed" || to === "emptied") patch.due = null;
  for (const j of kanbanJobs.all().filter((x) => x.kanbanId === k.id)) {
    kanbanJobs.update(j.id, { cardState: to, state: to === "not-planned" || to === "planned" || to === "prepared" ? to : j.state, due: patch.due ?? j.due });
  }
  return reply.ok(kanbans.update(k.id, patch));
});
