// Scheduling service (/api/v1/scheduling) for the demo: the resource Gantt and capacity load. Resources and
// their load come from company.ts (BAL-01 over capacity, MCH-01 near full); the bars are the running and
// released production orders of the production demo.
import type { Firm, GanttBar, LoadItem, LoadJob, MoveRequest, MoveResponse, Resource, Run, RunKpis, SchedulingParameters } from "../../../modules/scheduling/api/client";
import { reply, route } from "../../backend";
import { RESOURCES, resourceByCode } from "../../company";
import { orders as productionOrders } from "./production";
import { at, day, table, uid } from "../../store";

const BASE = "/api/v1/scheduling";
const KIND_JOB = 220;
const MIN = 60_000;
const t0 = Date.parse(`${day(0)}T00:00:00Z`);
const iso = (ms: number) => new Date(ms).toISOString().replace(".000Z", "Z");
const hour = (h: number) => iso(t0 + h * 60 * MIN);

const resources: Resource[] = RESOURCES.map((r) => {
  const longDay = r.hoursPerDay >= 16;
  const shift = (weekday: number) => ({ weekday, startMinute: 6 * 60, endMinute: longDay ? 22 * 60 : 14 * 60 });
  return {
    id: r.id,
    code: r.code,
    name: r.name,
    finite: true,
    timeZone: "UTC",
    shifts: [1, 2, 3, 4, 5].map(shift),
    version: 1,
    createdAt: at(-120),
    engineeringResourceId: r.id,
    efficiencyPct: r.code === "BAL-01" ? 92 : 100,
  };
});
const idOf = (code: string) => resourceByCode(code).id;

interface JobSpec {
  n: number;
  order: string;
  op: number;
  res: string;
  /** Segments as [start hour, end hour] from today 00:00 UTC. */
  segs: [number, number][];
  late?: number;
  firmed?: boolean;
}
const JOBS: JobSpec[] = [
  { n: 1, order: "MO-24618", op: 20, res: "BAL-01", segs: [[6, 14], [30, 38]], late: 4320, firmed: true },
  { n: 2, order: "MO-24624", op: 10, res: "MCH-01", segs: [[6, 22], [30, 33.5]] },
  { n: 3, order: "MO-24627", op: 10, res: "ASM-01", segs: [[6, 18.25]], firmed: true },
  { n: 4, order: "MO-24605", op: 10, res: "ASM-01", segs: [[18.25, 22], [30, 36.6]], late: 1440 },
  { n: 5, order: "MO-24615", op: 10, res: "ASM-01", segs: [[36.6, 46], [54, 74.2]] },
  { n: 6, order: "MO-24611", op: 20, res: "TST-01", segs: [[6, 20.65]], firmed: true },
  { n: 7, order: "MO-24605", op: 20, res: "TST-01", segs: [[30, 34.9]], late: 1440 },
  { n: 8, order: "MO-24611", op: 30, res: "PNT-01", segs: [[30, 38], [54, 56.1]] },
  { n: 9, order: "MO-24608", op: 40, res: "PCK-01", segs: [[8, 10.5]] },
  { n: 10, order: "MO-24611", op: 40, res: "PCK-01", segs: [[56, 62]] },
  { n: 11, order: "MO-24621", op: 10, res: "ASM-01", segs: [[78, 94], [102, 120.5]] },
  { n: 12, order: "MO-24618", op: 10, res: "MCH-01", segs: [[-30, -22]], firmed: true },
];

const bars: GanttBar[] = JOBS.flatMap((j) =>
  j.segs.map(([s, e], i) => ({
    jobId: uid(KIND_JOB, j.n),
    orderId: j.order,
    operNumber: j.op,
    resourceId: idOf(j.res),
    segment: i + 1,
    start: hour(s),
    end: hour(e),
    lateMinutes: j.late ?? 0,
    firmed: Boolean(j.firmed),
    setupGroup: null,
    changeoverMinutes: 0,
    changeoverCost: null,
    changeoverCurrency: null,
    cleaningSetId: null,
    secondaryResourceIds: [],
    outsideCapacity: false,
    placementVersion: `pv-${j.order}-1`,
  })),
);
const firmedJobs = table<{ id: string; firmed: boolean }>(JOBS.map((j) => ({ id: uid(KIND_JOB, j.n), firmed: Boolean(j.firmed) })));

