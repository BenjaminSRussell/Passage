// Scheduling service (/api/v1/scheduling) for the demo: the resource Gantt and capacity load. Resources and
// their load come from company.ts (BAL-01 over capacity, MCH-01 near full); the bars are the running and
// released production orders of the production demo.
import type { Firm, GanttBar, LoadItem, LoadJob, Resource, SchedulingParameters } from "../../../modules/scheduling/api/client";
import { reply, route } from "../../backend";
import { RESOURCES, resourceByCode } from "../../company";
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
