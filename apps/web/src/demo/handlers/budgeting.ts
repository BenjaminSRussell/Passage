// Budgeting service (/api/v1/budget) for the demo: control rule versions and budget plans with scenarios, the plan grid,
// spreading and scenario comparison. Typed against the module's own client types so shape drift fails `tsc`.
import type { ActualRow, Balance, Rule } from "../../modules/budgeting/api/client";
import type { Comparison, Plan, PlanCell, PlanScenario, StageOwner } from "../../modules/budgeting/api/plans";
import { LEGAL_ENTITY } from "../company";
import { DemoResponse, reply, route } from "../backend";
import { at, newId, table, uid } from "../store";

const BASE = "/api/v1/budget";
const K_RULE = 365;
const K_PLAN = 366;
const K_SCENARIO = 367;
const K_UNIT = 368;

// Money is kept in ten-thousandths so sums stay exact; the services send decimal strings.
const units = (s: string): number => Math.round(Number(s) * 10_000);
const dec = (u: number): string => {
  const text = (u / 10_000).toFixed(4);
  return text.replace(/(\.\d*?)0+$/, "$1").replace(/\.$/, "");
};

const YEAR = new Date().getFullYear() + 1;
const periods = Array.from({ length: 12 }, (_, i) => `${YEAR}-${String(i + 1).padStart(2, "0")}`);

const rules = table<Rule>([
  { id: uid(K_RULE, 1), legalEntityId: LEGAL_ENTITY.id, code: "DEPT", version: 3, warnPct: "90", blockPct: "105", currency: "USD", effectiveFrom: `${YEAR - 1}-01-01` },
  { id: uid(K_RULE, 2), legalEntityId: LEGAL_ENTITY.id, code: "CAPEX", version: 1, warnPct: "95", blockPct: "100", currency: "USD", effectiveFrom: `${YEAR - 1}-01-01` },
]);

const orgUnits = [
  { id: uid(K_UNIT, 1), code: "CR-PLANT", name: "Cedar Rapids plant operations" },
  { id: uid(K_UNIT, 2), code: "DAL-DC", name: "Dallas distribution center" },
  { id: uid(K_UNIT, 3), code: "ADMIN", name: "Corporate administration" },
];

interface StoredPlan extends Plan {
  scenarios: PlanScenario[];
  stageOwners: StageOwner[];
}

const BASELINE = uid(K_SCENARIO, 1);
const OPTIMISTIC = uid(K_SCENARIO, 2);

const plans = table<StoredPlan>([
  {
    id: uid(K_PLAN, 1),
    legalEntityId: LEGAL_ENTITY.id,
    ruleVersionId: uid(K_RULE, 1),
    name: `FY${YEAR} operating budget`,
    fiscalYear: YEAR,
    currency: "USD",
    status: "Draft",
    submission: 0,
    version: 4,
    createdBy: "ana.ortiz",
    createdAt: at(-14),
    scenarios: [
      { id: BASELINE, name: "Baseline", kind: "Baseline", version: 6, total: "0" },
      { id: OPTIMISTIC, name: "Higher volume", kind: "Optimistic", copiedFromId: BASELINE, version: 2, total: "0" },
    ],
    stageOwners: [],
  },
  {
    id: uid(K_PLAN, 2),
    legalEntityId: LEGAL_ENTITY.id,
    ruleVersionId: uid(K_RULE, 1),
    name: `FY${YEAR - 1} operating budget`,
    fiscalYear: YEAR - 1,
    currency: "USD",
    status: "Approved",
    submission: 1,
    submittedBy: "ana.ortiz",
    approvedScenarioId: uid(K_SCENARIO, 3),
    version: 9,
    createdBy: "ana.ortiz",
    createdAt: at(-380),
    scenarios: [{ id: uid(K_SCENARIO, 3), name: "Baseline", kind: "Baseline", version: 3, total: "0" }],
    stageOwners: [
      { stage: "Review", orgUnitId: uid(K_UNIT, 1), owner: "dana.kim", signedBy: "dana.kim", signedAt: at(-370) },
      { stage: "Approve", orgUnitId: uid(K_UNIT, 1), owner: "marcus.lee", signedBy: "marcus.lee", signedAt: at(-365) },
    ],
  },
]);