route("GET", `${BASE}/resources`, () => ({ items: resources }));
route("GET", `${BASE}/resources/{id}/capacity`, () => ({ items: [] }));
route("GET", `${BASE}/resources/{id}`, (req) => resources.find((r) => r.id === req.params.id) ?? reply.problem(404, "SCHED-0404", "Resource not found."));
route("GET", `${BASE}/parameters`, (): SchedulingParameters => ({ defaultMode: "jobs", defaultDirection: "forward", defaultFinite: true, dragSnapMinutes: 15, propagationOrders: 500, propagationLevels: 5, version: 1, updatedAt: null }));

route("GET", `${BASE}/gantt`, (req) => {
  const from = Date.parse(req.query.get("from") ?? "") || t0;
  const to = Date.parse(req.query.get("to") ?? "") || t0 + 2 * 86_400_000;
  const only = (req.query.get("resourceIds") ?? "").split(",").filter(Boolean);
  const items = bars
    .filter((b) => Date.parse(b.end) > from && Date.parse(b.start) < to && (only.length === 0 || only.includes(b.resourceId)))
    .map((b) => ({ ...b, firmed: firmedJobs.get(b.jobId)?.firmed ?? b.firmed }));
  return { items };
});

route("POST", `${BASE}/jobs/{id}:firm`, (req) => {
  const bar = bars.find((b) => b.jobId === req.params.id);
  if (!bar) return reply.problem(404, "SCHED-0404", "Job not found.");
  const firmed = Boolean((req.body as { firmed?: boolean }).firmed);
  firmedJobs.update(bar.jobId, { firmed });
  const out: Firm = { jobId: bar.jobId, orderId: bar.orderId, resourceId: bar.resourceId, start: bar.start, end: bar.end, firmed };
  return reply.ok(out);
});

// Moves: the Gantt previews a move with dryRun and saves it without. A move that lands on another job of the same
// resource is refused with the job it collides with; otherwise every segment of the job shifts by the same amount.
const jobBars = (jobId: string) => bars.filter((b) => b.jobId === jobId).sort((a, b) => a.segment - b.segment);
const resourceName = (id: string) => RESOURCES.find((r) => r.id === id)?.name ?? "the resource";

function planMove(jobId: string, req: MoveRequest): MoveResponse | { notFound: true } {
  const segs = jobBars(jobId);
  if (segs.length === 0) return { notFound: true };
  const delta = Date.parse(req.start) - Date.parse(segs[0].start);
  const resourceId = req.resourceId ?? segs[0].resourceId;
  const moved = segs.map((b) => ({ start: Date.parse(b.start) + delta, end: Date.parse(b.end) + delta }));
  const violations: MoveResponse["violations"] = [];
  for (const other of bars) {
    if (other.jobId === jobId || other.resourceId !== resourceId) continue;
    const clash = moved.some((m) => Date.parse(other.start) < m.end && Date.parse(other.end) > m.start);
    if (clash && !violations.some((v) => v.jobId === other.jobId)) {
      violations.push({ code: "SCHED-0005", detail: `Overlaps order ${other.orderId}, operation ${other.operNumber} on ${resourceName(resourceId)}.`, jobId: other.jobId });
    }
  }
  const lastEnd = moved[moved.length - 1].end;
  const order = productionOrders.get(segs[0].orderId);
  const due = order?.deliveryDate ? Date.parse(`${order.deliveryDate}T23:59:59Z`) : null;
  const late = due !== null && lastEnd > due ? Math.round((lastEnd - due) / MIN) : 0;
  return {
    dryRun: req.dryRun,
    feasible: violations.length === 0,
    violations,
    changes: [
      {
        operationId: jobId,
        orderId: segs[0].orderId,
        resourceId,
        oldStart: segs[0].start,
        oldEnd: segs[segs.length - 1].end,
        newStart: iso(moved[0].start),
        newEnd: iso(lastEnd),
        lateMinutes: late,
      },
    ],
    orderId: segs[0].orderId,
    orderStart: iso(moved[0].start),
    orderEnd: iso(lastEnd),
    dependents: [],
  };
}

