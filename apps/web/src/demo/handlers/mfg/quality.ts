// Quality service (/api/v1/quality) for the demo: quality orders with their test groups, results and usage decisions.
// The quality screens show the first eight characters of order ids, so order ids here are readable codes (QO-24007);
// items, sites and warehouses carry the real ids of company.ts.
import type { Decision, DecisionLine, Instrument, NewOrder, Order, OrderView, Result, TestGroup } from "../../../modules/quality/api/client";
import { reply, route } from "../../backend";
import { USER, PLANT, WH_FG, WH_RAW, itemByNumber } from "../../company";
import { at, dec, table, uid } from "../../store";

const BASE = "/api/v1/quality";

const GROUPS: Record<string, TestGroup> = {
  pump: {
    id: uid(250, 1),
    code: "PUMP-FG-FINAL",
    version: 3,
    tests: [
      { testId: "HYDRO", name: "Hydrostatic pressure hold, psi", kind: "VARIABLE", required: true, decimals: 0, min: "450", max: "520", destructive: false },
      { testId: "FLOW", name: "Flow at rated head, gpm", kind: "VARIABLE", required: true, decimals: 1, min: "94.0", max: "106.0", destructive: false },
      { testId: "COAT", name: "Coating thickness and finish", kind: "ATTRIBUTE", required: true, decimals: 0, passingOutcomeIds: ["PASS"], destructive: false },
    ],
  },
  casting: {
    id: uid(250, 2),
    code: "CASTING-IN",
    version: 2,
    tests: [
      { testId: "HARD", name: "Hardness, HB", kind: "VARIABLE", required: true, decimals: 0, min: "150", max: "210", destructive: false },
      { testId: "DIM", name: "Bore diameter, mm", kind: "VARIABLE", required: true, decimals: 2, min: "49.95", max: "50.05", destructive: false },
      { testId: "POR", name: "Porosity visual", kind: "ATTRIBUTE", required: true, decimals: 0, passingOutcomeIds: ["PASS"], destructive: false },
    ],
  },
  motor: {
    id: uid(250, 3),
    code: "MOTOR-IN",
    version: 1,
    tests: [
      { testId: "IR", name: "Insulation resistance, MOhm", kind: "VARIABLE", required: true, decimals: 0, min: "100", destructive: false },
      { testId: "NAME", name: "Nameplate and rotation check", kind: "ATTRIBUTE", required: true, decimals: 0, passingOutcomeIds: ["PASS"], destructive: false },
    ],
  },
};

const INSTRUMENTS: Instrument[] = [
  { id: "INS-PG01", code: "PG-01", name: "Test stand pressure transducer", calibrationIntervalDays: 180, lastCalibratedAt: at(-171), dueAt: at(9), calibrationStatus: "DUE_SOON" },
  { id: "INS-FM02", code: "FM-02", name: "Magnetic flowmeter", calibrationIntervalDays: 365, lastCalibratedAt: at(-120), dueAt: at(245), calibrationStatus: "IN_CALIBRATION" },
  { id: "INS-CAL3", code: "CAL-03", name: "Digital bore gauge", calibrationIntervalDays: 90, lastCalibratedAt: at(-98), dueAt: at(-8), calibrationStatus: "OVERDUE" },
];

interface Spec {
  n: number;
  trigger: Order["trigger"];
  ref: [string, string];
  item: string;
  group: keyof typeof GROUPS;
  lot: number;
  sample: number;
  ac: number;
  re: number;
  letter: string;
  status: Order["status"];
  ageDays: number;
  /** Samples that carry a failing result. */
  failing?: number[];
  /** Results recorded: all samples, or only the first n. */
  recorded?: "all" | number;
  cert?: Order["certificateRequired"];
  batch?: string;
}