const cells = new Map<string, PlanCell[]>();

function seed(scenarioId: string, year: number, factor: number) {
  const accounts: [string, string, number][] = [
    ["6100", "CR-PLANT", 118_000],
    ["6100", "DAL-DC", 46_000],
    ["6200", "CR-PLANT", 21_500],
    ["6200", "DAL-DC", 8_400],
    ["6300", "CR-PLANT", 14_250],
    ["6400", "DAL-DC", 31_000],
    ["6500", "ADMIN", 9_800],
  ];
  const season = [0.9, 0.9, 1, 1, 1.05, 1.1, 1.05, 1, 1, 1.05, 1.05, 0.9];
  cells.set(
    scenarioId,
    accounts.flatMap(([account, costCenter, monthly]) =>
      season.map((s, i) => ({ account, costCenter, period: `${year}-${String(i + 1).padStart(2, "0")}`, amount: dec(Math.round(monthly * s * factor) * 10_000) })),
    ),
  );
}
seed(BASELINE, YEAR, 1);
seed(OPTIMISTIC, YEAR, 1.08);
seed(uid(K_SCENARIO, 3), YEAR - 1, 0.95);

const scenarioTotal = (id: string): string => dec((cells.get(id) ?? []).reduce((s, c) => s + units(c.amount), 0));

function view(p: StoredPlan): StoredPlan {
  return { ...p, scenarios: p.scenarios.map((s) => ({ ...s, total: scenarioTotal(s.id) })) };
}

const notFound = (what: string) => reply.problem(404, "COM-0404", `${what} not found`);

function plan(id: string): StoredPlan | undefined {
  return plans.get(id);
}