route("POST", `${BASE}/jobs/{id}:move`, (req) => {
  const body = req.body as MoveRequest;
  if (!body?.start) return reply.problem(422, "SCHED-0001", "Enter a start.", { start: "Enter a start." });
  const plan = planMove(req.params.id, body);
  if ("notFound" in plan) return reply.problem(404, "SCHED-0404", "Job not found.");
  if (body.dryRun || !plan.feasible) return plan;
  const segs = jobBars(req.params.id);
  const delta = Date.parse(body.start) - Date.parse(segs[0].start);
  for (const b of segs) {
    b.start = iso(Date.parse(b.start) + delta);
    b.end = iso(Date.parse(b.end) + delta);
    b.resourceId = body.resourceId ?? b.resourceId;
    b.lateMinutes = plan.changes[0].lateMinutes;
  }
  runs.unshift(newRun("move", 1, plan.changes[0].lateMinutes, { makespanMinutes: 0 }));
  return { ...plan, run: runs[0] };
});

// Schedule runs: a short history, and a new run can be started from the form. A run keeps the plan as it is
// (the demo's bars are the plan); it only records itself, so the Gantt looks the same afterwards.
let runSeq = 420;
const jobCount = () => new Set(bars.map((b) => b.jobId)).size;
function newRun(scope: Run["scope"], operations: number, lateness: number, extra: Partial<Run> = {}): Run {
  runSeq += 1;
  return {
    id: `RUN-${String(runSeq).padStart(4, "0")}`,
    scope,
    mode: "jobs",
    direction: "forward",
    finite: true,
    algorithm: "heuristic",
    status: "complete",
    operationsCount: operations,
    makespanMinutes: 8460,
    totalLatenessMinutes: lateness,
    solverStatus: null,
    messages: [],
    warnings: [],
    createdAt: at(0),
    snapshot: true,
    ...extra,
  };
}
const past = (id: string, daysAgo: number, rest: Partial<Run>): Run => ({ ...newRun("site", jobCount(), 5760), ...rest, id, createdAt: at(-daysAgo) });
const runs: Run[] = [
  past("RUN-0420", 0.3, {}),
  past("RUN-0419", 1.3, { operationsCount: 11, makespanMinutes: 8100, totalLatenessMinutes: 7200 }),
  past("RUN-0418", 2.1, { scope: "move", operationsCount: 1, makespanMinutes: 0, totalLatenessMinutes: 4320 }),
  past("RUN-0417", 3.3, { direction: "backward", operationsCount: 11, makespanMinutes: 9240, totalLatenessMinutes: 2880 }),
  past("RUN-0416", 5.2, { finite: false, operationsCount: 10, makespanMinutes: 6120, totalLatenessMinutes: 0, snapshot: false }),
];
runSeq = 420;

route("GET", `${BASE}/runs`, () => ({ items: runs }));
route("POST", `${BASE}/runs`, (req) => {
  const body = req.body as { scope?: Run["scope"]; mode?: string; direction?: string; finite?: boolean };
  const run = newRun(body.scope ?? "site", jobCount(), 5760, { mode: body.mode ?? "jobs", direction: body.direction ?? "forward", finite: body.finite !== false });
  runs.unshift(run);
  return reply.created(run);
});
route("GET", `${BASE}/runs/{id}/kpis`, (req) => {
  if (!runs.some((r) => r.id === req.params.id)) return reply.problem(404, "SCHED-0404", "Run not found.");
  const booked = (list: GanttBar[]) => Math.round(list.reduce((n, b) => n + (Date.parse(b.end) - Date.parse(b.start)) / MIN, 0));
  const late = bars.filter((b) => b.lateMinutes > 0);
  const kpis: RunKpis = {
    runId: req.params.id,
    placedJobs: jobCount(),
    orders: new Set(bars.map((b) => b.orderId)).size,
    lateJobs: new Set(late.map((b) => b.jobId)).size,
    lateOrders: new Set(late.map((b) => b.orderId)).size,
    maxLateMinutes: Math.max(0, ...bars.map((b) => b.lateMinutes)),
    changeoverMinutes: 0,
    bookedMinutes: booked(bars),
    spanStart: bars.map((b) => b.start).sort()[0] ?? null,
    spanEnd: bars.map((b) => b.end).sort().at(-1) ?? null,
    resources: RESOURCES.map((r) => {
      const mine = bars.filter((b) => b.resourceId === r.id);
      return {
        resourceId: r.id,
        resourceCode: r.code,
        jobs: new Set(mine.map((b) => b.jobId)).size,
        bookedMinutes: booked(mine),
        openMinutes: r.hoursPerDay * 60 * 5,
        utilizationPct: Math.round(r.load * 1000) / 10,
      };
    }).filter((r) => r.jobs > 0),
  };
  return kpis;
});
route("GET", `${BASE}/runs/{id}`, (req) => runs.find((r) => r.id === req.params.id) ?? reply.problem(404, "SCHED-0404", "Run not found."));