const SPECS: Spec[] = [
  { n: 24007, trigger: "PRODUCTION_RAF", ref: ["production-order", "MO-24608"], item: "CP-200", group: "pump", lot: 40, sample: 8, ac: 1, re: 2, letter: "D", status: "PENDING_DECISION", ageDays: 0.4, failing: [5], recorded: "all", batch: "B24608" },
  { n: 24006, trigger: "PURCHASE_RECEIPT", ref: ["purchase-order", "PO-52317"], item: "CST-IMP-06", group: "casting", lot: 120, sample: 20, ac: 1, re: 2, letter: "F", status: "RESULTS_IN_PROGRESS", ageDays: 0.8, recorded: 6, cert: "EN10204_31", batch: "IR-9931" },
  { n: 24005, trigger: "PURCHASE_RECEIPT", ref: ["purchase-order", "PO-52331"], item: "MTR-15HP-TEFC", group: "motor", lot: 6, sample: 3, ac: 0, re: 1, letter: "B", status: "OPEN", ageDays: 1.1, cert: "COC", batch: "VM-4410" },
  { n: 24004, trigger: "SALES_PICK", ref: ["sales-order", "SO-024188"], item: "CP-400", group: "pump", lot: 3, sample: 3, ac: 0, re: 1, letter: "B", status: "CREATED", ageDays: 0.2 },
  { n: 24003, trigger: "PRODUCTION_RAF", ref: ["production-order", "MO-24575"], item: "SA-IMP-06", group: "pump", lot: 40, sample: 8, ac: 1, re: 2, letter: "D", status: "CLOSED", ageDays: 14, failing: [2, 6], recorded: "all", batch: "B24575" },
  { n: 24002, trigger: "PRODUCTION_RAF", ref: ["production-order", "MO-24592"], item: "CP-400", group: "pump", lot: 8, sample: 5, ac: 0, re: 1, letter: "C", status: "CLOSED", ageDays: 5, recorded: "all", batch: "B24592" },
  { n: 24001, trigger: "PURCHASE_RECEIPT", ref: ["purchase-order", "PO-52284"], item: "SEAL-MK-125", group: "casting", lot: 200, sample: 32, ac: 2, re: 3, letter: "G", status: "CLOSED", ageDays: 9, recorded: "all", batch: "KS-7720" },
  { n: 24000, trigger: "PRODUCTION_RAF", ref: ["production-order", "MO-24580"], item: "CP-200", group: "pump", lot: 25, sample: 5, ac: 0, re: 1, letter: "C", status: "CLOSED", ageDays: 11, recorded: "all", batch: "B24580" },
];

const instrumentFor = (testId: string) => (testId === "HYDRO" ? "INS-PG01" : testId === "FLOW" ? "INS-FM02" : testId === "DIM" ? "INS-CAL3" : undefined);

function valueFor(test: TestGroup["tests"][number], sampleNo: number, fail: boolean): { value?: string; outcomeId?: string; passed: boolean } {
  if (test.kind === "ATTRIBUTE") return fail ? { outcomeId: "FAIL", passed: false } : { outcomeId: "PASS", passed: true };
  const min = Number(test.min ?? 0);
  const max = test.max === undefined ? min * 1.6 : Number(test.max);
  const mid = (min + max) / 2;
  const span = (max - min) / 2;
  const jitter = (((sampleNo * 37 + test.testId.length * 11) % 17) / 17 - 0.5) * span * 0.7;
  const value = fail ? max + span * 0.15 : mid + jitter;
  return { value: dec(value, test.decimals), passed: value >= min && value <= max };
}

function buildResults(s: Spec, group: TestGroup): Result[] {
  if (s.recorded === undefined) return [];
  const samples = s.recorded === "all" ? s.sample : s.recorded;
  const out: Result[] = [];
  for (let sampleNo = 1; sampleNo <= samples; sampleNo++) {
    const failing = s.failing?.includes(sampleNo) ?? false;
    group.tests.forEach((test, ti) => {
      // A failing sample fails its first test only.
      const v = valueFor(test, sampleNo, failing && ti === 0);
      out.push({
        testId: test.testId,
        sampleNo,
        retestNo: 0,
        value: v.value,
        outcomeId: v.outcomeId,
        passed: v.passed,
        instrumentId: instrumentFor(test.testId),
        recordedBy: ti % 2 ? "Priya Natarajan" : USER.name,
        recordedAt: at(-s.ageDays + 0.05 + sampleNo * 0.004),
      });
    });
  }
  return out;
}

function evaluate(group: TestGroup, results: Result[], samples: number): OrderView["evaluation"] {
  const defective = new Set(results.filter((r) => !r.passed).map((r) => r.sampleNo)).size;
  const missing = group.tests.filter((t) => t.required && new Set(results.filter((r) => r.testId === t.testId).map((r) => r.sampleNo)).size < samples).map((t) => t.testId);
  return { defectiveSamples: defective, passed: false, missingRequired: missing };
}