function ifMatch(headers: Headers): number | null {
  const raw = headers.get("If-Match");
  return raw === null ? null : Number(raw.replace(/"/g, ""));
}

route("GET", `${BASE}/control-rules`, ({ query }) => ({ items: rules.all().filter((r) => !query.get("legalEntityId") || r.legalEntityId === query.get("legalEntityId")) }));
route("GET", `${BASE}/control-rules/{id}`, ({ params }) => rules.get(params.id) ?? notFound("budget control rule"));
// Available funds per control combination, the figures the cost control workspace reads.
function balance(n: number, dimension: string, span: string, budget: number, actuals: number, enc: number, pre: number, res: number): Balance {
  return {
    id: uid(372, n),
    ruleVersionId: uid(K_RULE, 1),
    controlDimensionKey: dimension,
    timeSpanKey: span,
    budgetTotal: dec(budget * 10_000),
    actuals: dec(actuals * 10_000),
    encumbrances: dec(enc * 10_000),
    preEncumbrances: dec(pre * 10_000),
    reservations: dec(res * 10_000),
    available: dec((budget - actuals - enc - pre - res) * 10_000),
    version: 1,
  };
}

const balances: Balance[] = [
  balance(1, "6100|CR-PLANT", "FY2026-Q3", 365_000, 351_200, 0, 0, 0),
  balance(2, "6100|CR-PLANT", "FY2026-Q4", 372_000, 148_600, 0, 0, 0),
  balance(3, "6200|CR-PLANT", "FY2026-Q4", 64_500, 31_900, 18_400, 0, 0),
  balance(4, "6300|CR-PLANT", "FY2026-Q4", 42_750, 29_400, 9_800, 6_200, 0),
  balance(5, "6400|DAL-DC", "FY2026-Q3", 93_000, 96_450, 0, 0, 0),
  balance(6, "6400|DAL-DC", "FY2026-Q4", 93_000, 38_200, 41_000, 0, 12_000),
  balance(7, "6500|ADMIN", "FY2026-Q4", 29_400, 11_100, 2_300, 0, 0),
];

// Cost-ledger actuals per cost center (cost accounting, /api/v1/controlling): a few cents off the budget register's actuals, as a
// month's journal mirroring that is not yet complete would leave them, so the comparison has something to show.
const ledgerActual = (code: string, period: string, total: number): ActualRow => ({
  costObjectId: uid(373, code.length * 7 + period.length), kind: "CostCenter", code, name: code, period, currency: "USD",
  primary: dec(total * 10_000), allocated: "0", total: dec(total * 10_000),
});
route("GET", "/api/v1/controlling/cost-actuals", ({ query }) => {
  const from = query.get("from") ?? "";
  const to = query.get("to") ?? "";
  const rows = [
    ledgerActual("CR-PLANT", `${YEAR - 1}-09`, 351_200 + 31_900 + 29_400),
    ledgerActual("CR-PLANT", `${YEAR - 1}-11`, 148_600),
    ledgerActual("DAL-DC", `${YEAR - 1}-08`, 96_450 + 1_250),
    ledgerActual("DAL-DC", `${YEAR - 1}-11`, 38_200),
    ledgerActual("ADMIN", `${YEAR - 1}-11`, 11_100),
  ];
  return { items: rows.filter((r) => r.period >= from && r.period <= to) };
});

route("GET", `${BASE}/available`, ({ query }) => ({ items: query.get("rule") === uid(K_RULE, 1) ? balances : [] }));

// The cost centers, and the pump assembly value stream the lean flows belong to (handlers/mfg/lean.ts).
const operatingUnits = [
  ...orgUnits.map((u) => ({ ...u, type: "cost_center", status: "active", version: 1, defaultLegalEntityId: LEGAL_ENTITY.id })),
  { id: uid(271, 1), code: "VS-PUMP", name: "Pump assembly value stream", type: "value_stream", status: "active", version: 1, defaultLegalEntityId: LEGAL_ENTITY.id },
];
route("GET", "/api/v1/org/operating-units", () => ({ items: operatingUnits }));
route("GET", "/api/v1/org/operating-units/{id}", ({ params }) => operatingUnits.find((u) => u.id === params.id) ?? reply.problem(404, "ORG-0404", "No operating unit with that id"));

route("GET", `${BASE}/plans`, ({ query }) => ({
  items: plans
    .all()
    .filter((p) => !query.get("legalEntityId") || p.legalEntityId === query.get("legalEntityId"))
    .sort((a, b) => b.fiscalYear - a.fiscalYear)
    .map(({ scenarios: _s, stageOwners: _o, ...rest }) => rest),
}));

route("POST", `${BASE}/plans`, ({ body }) => {
  const input = body as { legalEntityId: string; ruleVersionId: string; name: string; fiscalYear: number };
  if (plans.all().some((p) => p.legalEntityId === input.legalEntityId && p.fiscalYear === input.fiscalYear && p.name === input.name)) {
    return reply.problem(409, "BUD-0041", "a plan with this name already exists for the fiscal year");
  }
  const scenarioId = newId();
  cells.set(scenarioId, []);
  const created = plans.insert({
    id: newId(),
    ...input,
    currency: "USD",
    status: "Draft",
    submission: 0,
    version: 1,
    createdBy: "dev",
    createdAt: at(0),
    scenarios: [{ id: scenarioId, name: "Baseline", kind: "Baseline", version: 1, total: "0" }],
    stageOwners: [],
  });
  return reply.created(view(created));
});

route("GET", `${BASE}/plans/{id}`, ({ params }) => {
  const p = plan(params.id);
  return p ? view(p) : notFound("budget plan");
});

route("PUT", `${BASE}/plans/{id}/stage-owners`, ({ params, body }) => {
  const p = plan(params.id);
  if (!p) return notFound("budget plan");
  const owners = (body as { owners: StageOwner[] }).owners;
  const review = new Set(owners.filter((o) => o.stage === "Review").map((o) => o.owner));
  if (owners.some((o) => o.stage === "Approve" && review.has(o.owner))) return reply.problem(422, "BUD-0044", "one person cannot own both stages");
  return view(plans.update(p.id, { stageOwners: owners.map((o) => ({ stage: o.stage, orgUnitId: o.orgUnitId, owner: o.owner })) })!);
});

route("POST", `${BASE}/plans/{id}/scenarios`, ({ params, body }) => {
  const p = plan(params.id);
  if (!p) return notFound("budget plan");
  const input = body as { name: string; kind: PlanScenario["kind"]; copyFromScenarioId?: string; adjustmentPct?: string };
  if (p.scenarios.some((s) => s.name === input.name)) return reply.problem(409, "BUD-0049", "a scenario with this name already exists");
  if (p.scenarios.length >= 10) return reply.problem(409, "BUD-0051", "a plan holds at most 10 scenarios");
  const id = newId();
  const factor = 1 + Number(input.adjustmentPct ?? 0) / 100;
  cells.set(id, (cells.get(input.copyFromScenarioId ?? "") ?? []).map((c) => ({ ...c, amount: dec(Math.round(units(c.amount) * factor)) })));
  const scenario: PlanScenario = { id, name: input.name, kind: input.kind, copiedFromId: input.copyFromScenarioId, version: 1, total: "0" };
  plans.update(p.id, { scenarios: [...p.scenarios, scenario] });
  return reply.created({ ...scenario, total: scenarioTotal(id) });
});

route("GET", `${BASE}/plans/{id}/scenarios:compare`, ({ params, query }) => {
  const p = plan(params.id);
  const base = p?.scenarios.find((s) => s.id === query.get("base"));
  const other = p?.scenarios.find((s) => s.id === query.get("other"));
  if (!p || !base || !other) return reply.problem(404, "BUD-0048", "scenario not found in this plan");
  const groupBy = (query.get("groupBy") ?? "account") as Comparison["groupBy"];
  const keyOf = (c: PlanCell) => (groupBy === "costCenter" ? c.costCenter : groupBy === "period" ? c.period : c.account);
  const sums = new Map<string, [number, number]>();
  for (const c of cells.get(base.id) ?? []) sums.set(keyOf(c), [(sums.get(keyOf(c)) ?? [0, 0])[0] + units(c.amount), (sums.get(keyOf(c)) ?? [0, 0])[1]]);
  for (const c of cells.get(other.id) ?? []) sums.set(keyOf(c), [(sums.get(keyOf(c)) ?? [0, 0])[0], (sums.get(keyOf(c)) ?? [0, 0])[1] + units(c.amount)]);
  const rows = [...sums.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, [b, o]]) => ({ key, base: dec(b), other: dec(o), delta: dec(o - b), ...(b !== 0 ? { deltaPct: (((o - b) / b) * 100).toFixed(2) } : {}) }));
  const result: Comparison = { groupBy, base: { ...base, total: scenarioTotal(base.id) }, other: { ...other, total: scenarioTotal(other.id) }, rows, delta: dec(units(scenarioTotal(other.id)) - units(scenarioTotal(base.id))) };
  return result;
});