// Capacity load: week buckets start on Monday; the weekly curve comes from each resource's load in company.ts.
const WEEK_CURVE = [0.02, 0, -0.04, -0.1, -0.14, -0.16, -0.18, -0.2];
const DAY_WOBBLE = [0.04, -0.03, 0.06, -0.02, 0.01];

function mondayOf(date: string): number {
  const d = new Date(`${date}T00:00:00Z`);
  const back = (d.getUTCDay() + 6) % 7;
  return d.getTime() - back * 86_400_000;
}

route("GET", `${BASE}/capacity-load/jobs`, (req) => {
  const id = req.query.get("resourceId");
  const from = Date.parse(req.query.get("from") ?? "") || t0;
  const to = Date.parse(req.query.get("to") ?? "") || t0 + 7 * 86_400_000;
  const items: LoadJob[] = bars
    .filter((b) => b.resourceId === id && Date.parse(b.end) > from && Date.parse(b.start) < to)
    .map((b) => ({
      jobId: b.jobId,
      orderId: b.orderId,
      operNumber: b.operNumber,
      segment: b.segment,
      role: "primary",
      start: b.start,
      end: b.end,
      minutesInRange: Math.round((Math.min(Date.parse(b.end), to) - Math.max(Date.parse(b.start), from)) / MIN),
      finite: true,
      firmed: b.firmed,
      lateMinutes: b.lateMinutes,
    }));
  return { items, truncated: false };
});

route("GET", `${BASE}/capacity-load`, (req) => {
  const from = req.query.get("from") ?? day(0);
  const only = (req.query.get("resourceIds") ?? "").split(",").filter(Boolean);
  const list = RESOURCES.filter((r) => only.length === 0 || only.includes(r.id));
  const items: LoadItem[] = [];
  if (req.query.get("period") === "day") {
    const days = Number(req.query.get("days") ?? 14);
    const start = Date.parse(`${from}T00:00:00Z`);
    for (const r of list) {
      for (let i = 0; i < days; i++) {
        const t = start + i * 86_400_000;
        const weekday = new Date(t).getUTCDay();
        const open = weekday === 0 || weekday === 6 ? 0 : r.hoursPerDay * 60;
        const pct = open === 0 ? 0 : Math.max(0.2, r.load + WEEK_CURVE[Math.min(Math.floor(i / 7), 7)] + DAY_WOBBLE[i % 5]) * 100;
        items.push({ resourceId: r.id, period: "day", periodStart: iso(t).slice(0, 10), start: iso(t), end: iso(t + 86_400_000), openMinutes: open, bookedMinutes: Math.round((open * pct) / 100), loadPct: Math.round(pct * 10) / 10, bottleneck: false });
      }
    }
    return { items };
  }
  const weeks = Number(req.query.get("weeks") ?? 4);
  const monday = mondayOf(from);
  for (const r of list) {
    for (let w = 0; w < weeks; w++) {
      const t = monday + w * 7 * 86_400_000;
      const open = r.hoursPerDay * 60 * 5;
      const pct = Math.max(0.2, r.load + WEEK_CURVE[Math.min(w, 7)]) * 100;
      const next = Math.max(0.2, r.load + WEEK_CURVE[Math.min(w + 1, 7)]) * 100;
      const prev = w > 0 ? Math.max(0.2, r.load + WEEK_CURVE[Math.min(w - 1, 7)]) * 100 : 0;
      items.push({
        resourceId: r.id,
        period: "week",
        periodStart: iso(t).slice(0, 10),
        weekStart: iso(t).slice(0, 10),
        start: iso(t),
        end: iso(t + 7 * 86_400_000),
        openMinutes: open,
        bookedMinutes: Math.round((open * pct) / 100),
        loadPct: Math.round(pct * 10) / 10,
        bottleneck: pct > 100 && (next > 100 || prev > 100),
      });
    }
  }
  return { items };
});