function decisionFor(s: Spec, view: OrderView): Decision | undefined {
  if (s.status !== "CLOSED") return undefined;
  const lot = s.lot;
  const rejected = view.evaluation.defectiveSamples >= s.re ? Math.round(lot * 0.2) : 0;
  const lines: DecisionLine[] = rejected > 0
    ? [
        { decision: "ACCEPT", quantity: dec(lot - rejected), reason: "Remaining lot passed re-sort." },
        { decision: "REWORK", quantity: dec(rejected), reason: "Test readings out of range on 2 samples; unit returned to the line." },
      ]
    : [{ decision: "ACCEPT", quantity: dec(lot) }];
  return {
    id: `DEC-${s.n}`,
    orderId: `QO-${s.n}`,
    lines,
    split: { acceptQty: dec(lot - rejected), rejectQty: dec(0), deviationQty: dec(0) },
    decidedBy: USER.name,
    decidedAt: at(-s.ageDays + 0.5),
    nonconformanceId: rejected > 0 ? "NCR-1042" : undefined,
  };
}

function build(s: Spec): { order: Order; view: OrderView } {
  const group = GROUPS[s.group];
  const results = buildResults(s, group);
  const order: Order = {
    id: `QO-${s.n}`,
    trigger: s.trigger,
    sourceRef: { type: s.ref[0], id: s.ref[1] },
    itemId: itemByNumber(s.item).id,
    siteId: PLANT.id,
    warehouseId: s.trigger === "PURCHASE_RECEIPT" ? WH_RAW.id : WH_FG.id,
    dims: s.batch ? { batch: s.batch } : {},
    lotQty: dec(s.lot, 6),
    sampleQty: dec(s.sample, 6),
    uom: "ea",
    testGroupId: group.id,
    testGroupVersion: group.version,
    samplingMethod: "AQL",
    severity: "NORMAL",
    codeLetter: s.letter,
    acceptNo: s.ac,
    rejectNo: s.re,
    status: s.status,
    version: 1,
    createdAt: at(-s.ageDays),
    closedAt: s.status === "CLOSED" ? at(-s.ageDays + 0.5) : undefined,
    certificateRequired: s.cert ?? "NONE",
  };
  const evaluation = evaluate(group, results, s.sample);
  evaluation.passed = evaluation.missingRequired.length === 0 ? evaluation.defectiveSamples < s.re : false;
  const view: OrderView = { ...order, testGroup: group, results, evaluation, certificateStatus: s.cert ? "MISSING" : "NOT_REQUIRED" };
  if (s.cert && s.status === "CLOSED") view.certificateStatus = "RECEIVED_OK";
  view.decision = decisionFor(s, view);
  return { order, view };
}

const built = SPECS.map(build);
const views = table<OrderView & { id: string }>(built.map((b) => b.view));

const asOrder = (v: OrderView): Order => {
  const { testGroup: _g, results: _r, evaluation: _e, certificateStatus: _c, decision: _d, ...order } = v;
  void [_g, _r, _e, _c, _d];
  return order;
};

route("GET", `${BASE}/quality-orders`, (req) => {
  const status = req.query.get("status");
  const site = req.query.get("siteId");
  let rows = views.all();
  if (status) rows = rows.filter((v) => v.status === status);
  if (site) rows = rows.filter((v) => v.siteId === site);
  rows.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items: rows.map(asOrder) };
});