route("GET", `${BASE}/plans/{id}/scenarios/{sid}/grid`, ({ params, query }) => {
  const p = plan(params.id);
  const sc = p?.scenarios.find((s) => s.id === params.sid);
  if (!p || !sc) return reply.problem(404, "BUD-0048", "scenario not found in this plan");
  const matching = (cells.get(sc.id) ?? []).filter((c) => (!query.get("account") || c.account === query.get("account")) && (!query.get("costCenter") || c.costCenter === query.get("costCenter")) && (!query.get("period") || c.period === query.get("period")));
  const total = dec(matching.reduce((s, c) => s + units(c.amount), 0));
  return reply.versioned({ scenario: { ...sc, total: scenarioTotal(sc.id) }, cells: matching, total, truncated: false }, sc.version);
});

function write(planId: string, scenarioId: string, headers: Headers, apply: (existing: PlanCell[]) => PlanCell[] | ReturnType<typeof reply.problem>) {
  const p = plan(planId);
  const sc = p?.scenarios.find((s) => s.id === scenarioId);
  if (!p || !sc) return reply.problem(404, "BUD-0048", "scenario not found in this plan");
  if (p.status !== "Draft") return reply.problem(409, "BUD-0042", `the plan is ${p.status}; only a Draft plan can be changed`);
  const version = ifMatch(headers);
  if (version === null) return reply.problem(428, "COM-0428", "send the grid version in If-Match");
  if (version !== sc.version) return reply.problem(412, "BUD-0050", "the grid changed since you read it");
  const next = apply(cells.get(scenarioId) ?? []);
  if (!Array.isArray(next)) return next;
  cells.set(scenarioId, next);
  const bumped = sc.version + 1;
  plans.update(p.id, { scenarios: p.scenarios.map((s) => (s.id === scenarioId ? { ...s, version: bumped } : s)) });
  return reply.versioned({ scenarioId, version: bumped, cells: next.length, total: scenarioTotal(scenarioId) }, bumped);
}

