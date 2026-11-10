// Shop floor service (/api/v1/shopfloor) for the demo: machines, the andon board and OEE.
// Resource ids on these screens are the work centre codes (they are shown raw).
import type { Andon, Downtime, Machine, OeeBucket } from "../../../modules/shopfloor/api/client";
import { reply, route } from "../../backend";
import { PLANT } from "../../company";
import { at, table, uid } from "../../store";

const BASE = "/api/v1/shopfloor";
const KIND_ANDON = 230;
const KIND_DOWNTIME = 231;
const KIND_MACHINE = 232;

const MACHINES: { code: string; resource: string; cycle: number; seed: number }[] = [
  { code: "M-101", resource: "MCH-01", cycle: 90, seed: 3 },
  { code: "M-102", resource: "BAL-01", cycle: 60, seed: 7 },
  { code: "M-103", resource: "ASM-01", cycle: 120, seed: 11 },
  { code: "M-104", resource: "TST-01", cycle: 100, seed: 5 },
  { code: "M-105", resource: "PNT-01", cycle: 110, seed: 13 },
];

const machines: Machine[] = MACHINES.map((m, i) => ({
  id: uid(KIND_MACHINE, i + 1),
  machineId: m.code,
  resourceId: m.resource,
  siteId: PLANT.code,
  idealCycleSeconds: String(m.cycle),
  idleThresholdSeconds: 300,
  lastSeq: 18000 + i * 731,
  createdAt: at(-200),
}));

const downtimes: Downtime[] = [
  { id: uid(KIND_DOWNTIME, 1), machineId: "M-102", resourceId: "BAL-01", startedAt: at(-0.06), endedAt: null, reasonCode: null, category: "unplanned", cause: "down", andonRaised: true },
  { id: uid(KIND_DOWNTIME, 2), machineId: "M-105", resourceId: "PNT-01", startedAt: at(-0.04), endedAt: null, reasonCode: null, category: "unplanned", cause: "idle", andonRaised: true },
];

const andon = table<Andon>([
  { id: uid(KIND_ANDON, 1), resourceId: "BAL-01", siteId: PLANT.code, kind: "downtime", severity: "error", downtimeId: downtimes[0].id, jobId: null, detail: "Balancer drive fault E-214 while balancing MO-24618", startedAt: at(-0.06) },
  { id: uid(KIND_ANDON, 2), resourceId: "MCH-01", siteId: PLANT.code, kind: "scrap-rate", severity: "warning", downtimeId: null, jobId: null, detail: "Scrap 6.8 % over the last hour on SA-VOL-04 (limit 5 %)", startedAt: at(-0.15) },
  { id: uid(KIND_ANDON, 3), resourceId: "TST-01", siteId: PLANT.code, kind: "quality-fail", severity: "error", downtimeId: null, jobId: uid(233, 1), detail: "Hydrostatic test failed on MO-24611: seal weep at 450 psi, unit 24611-014", startedAt: at(-0.3) },
  { id: uid(KIND_ANDON, 4), resourceId: "PNT-01", siteId: PLANT.code, kind: "downtime", severity: "warning", downtimeId: downtimes[1].id, jobId: null, detail: "Booth idle for 58 min: waiting for CP-200 housings", startedAt: at(-0.04) },
]);

route("GET", `${BASE}/machines`, () => ({ items: machines }));
route("GET", `${BASE}/downtimes`, (req) => {
  const open = req.query.get("open");
  let rows = downtimes;
  if (open === "true") rows = rows.filter((d) => d.endedAt === null);
  if (open === "false") rows = rows.filter((d) => d.endedAt !== null || d.reasonCode === null);
  return { items: rows };
});
route("GET", `${BASE}/andon`, () => ({ items: andon.all().sort((a, b) => b.startedAt.localeCompare(a.startedAt)) }));
route("POST", `${BASE}/andon/{id}:clear`, (req) => {
  const row = andon.get(req.params.id);
  if (!row) return reply.problem(404, "SF-0162", "The alert is unknown or already cleared.");
  andon.remove(row.id);
  return reply.ok(row);
});

/** A repeatable pseudo-random number in [0, 1) so the OEE chart is stable between reloads. */
function noise(a: number, b: number): number {
  const x = Math.sin(a * 127.1 + b * 311.7) * 43758.5453;
  return x - Math.floor(x);
}

route("GET", `${BASE}/oee`, (req) => {
  const ids = (req.query.get("resourceIds") ?? "").split(",").filter(Boolean);
  const from = Date.parse(req.query.get("from") ?? "") || Date.parse(at(0));
  const size = { "5m": 5, "15m": 15, "60m": 60 }[(req.query.get("bucket") ?? "15m") as "5m" | "15m" | "60m"] ?? 15;
  const items: OeeBucket[] = [];
  // The first shift, 06:00 to 09:00 local, as the machines report it.
  const shiftStart = from + 6 * 3_600_000;
  const buckets = Math.floor((3 * 60) / size);
  for (const m of MACHINES.filter((x) => ids.includes(x.resource))) {
    for (let i = 0; i < buckets; i++) {
      const start = shiftStart + i * size * 60_000;
      const n = noise(m.seed, i);
      // BAL-01 is stopped for a stretch mid-shift; the others run with small stops.
      const stopped = m.resource === "BAL-01" && i * size >= 90 && i * size < 150;
      const down = stopped ? size : n < 0.12 ? size * (0.2 + n * 2) : 0;
      const run = size - down;
      const availability = run / size;
      const performance = Math.min(1, 0.86 + noise(i, m.seed) * 0.13);
      const quality = m.resource === "MCH-01" ? 0.93 + noise(m.seed, i + 9) * 0.05 : 0.975 + noise(m.seed, i + 4) * 0.025;
      const total = Math.floor(((run * 60) / m.cycle) * performance * 1.0);
      const good = Math.round(total * quality);
      items.push({
        resourceId: m.resource,
        bucketStart: new Date(start).toISOString(),
        plannedMinutes: size.toFixed(3),
        runMinutes: run.toFixed(3),
        downMinutes: down.toFixed(3),
        totalCount: total,
        goodCount: good,
        availability: availability.toFixed(4),
        performance: performance.toFixed(4),
        quality: (total === 0 ? 1 : good / total).toFixed(4),
        oee: (availability * performance * (total === 0 ? 1 : good / total)).toFixed(4),
        overRate: false,
        sourceEvents: 6 + Math.floor(n * 20),
      });
    }
  }
  return { items };
});