let nextOrder = 24010;
route("POST", `${BASE}/quality-orders`, (req) => {
  const input = req.body as NewOrder;
  const group = Object.values(GROUPS).find((g) => g.id === input.testGroupId) ?? GROUPS.pump;
  const lot = Number(input.lotQty);
  const plan = input.samplingPlan;
  const sample = Math.max(1, Math.min(lot, plan?.method === "FIXED" ? Number(plan.fixedQty ?? 5) : plan?.method === "PERCENT" ? Math.ceil((lot * Number(plan.percent ?? 10)) / 100) : Math.min(lot, 8)));
  const order: Order = {
    id: `QO-${nextOrder++}`,
    trigger: input.trigger,
    sourceRef: input.sourceRef,
    itemId: input.itemId,
    siteId: input.siteId,
    warehouseId: input.warehouseId,
    dims: input.dims ?? {},
    lotQty: dec(lot, 6),
    sampleQty: dec(sample, 6),
    uom: input.uom,
    testGroupId: group.id,
    testGroupVersion: group.version,
    samplingMethod: plan?.method ?? "AQL",
    severity: plan?.severity ?? "NORMAL",
    codeLetter: "D",
    acceptNo: 1,
    rejectNo: 2,
    status: "OPEN",
    version: 1,
    createdAt: at(0),
    certificateRequired: input.certificateRequired ?? "NONE",
  };
  const evaluation = evaluate(group, [], sample);
  views.insert({ ...order, testGroup: group, results: [], evaluation, certificateStatus: input.certificateRequired && input.certificateRequired !== "NONE" ? "MISSING" : "NOT_REQUIRED" });
  return reply.created(order, { ETag: '"1"' });
});

route("POST", `${BASE}/quality-orders/{id}/results`, (req) => {
  const v = views.get(req.params.id);
  if (!v) return reply.problem(404, "QUAL-0404", "Quality order not found.");
  const incoming = (req.body as { results: Result[] }).results.map((r) => ({ ...r, retestNo: r.retestNo ?? 0, passed: r.outcomeId ? r.outcomeId === "PASS" : isInSpec(v, r), recordedBy: USER.name, recordedAt: at(0) }));
  const results = [...v.results, ...incoming];
  const evaluation = evaluate(v.testGroup, results, Number(v.sampleQty));
  evaluation.passed = evaluation.missingRequired.length === 0 ? evaluation.defectiveSamples < v.rejectNo : false;
  const status = evaluation.missingRequired.length === 0 ? "PENDING_DECISION" : "RESULTS_IN_PROGRESS";
  return reply.created(views.update(v.id, { results, evaluation, status, version: v.version + 1 }));
});

function isInSpec(v: OrderView, r: { testId: string; value?: string }): boolean {
  const t = v.testGroup.tests.find((x) => x.testId === r.testId);
  const n = Number(r.value);
  return !t || Number.isNaN(n) || ((t.min === undefined || n >= Number(t.min)) && (t.max === undefined || n <= Number(t.max)));
}

route("POST", `${BASE}/quality-orders/{id}:usage-decision`, (req) => {
  const v = views.get(req.params.id);
  if (!v) return reply.problem(404, "QUAL-0404", "Quality order not found.");
  const lines = (req.body as { lines: DecisionLine[] }).lines;
  const sum = (kind: DecisionLine["decision"][]) => lines.filter((l) => kind.includes(l.decision)).reduce((n, l) => n + Number(l.quantity), 0);
  const decision: Decision = {
    id: `DEC-${v.id.slice(3)}`,
    orderId: v.id,
    lines,
    split: { acceptQty: dec(sum(["ACCEPT"])), rejectQty: dec(sum(["REJECT", "SCRAP", "RETURN_TO_VENDOR", "REWORK"])), deviationQty: dec(sum(["ACCEPT_WITH_DEVIATION"])) },
    decidedBy: USER.name,
    decidedAt: at(0),
  };
  const order = views.update(v.id, { status: "CLOSED", closedAt: at(0), decision, version: v.version + 1 }) as OrderView;
  return reply.ok({ order, decision });
});

route("GET", `${BASE}/quality-orders/{id}/certificates`, (req) => {
  const v = views.get(req.params.id);
  return v ? { required: v.certificateRequired, certificateStatus: v.certificateStatus, items: [] } : reply.problem(404, "QUAL-0404", "Quality order not found.");
});
route("GET", `${BASE}/quality-orders/{id}/coas`, () => ({ items: [] }));
route("GET", `${BASE}/samples`, () => ({ items: [] }));
route("GET", `${BASE}/quality-orders/{id}`, (req) => {
  const v = views.get(req.params.id);
  return v ? reply.versioned(v, v.version) : reply.problem(404, "QUAL-0404", `Quality order ${req.params.id} was not found.`);
});

route("GET", `${BASE}/instruments`, () => ({ items: INSTRUMENTS }));
route("GET", `${BASE}/test-groups`, () => ({ items: Object.values(GROUPS) }));