const upsert = (existing: PlanCell[], incoming: PlanCell[]): PlanCell[] => {
  const map = new Map(existing.map((c) => [`${c.account}|${c.costCenter}|${c.period}`, c]));
  for (const c of incoming) map.set(`${c.account}|${c.costCenter}|${c.period}`, c);
  return [...map.values()];
};

route("PUT", `${BASE}/plans/{id}/scenarios/{sid}/grid`, ({ params, body, headers }) =>
  write(params.id, params.sid, headers, (existing) => {
    const incoming = (body as { cells: PlanCell[] }).cells;
    if (incoming.some((c) => units(c.amount) < 0)) return reply.problem(400, "COM-0001", "amounts cannot be negative");
    return upsert(existing, incoming);
  }),
);

route("POST", `${BASE}/plans/{id}/scenarios/{sid}:spread`, ({ params, body, headers }) => {
  const input = body as { account: string; total: string; method: string; periodWeights?: string[]; costCenters: { costCenter: string; weight: string }[] };
  return write(params.id, params.sid, headers, (existing) => {
    const weights = input.method === "custom" ? (input.periodWeights ?? []).map(Number) : Array(12).fill(1);
    const centerSum = input.costCenters.reduce((s, c) => s + Number(c.weight), 0);
    const weightSum = weights.reduce((s: number, w: number) => s + w, 0);
    if (centerSum <= 0) return reply.problem(422, "BUD-0060", "the cost center weights sum to zero");
    const total = units(input.total);
    const made: PlanCell[] = [];
    for (const c of input.costCenters) {
      const share = Math.round((total * Number(c.weight)) / centerSum);
      periods.forEach((period, i) => made.push({ account: input.account, costCenter: c.costCenter, period, amount: dec(Math.round((share * weights[i]) / weightSum)) }));
    }
    return upsert(existing, made);
  });
});

// The drivers plan generation reads (FR-38.14): the depreciation of the open fixed assets, the HR positions, project forecasts
// and the planning demand forecast. Fictional Cedar Ridge Pumps figures; the routes the browser reads (projects and their
// forecasts) are served below, the rest stay inside this handler as the budgeting service would read them server side.
const K_PROJECT_MODEL = 1901;
const demoProjects = [
  { id: "P-1001", legalEntityId: LEGAL_ENTITY.id, projectGroupId: "CAPEX", projectType: "INTERNAL", currency: "USD", contractValue: "0", stage: "IN_PROCESS", startDate: `${YEAR - 1}-09-01`, version: 3, createdAt: at(-120), updatedAt: at(-6) },
  { id: "P-2040", legalEntityId: LEGAL_ENTITY.id, projectGroupId: "CUST", projectType: "FIXED_PRICE", currency: "USD", contractValue: "480000", stage: "IN_PROCESS", startDate: `${YEAR - 1}-11-15`, version: 5, createdAt: at(-60), updatedAt: at(-2) },
];
const demoForecasts = [
  { id: uid(K_PROJECT_MODEL, 1), projectId: "P-1001", name: "Test bay rebuild", currency: "USD", version: 2, lineCount: 6, months: [3, 4, 5, 6, 7, 8], cost: 18_500, sales: 0 },
  { id: uid(K_PROJECT_MODEL, 2), projectId: "P-2040", name: "Municipal pump station", currency: "USD", version: 4, lineCount: 8, months: [2, 3, 4, 5, 6, 7, 8, 9], cost: 41_000, sales: 60_000 },
];
// Open positions: job, department (an operating unit), FTE and the month the position opens.
const demoPositions = [
  { jobCode: "ASSEMBLER", departmentId: uid(K_UNIT, 1), fte: 6, from: 1 },
  { jobCode: "TECHNICIAN", departmentId: uid(K_UNIT, 1), fte: 2, from: 4 },
  { jobCode: "PICKER", departmentId: uid(K_UNIT, 2), fte: 4, from: 1 },
  { jobCode: "CONTROLLER", departmentId: uid(K_UNIT, 3), fte: 1, from: 1 },
];
const monthlyDepreciation = [4_250, 4_250, 4_250, 4_250, 4_610, 4_610, 4_610, 4_610, 4_610, 4_610, 4_980, 4_980];
const demandUnits = [38, 36, 44, 52, 60, 66, 64, 58, 50, 46, 40, 34];

route("GET", "/api/v1/projects", ({ query }) => ({ items: demoProjects.filter((p) => !query.get("stage") || p.stage === query.get("stage")) }));
route("GET", "/api/v1/projects/{id}", ({ params }) => demoProjects.find((p) => p.id === params.id) ?? reply.problem(404, "PRJ-0404", "No project with that id"));
route("GET", "/api/v1/projects/{id}/forecasts", ({ params }) => {
  if (!demoProjects.some((p) => p.id === params.id)) return reply.problem(404, "PRJ-0404", "No project with that id");
  return {
    items: demoForecasts
      .filter((f) => f.projectId === params.id)
      .map(({ months, cost, sales, ...f }) => ({ ...f, totalCost: dec(months.length * cost * 10_000), totalSales: dec(months.length * sales * 10_000), createdBy: "ana.ortiz", createdAt: at(-30) })),
  };
});

interface GenerateBody {
  source: "prior-plan" | "gl-actuals" | "fixed-assets" | "positions" | "projects" | "demand";
  adjustmentPct?: string;
  accounts?: string[];
  costCenters?: { costCenter: string; weight: string }[];
  account?: string;
  book?: string;
  rates?: { jobCode: string; monthlyCost: string }[];
  departments?: { departmentId: string; costCenter: string }[];
  projects?: { projectId: string; modelId: string; costCenter: string; amount?: "cost" | "sales" }[];
  modelCode?: string;
  unitPrices?: { itemId: string; price: string }[];
}

/** The cells of a driver source: amounts per month (in units) on the request's account, by cost center. */
function driverCells(input: GenerateBody, factor: number): PlanCell[] | ReturnType<typeof reply.problem> {
  const account = input.account ?? "";
  const byCenter = new Map<string, number[]>();
  const add = (center: string, month: number, amount: number) => {
    const row = byCenter.get(center) ?? Array(12).fill(0);
    row[month] += amount;
    byCenter.set(center, row);
  };
  const share = (monthly: number[]) => {
    const centers = input.costCenters ?? [];
    const sum = centers.reduce((s, c) => s + Number(c.weight), 0);
    if (sum <= 0) return reply.problem(422, "BUD-0060", "the cost center weights sum to zero");
    for (const c of centers) monthly.forEach((m, i) => add(c.costCenter, i, (m * Number(c.weight)) / sum));
    return null;
  };
  switch (input.source) {
    case "fixed-assets": {
      const refused = share(monthlyDepreciation.map((m) => m * 10_000));
      if (refused) return refused;
      break;
    }
    case "demand": {
      if (input.modelCode !== `FC-${YEAR}`) return reply.problem(422, "BUD-0064", `planning has no forecast ${input.modelCode ?? ""}; the demo holds FC-${YEAR}`);
      const unitPrice = (input.unitPrices ?? []).reduce((s, p) => s + units(p.price), 0);
      const refused = share(demandUnits.map((q) => q * unitPrice));
      if (refused) return refused;
      break;
    }
    case "positions": {
      const rates = new Map((input.rates ?? []).map((r) => [r.jobCode, units(r.monthlyCost)]));
      const centers = new Map((input.departments ?? []).map((d) => [d.departmentId, d.costCenter]));
      for (const p of demoPositions) {
        const rate = rates.get(p.jobCode);
        if (rate === undefined) return reply.problem(422, "BUD-0064", `no monthly cost is given for job ${p.jobCode}`);
        const center = centers.get(p.departmentId);
        if (center === undefined) {
          const unit = orgUnits.find((u) => u.id === p.departmentId);
          return reply.problem(422, "BUD-0065", `the HR department ${unit?.name ?? p.departmentId} has no cost center in the request`);
        }
        for (let m = p.from - 1; m < 12; m++) add(center, m, p.fte * rate);
      }
      break;
    }
    case "projects":
      for (const row of input.projects ?? []) {
        const model = demoForecasts.find((f) => f.id === row.modelId && f.projectId === row.projectId);
        if (!model) return reply.problem(422, "BUD-0064", `project ${row.projectId} has no such forecast`);
        for (const m of model.months) add(row.costCenter, m - 1, (row.amount === "sales" ? model.sales : model.cost) * 10_000);
      }
      break;
    default:
      return [];
  }
  const made: PlanCell[] = [];
  for (const [costCenter, months] of byCenter) {
    months.forEach((amount, i) => {
      if (amount !== 0) made.push({ account, costCenter, period: periods[i], amount: dec(Math.round(amount * factor)) });
    });
  }
  if (made.length === 0) return reply.problem(422, "BUD-0061", "the source holds no amounts for the plan year");
  return made;
}

route("POST", `${BASE}/plans/{id}/scenarios/{sid}:generate`, ({ params, body, headers }) => {
  const input = body as GenerateBody;
  return write(params.id, params.sid, headers, (existing) => {
    const factor = 1 + Number(input.adjustmentPct ?? 0) / 100;
    if (input.source !== "prior-plan" && input.source !== "gl-actuals") {
      const made = driverCells(input, factor);
      return Array.isArray(made) ? upsert(existing, made) : made;
    }
    const wanted = new Set(input.accounts ?? []);
    const lastYear = (cells.get(uid(K_SCENARIO, 3)) ?? []).filter((c) => wanted.size === 0 || wanted.has(c.account));
    if (lastYear.length === 0) return reply.problem(422, "BUD-0061", "no approved plan of the previous fiscal year holds amounts for these accounts");
    if (input.source === "prior-plan") return upsert(existing, lastYear.map((c) => ({ ...c, period: `${YEAR}-${c.period.slice(5)}`, amount: dec(Math.round(units(c.amount) * factor)) })));
    const centers = input.costCenters ?? [];
    const sum = centers.reduce((s, c) => s + Number(c.weight), 0);
    if (sum <= 0) return reply.problem(422, "BUD-0060", "the cost center weights sum to zero");
    const byMonth = new Map<string, number>();
    for (const c of lastYear) byMonth.set(`${c.account}|${c.period.slice(5)}`, (byMonth.get(`${c.account}|${c.period.slice(5)}`) ?? 0) + units(c.amount));
    return upsert(existing, [...byMonth.entries()].flatMap(([k, total]) => centers.map((c) => ({ account: k.split("|")[0], costCenter: c.costCenter, period: `${YEAR}-${k.split("|")[1]}`, amount: dec(Math.round((total * factor * Number(c.weight)) / sum)) }))));
  });
});

// The workbook round trip. The demo cannot build or read a real .xlsx, so the export is a small placeholder file and an import only
// says the workbook is valid (a dry run) or leaves the grid as it is.
route("GET", `${BASE}/plans/{id}/scenarios/{sid}:export`, ({ params }) => {
  const p = plan(params.id);
  const sc = p?.scenarios.find((s) => s.id === params.sid);
  if (!p || !sc) return notFound("budget plan");
  return new DemoResponse(200, new Uint8Array([0x50, 0x4b, 0x05, 0x06, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]), {
    "Content-Type": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    "Content-Disposition": `attachment; filename="budget-plan-${p.fiscalYear}.xlsx"`,
    ETag: `"${sc.version}"`,
  });
});

route("POST", `${BASE}/plans/{id}/scenarios/{sid}:import`, ({ params, query, headers }) => {
  if (query.get("dryRun") === "true") {
    const rows = (cells.get(params.sid) ?? []).filter((c) => c.period === periods[0]).length;
    return { valid: true, dryRun: true, rows, cells: rows * 12, total: scenarioTotal(params.sid) };
  }
  return write(params.id, params.sid, headers, (existing) => existing);
});

route("POST", `${BASE}/plans/{id}:submit`, ({ params }) => {
  const p = plan(params.id);
  if (!p) return notFound("budget plan");
  if (p.status !== "Draft") return reply.problem(409, "BUD-0042", `the plan is ${p.status}; only a Draft plan can be submitted`);
  if (!p.stageOwners.some((o) => o.stage === "Review") || !p.stageOwners.some((o) => o.stage === "Approve")) return reply.problem(422, "BUD-0045", "set who reviews and who approves before submitting");
  return view(plans.update(p.id, { status: "Submitted", submission: p.submission + 1, submittedBy: "dev" })!);
});
