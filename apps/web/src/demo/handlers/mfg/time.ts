// Shop floor time and attendance (/api/v1/shopfloor) for the demo: workers, registrations, worker-days and their
// approval, supervisor corrections, auto clock-outs, who is clocked in and the payroll export. The workers are the
// hourly staff of hr/people.ts, so the HR screens and the time screens name the same people.
import type {
  AbsenceCode,
  ApprovedDay,
  AutoClockOutReview,
  ClockedWorker,
  CostRate,
  Correction,
  CorrectionInput,
  DayFlag,
  PayAgreement,
  PayExport,
  PayLine,
  PayRevision,
  Registration,
  TimeProfile,
  Worker,
  WorkerDay,
} from "../../../modules/shopfloor/api/client";
import { reply, route } from "../../backend";
import { resourceByCode } from "../../company";
import { at, day, dec, table, uid } from "../../store";
import { FLOOR, displayName, partyIdOf, personById, type Person } from "../hr/people";
import { jobs } from "./shopfloor";

const BASE = "/api/v1/shopfloor";
const KIND_REG = 234;
const KIND_CORRECTION = 235;
const KIND_RATE = 236;
const KIND_PROFILE = 237;
const KIND_AGREEMENT = 238;
const KIND_ABSENCE_CODE = 239;
const HOUR = 3_600_000;
const SUPERVISOR = "Tom Brennan";
const PAYROLL = "Rachel Osei";

// --- Setup: time profiles, pay agreements, absence codes ---

const weekday = (start: string, end: string) => ({ dayType: "weekday" as const, shiftStart: start, shiftEnd: end, breaks: [{ from: start === "06:00" ? "10:00" : "18:00", to: start === "06:00" ? "10:30" : "18:30" }], roundingMinutes: 6, toleranceBeforeMin: 10, toleranceAfterMin: 10, flexAllowed: false });
const weekend = (dayType: "saturday" | "sunday") => ({ dayType, shiftStart: null, shiftEnd: null, breaks: [], roundingMinutes: 6, toleranceBeforeMin: 0, toleranceAfterMin: 0, flexAllowed: false });
const profiles: TimeProfile[] = [
  { id: uid(KIND_PROFILE, 1), code: "DAY-1", name: "First shift, 06:00 to 14:30", active: true, version: 2, days: [weekday("06:00", "14:30"), weekend("saturday"), weekend("sunday")], createdBy: PAYROLL, createdAt: at(-420), updatedAt: at(-90) },
  { id: uid(KIND_PROFILE, 2), code: "SWING", name: "Second shift, 14:00 to 22:30", active: true, version: 1, days: [weekday("14:00", "22:30"), weekend("saturday"), weekend("sunday")], createdBy: PAYROLL, createdAt: at(-420), updatedAt: at(-420) },
  { id: uid(KIND_PROFILE, 3), code: "PART-AM", name: "Part time mornings, 06:00 to 11:00", active: true, version: 1, days: [{ ...weekday("06:00", "11:00"), breaks: [] }, weekend("saturday"), weekend("sunday")], createdBy: PAYROLL, createdAt: at(-230), updatedAt: at(-230) },
];
const agreements: PayAgreement[] = [
  { id: uid(KIND_AGREEMENT, 1), code: "HOURLY-IA", name: "Iowa hourly production staff", dailyThreshold: "8.00", weeklyThreshold: "40.00", overtimeMultiplier: "1.50", sundayMultiplier: "2.00", weekStart: 1, shiftPremiums: [{ name: "Second shift", from: "18:00", to: "22:30", premium: "0.10" }], active: true, version: 3, createdBy: PAYROLL, createdAt: at(-700), updatedAt: at(-60) },
  { id: uid(KIND_AGREEMENT, 2), code: "MAINT-IA", name: "Maintenance on call", dailyThreshold: "8.00", weeklyThreshold: "40.00", overtimeMultiplier: "1.50", sundayMultiplier: "2.00", weekStart: 1, shiftPremiums: [{ name: "Night call-out", from: "22:00", to: "06:00", premium: "0.25" }], active: true, version: 1, createdBy: PAYROLL, createdAt: at(-700), updatedAt: at(-700) },
];
const absenceCodes: AbsenceCode[] = [
  { id: uid(KIND_ABSENCE_CODE, 1), code: "SICK", name: "Sick leave", paid: true, payMultiplier: "1", active: true, version: 1, createdBy: PAYROLL, createdAt: at(-700), updatedAt: at(-700) },
  { id: uid(KIND_ABSENCE_CODE, 2), code: "VAC", name: "Vacation", paid: true, payMultiplier: "1", active: true, version: 1, createdBy: PAYROLL, createdAt: at(-700), updatedAt: at(-700) },
  { id: uid(KIND_ABSENCE_CODE, 3), code: "TRAIN", name: "Training", paid: true, payMultiplier: "1", active: true, version: 1, createdBy: PAYROLL, createdAt: at(-400), updatedAt: at(-400) },
  { id: uid(KIND_ABSENCE_CODE, 4), code: "UNPAID", name: "Unpaid leave", paid: false, payMultiplier: "0", active: true, version: 1, createdBy: PAYROLL, createdAt: at(-700), updatedAt: at(-700) },
];
route("GET", `${BASE}/time-profiles`, () => ({ items: profiles }));
route("GET", `${BASE}/pay-agreements`, () => ({ items: agreements }));
route("GET", `${BASE}/absence-codes`, () => ({ items: absenceCodes }));

// --- Workers ---

const BADGE = (p: Person) => `B-${p.number.slice(2)}`;
const profileOf = (p: Person) => (p.employmentType === "PART_TIME" ? profiles[2] : profiles[0]);
const workers = table<Worker>(
  FLOOR.map((p) => ({
    id: p.id,
    partyId: partyIdOf(p),
    name: displayName(p),
    badgeId: BADGE(p),
    userId: null,
    costRate: dec(p.pay),
    currency: "USD",
    active: true,
    version: 2,
    createdAt: `${day(p.hired)}T14:00:00Z`,
    profileId: profileOf(p).id,
    payAgreementId: p.jobCode === "MAINT-TECH" ? agreements[1].id : agreements[0].id,
    defaultResourceId: p.resource ? resourceByCode(p.resource).id : null,
  })),
);

route("GET", `${BASE}/workers`, (req) => {
  const active = req.query.get("active");
  const rows = workers.all().filter((w) => active === null || String(w.active) === active);
  return { items: rows.sort((a, b) => a.name.localeCompare(b.name)) };
});
route("GET", `${BASE}/workers/{id}/cost-rates`, (req) => {
  const w = workers.get(req.params.id);
  if (!w) return reply.problem(404, "SF-0404", "Worker not found.");
  const year = new Date().getUTCFullYear();
  const now = Number(w.costRate);
  const items: CostRate[] = [
    { id: uid(KIND_RATE, workers.all().findIndex((x) => x.id === w.id) * 2 + 1), workerId: w.id, effectiveFrom: `${year}-01-01`, costRate: dec(now * 1.32), note: "Annual review: rate plus burden", createdBy: PAYROLL, createdAt: `${year - 1}-12-18T15:00:00Z` },
    { id: uid(KIND_RATE, workers.all().findIndex((x) => x.id === w.id) * 2 + 2), workerId: w.id, effectiveFrom: `${year - 1}-01-01`, costRate: dec(now * 1.27), note: null, createdBy: PAYROLL, createdAt: `${year - 2}-12-15T15:00:00Z` },
  ];
  return { items };
});
route("POST", `${BASE}/workers/{id}/cost-rates`, (req) => {
  const w = workers.get(req.params.id);
  if (!w) return reply.problem(404, "SF-0404", "Worker not found.");
  const body = req.body as { effectiveFrom: string; costRate: string; note: string | null };
  return reply.created({ id: crypto.randomUUID(), workerId: w.id, effectiveFrom: body.effectiveFrom, costRate: body.costRate, note: body.note, createdBy: "Jordan Avery", createdAt: at(0) } satisfies CostRate);
});
route("GET", `${BASE}/workers/{id}`, (req) => workers.get(req.params.id) ?? reply.problem(404, "SF-0404", "Worker not found."));
for (const command of ["assign-setup", "assign-resource", "deactivate", "reactivate"]) {
  route("POST", `${BASE}/workers/{id}:${command}`, (req) => {
    const w = workers.get(req.params.id);
    if (!w) return reply.problem(404, "SF-0404", "Worker not found.");
    const body = (req.body ?? {}) as { profileId?: string | null; payAgreementId?: string | null; resourceId?: string | null; reason?: string };
    const patch: Partial<Worker> =
      command === "assign-setup"
        ? { profileId: body.profileId ?? null, payAgreementId: body.payAgreementId ?? null }
        : command === "assign-resource"
          ? { defaultResourceId: body.resourceId ?? null }
          : command === "deactivate"
            ? { active: false, deactivatedAt: at(0), deactivatedBy: "Jordan Avery", deactivationReason: body.reason ?? null }
            : { active: true, deactivatedAt: null, deactivatedBy: null, deactivationReason: null };
    return reply.ok(workers.update(w.id, { ...patch, version: w.version + 1 }));
  });
}

// --- Worker-days and registrations of the last four weeks ---

const DAY_MS = 86_400_000;
const todayStart = Date.parse(`${day(0)}T00:00:00Z`);
const isWeekday = (date: string) => {
  const d = new Date(`${date}T00:00:00Z`).getUTCDay();
  return d !== 0 && d !== 6;
};
/** The n-th most recent weekday before today (1 is the last working day). */
function workday(n: number): string {
  let found = 0;
  for (let i = 1; i < 60; i++) {
    if (isWeekday(day(-i)) && ++found === n) return day(-i);
  }
  return day(-n);
}
const noise = (a: number, b: number) => {
  const x = Math.sin(a * 91.7 + b * 47.3) * 10_000;
  return x - Math.floor(x);
};
const stamp = (date: string, hours: number) => new Date(Date.parse(`${date}T00:00:00Z`) + hours * HOUR).toISOString().replace(".000Z", "Z");

interface DayPlan {
  person: Person;
  date: string;
  start: number;
  end: number;
  absence?: string;
  flags: DayFlag[];
}

/** Special days: an absence, a sick day overlapping a clock-in, an auto clock-out still waiting for review. */
const SPECIAL: Record<string, Partial<DayPlan>> = {};
const key = (p: Person, date: string) => `${p.number}|${date}`;
const byNumber = (n: string) => FLOOR.find((p) => p.number === n)!;
SPECIAL[key(byNumber("W-0158"), workday(1))] = { flags: ["missing-clock-out"], end: 22 - 0.04 };
SPECIAL[key(byNumber("W-0141"), workday(2))] = { flags: ["absence-overlap"], absence: "Sick leave" };
SPECIAL[key(byNumber("W-0158"), workday(6))] = { absence: "Vacation", start: 0, end: 0 };
SPECIAL[key(byNumber("W-0112"), workday(3))] = { absence: "Training", start: 0, end: 0 };

const plans: DayPlan[] = [];
for (const p of FLOOR) {
  for (let i = 28; i >= 1; i--) {
    const date = day(-i);
    if (!isWeekday(date) || -i < p.hired) continue;
    const n = noise(Number(p.number.slice(2)), i);
    const part = p.employmentType === "PART_TIME";
    const start = 6 - 0.1 + Math.round(n * 4) * 0.05;
    const extra = part ? 0 : n > 0.8 ? 1.5 : n > 0.6 ? 1 : n > 0.45 ? 0.5 : 0;
    const end = part ? 11 + Math.round(n * 2) * 0.05 : 14.5 + extra + Math.round(noise(i, 3) * 2) * 0.05;
    plans.push({ person: p, date, start, end, flags: [], ...SPECIAL[key(p, date)] });
  }
}

function payLines(plan: DayPlan): { hours: number; lines: PayLine[] } {
  const rate = plan.person.pay;
  if (plan.end <= plan.start) {
    const hours = plan.person.employmentType === "PART_TIME" ? 5 : 8;
    return { hours, lines: [{ type: plan.absence ?? "Absence", hours: dec(hours), multiplier: "1.00", amount: dec(hours * rate), currency: "USD" }] };
  }
  const brk = plan.person.employmentType === "PART_TIME" ? 0 : 0.5;
  const worked = Math.round((plan.end - plan.start - brk) * 10) / 10;
  const regular = Math.min(8, worked);
  const overtime = Math.max(0, worked - 8);
  const lines: PayLine[] = [{ type: "Regular", hours: dec(regular), multiplier: "1.00", amount: dec(regular * rate), currency: "USD" }];
  if (overtime > 0) lines.push({ type: "Overtime", hours: dec(overtime), multiplier: "1.50", amount: dec(overtime * rate * 1.5), currency: "USD" });
  return { hours: worked, lines };
}

/** Days up to the last working day of last week are approved; this week's and last Friday's wait for the supervisor. */
const approvedThrough = workday(3);
const days = table<WorkerDay & { id: string; exported?: string }>(
  plans.map((plan) => {
    const { hours, lines } = payLines(plan);
    const approved = plan.date <= approvedThrough && plan.flags.length === 0;
    return {
      id: key(plan.person, plan.date),
      workerId: plan.person.id,
      workerName: displayName(plan.person),
      workDate: plan.date,
      hours: dec(hours),
      flags: plan.flags,
      status: approved ? "approved" : "calculated",
      timeZone: "UTC",
      calculatedAt: stamp(plan.date, 23),
      approvedBy: approved ? SUPERVISOR : null,
      approvedAt: approved ? stamp(plan.date, 24 + 9.5) : null,
      payLines: lines,
    };
  }),
);

const publicDay = ({ id: _id, exported: _exported, ...d }: WorkerDay & { id: string; exported?: string }): WorkerDay => (void _id, void _exported, d);

route("GET", `${BASE}/worker-days`, (req) => {
  const q = req.query;
  const from = q.get("from") ?? day(-6);
  const to = q.get("to") ?? day(0);
  let rows = days.all().filter((d) => d.workDate >= from && d.workDate <= to);
  if (q.get("workerId")) rows = rows.filter((d) => d.workerId === q.get("workerId"));
  if (q.get("status")) rows = rows.filter((d) => d.status === q.get("status"));
  rows.sort((a, b) => b.workDate.localeCompare(a.workDate) || a.workerName.localeCompare(b.workerName));
  return { items: rows.map(publicDay) };
});

let regSeq = 0;
const registrations = table<Registration>([]);
for (const plan of plans) {
  if (plan.end <= plan.start) continue;
  const p = plan.person;
  const auto = plan.flags.includes("missing-clock-out");
  const mk = (kind: Registration["kind"], start: number, end: number, rest: Partial<Registration> = {}): Registration => ({
    id: uid(KIND_REG, ++regSeq),
    workerId: p.id,
    kind,
    jobId: null,
    bundleId: null,
    qtyGood: "0.0000",
    qtyScrap: "0.0000",
    scrapReasonId: null,
    startedAt: stamp(plan.date, start),
    stoppedAt: stamp(plan.date, end),
    hours: dec(end - start),
    serverReceivedAt: stamp(plan.date, start + 0.001),
    deviceId: "KIOSK-CR-01",
    offline: false,
    note: null,
    clockOffsetSeconds: null,
    flags: [],
    ...rest,
  });
  registrations.insert(mk("clock-in", plan.start, plan.end, auto ? { flags: ["auto-clock-out"], note: "Closed by the auto clock-out after 16 hours" } : {}));
  if (p.resource) {
    const mid = plan.start + (plan.end - plan.start) * 0.55;
    const job = jobs.all().find((j) => j.resourceId === resourceByCode(p.resource!).id);
    const order = job?.orderRef ?? "MO-24608";
    registrations.insert(mk("job-start", plan.start + 0.2, mid, { jobId: job?.id ?? null, qtyGood: dec(Math.round(4 + noise(plan.start, mid) * 8), 4), note: `${order} operation ${job?.operationRef ?? "10"}` }));
    registrations.insert(mk("job-start", mid + 0.5, Math.min(plan.end - 0.1, mid + 3), { jobId: job?.id ?? null, qtyGood: dec(Math.round(3 + noise(mid, plan.end) * 6), 4) }));
  } else {
    registrations.insert(mk("indirect", plan.start + 0.3, plan.end - 0.3, { note: "Preventive maintenance rounds" }));
  }
}

route("GET", `${BASE}/registrations`, (req) => {
  const q = req.query;
  if (q.get("bundleId")) return { items: [] };
  const from = q.get("from") ?? day(-6);
  const to = q.get("to") ?? day(0);
  const rows = registrations
    .all()
    .filter((r) => (!q.get("workerId") || r.workerId === q.get("workerId")) && r.startedAt.slice(0, 10) >= from && r.startedAt.slice(0, 10) <= to)
    .sort((a, b) => b.startedAt.localeCompare(a.startedAt));
  return { items: rows };
});

// --- Who is clocked in now: the first shift, minus the painter who called in sick ---

const CLOCKED: { number: string; hoursAgo: number; onJobFor?: number }[] = [
  { number: "W-0112", hoursAgo: 5.4, onJobFor: 1.2 },
  { number: "W-0127", hoursAgo: 5.6, onJobFor: 2.1 },
  { number: "W-0133", hoursAgo: 5.3, onJobFor: 3.6 },
  { number: "W-0141", hoursAgo: 5.5, onJobFor: 0.7 },
  { number: "W-0164", hoursAgo: 5.2, onJobFor: 4.4 },
  { number: "W-0171", hoursAgo: 3.1, onJobFor: 0.4 },
  { number: "W-0175", hoursAgo: 5.5 },
  { number: "W-0122", hoursAgo: 4.9 },
];
route("GET", `${BASE}/clocked-in`, () => {
  const items: ClockedWorker[] = CLOCKED.map((c) => {
    const p = byNumber(c.number);
    const w = workers.get(p.id)!;
    const job = p.resource ? jobs.all().find((j) => j.resourceId === resourceByCode(p.resource!).id && j.status !== "finished") : undefined;
    return {
      workerId: p.id,
      name: w.name,
      badgeId: w.badgeId,
      active: w.active,
      clockedInAt: at(-c.hoursAgo / 24),
      jobs: c.onJobFor !== undefined && job ? [{ jobId: job.id, bundleId: null, startedAt: at(-c.onJobFor / 24) }] : [],
    };
  });
  return { items: items.sort((a, b) => a.name.localeCompare(b.name)) };
});

// --- Supervisor corrections and auto clock-outs ---

const regOf = (number: string, date: string, kind: Registration["kind"] = "clock-in") => registrations.all().find((r) => r.workerId === byNumber(number).id && r.kind === kind && r.startedAt.startsWith(date));

const corrections = table<Correction>([]);
let corrSeq = 0;
function seedCorrection(number: string, date: string, kind: Registration["kind"], oldStart: number, oldStop: number, newStart: number, newStop: number, reason: string, correctedAfter: number) {
  const reg = regOf(number, date, kind);
  if (!reg) return;
  registrations.update(reg.id, { startedAt: stamp(date, newStart), stoppedAt: stamp(date, newStop), hours: dec(newStop - newStart) });
  corrections.insert({
    id: uid(KIND_CORRECTION, ++corrSeq),
    registrationId: reg.id,
    workerId: reg.workerId,
    reason,
    oldStartedAt: stamp(date, oldStart),
    oldStoppedAt: stamp(date, oldStop),
    oldHours: dec(oldStop - oldStart),
    newStartedAt: stamp(date, newStart),
    newStoppedAt: stamp(date, newStop),
    newHours: dec(newStop - newStart),
    correctedBy: SUPERVISOR,
    correctedAt: stamp(date, newStop + correctedAfter),
  });
}

// Marcus left his clock-in open; the sweep closed it at 16 hours and Tom set the real end the next morning.
const marcusDay = workday(4);
const marcus = regOf("W-0141", marcusDay);
const marcusEnd = marcus ? (Date.parse(marcus.stoppedAt ?? "") - Date.parse(`${marcusDay}T00:00:00Z`)) / HOUR : 14.6;
const marcusStart = marcus ? (Date.parse(marcus.startedAt) - Date.parse(`${marcusDay}T00:00:00Z`)) / HOUR : 6;
seedCorrection("W-0141", marcusDay, "clock-in", marcusStart, marcusStart + 16, marcusStart, marcusEnd, "Forgot to clock out after first shift; left at the end of shift, confirmed with the lead.", 18);
seedCorrection("W-0112", workday(2), "clock-in", 6.4, 14.5, 5.92, 14.5, "Badge reader at door 2 was down; clocked in on the paper sheet at 05:55.", 1.2);
seedCorrection("W-0164", workday(1), "job-start", 6.9, 9.9, 6.45, 9.9, "Booked the balancing job late; the balancer was running from 06:27.", 2.5);
seedCorrection("W-0171", workday(5), "clock-in", 5.95, 21.95, 5.95, 11.05, "Part-time shift ended at 11:03; the clock-out was not registered.", 13);

const autoClockOuts = table<AutoClockOutReview & { id: string }>([]);
for (const [number, date, reviewed] of [
  ["W-0158", workday(1), false],
  ["W-0141", marcusDay, true],
  ["W-0171", workday(5), true],
] as const) {
  const reg = regOf(number, date);
  const p = byNumber(number);
  if (!reg) continue;
  const clockIn = reg.startedAt;
  const clockOut = new Date(Date.parse(clockIn) + 16 * HOUR).toISOString().replace(".000Z", "Z");
  autoClockOuts.insert({ id: reg.id, registrationId: reg.id, workerId: p.id, workerName: displayName(p), clockedInAt: clockIn, clockedOutAt: clockOut, closedAt: new Date(Date.parse(clockOut) + 7 * 60_000).toISOString(), reviewed });
}

const inRange = (iso: string, q: URLSearchParams) => iso.slice(0, 10) >= (q.get("from") ?? day(-6)) && iso.slice(0, 10) <= (q.get("to") ?? day(0));
route("GET", `${BASE}/registration-corrections`, (req) => ({
  items: corrections
    .all()
    .filter((c) => inRange(c.newStartedAt, req.query) && (!req.query.get("workerId") || c.workerId === req.query.get("workerId")))
    .sort((a, b) => b.correctedAt.localeCompare(a.correctedAt)),
}));
route("GET", `${BASE}/auto-clock-outs`, (req) => ({
  items: autoClockOuts
    .all()
    .filter((a) => inRange(a.clockedOutAt, req.query) && (!req.query.get("workerId") || a.workerId === req.query.get("workerId")))
    .sort((a, b) => Number(a.reviewed) - Number(b.reviewed) || b.clockedOutAt.localeCompare(a.clockedOutAt))
    .map(({ id: _id, ...a }) => (void _id, a)),
}));
route("POST", `${BASE}/registrations:auto-clock-out`, (req) => ({ afterHours: Number(req.query.get("afterHours") ?? 16), items: [] }));

route("POST", `${BASE}/registrations/{id}:correct`, (req) => {
  const reg = registrations.get(req.params.id);
  if (!reg) return reply.problem(404, "SF-0126", "This registration does not exist.");
  const input = (req.body ?? {}) as CorrectionInput;
  if (!input.reason || input.reason.trim() === "") return reply.problem(422, "SF-0127", "Give a reason for the correction.", { reason: "Give a reason for the correction." });
  const startedAt = input.startedAt ?? reg.startedAt;
  const stoppedAt = input.stoppedAt ?? reg.stoppedAt;
  if (stoppedAt && Date.parse(stoppedAt) <= Date.parse(startedAt)) return reply.problem(422, "SF-0128", "The stop must be after the start.", { stoppedAt: "The stop must be after the start." });
  const hours = stoppedAt ? dec((Date.parse(stoppedAt) - Date.parse(startedAt)) / HOUR) : null;
  const correction: Correction = {
    id: crypto.randomUUID(),
    registrationId: reg.id,
    workerId: reg.workerId,
    reason: input.reason.trim(),
    oldStartedAt: reg.startedAt,
    oldStoppedAt: reg.stoppedAt,
    oldHours: reg.hours,
    newStartedAt: startedAt,
    newStoppedAt: stoppedAt,
    newHours: hours,
    correctedBy: "Jordan Avery",
    correctedAt: at(0),
  };
  registrations.update(reg.id, { startedAt, stoppedAt, hours, flags: reg.flags.filter((f) => f !== "auto-clock-out") });
  corrections.insert(correction);
  // Confirming an auto clock-out clears the review and the day's flag, so the day can be approved.
  if (autoClockOuts.get(reg.id)) autoClockOuts.update(reg.id, { reviewed: true, clockedOutAt: stoppedAt ?? reg.stoppedAt ?? at(0) });
  const p = personById(reg.workerId);
  const d = p ? days.get(key(p, reg.startedAt.slice(0, 10))) : undefined;
  if (d && hours !== null && reg.kind === "clock-in") {
    const worked = Math.max(0, Number(hours) - 0.5);
    const regular = Math.min(8, worked);
    const overtime = Math.max(0, worked - 8);
    const lines: PayLine[] = [{ type: "Regular", hours: dec(regular), multiplier: "1.00", amount: dec(regular * p!.pay), currency: "USD" }];
    if (overtime > 0) lines.push({ type: "Overtime", hours: dec(overtime), multiplier: "1.50", amount: dec(overtime * p!.pay * 1.5), currency: "USD" });
    days.update(d.id, { hours: dec(worked), flags: d.flags.filter((f) => f !== "missing-clock-out"), payLines: lines, calculatedAt: at(0) });
  }
  return reply.created(correction);
});

// --- Pay: calculate, approve, revise, export ---

const rangeOf = (body: unknown) => (body ?? {}) as { workerId?: string; from?: string; to?: string; reason?: string };
const daysIn = (workerId: string | undefined, from: string, to: string) => days.all().filter((d) => (!workerId || d.workerId === workerId) && d.workDate >= from && d.workDate <= to);

route("POST", `${BASE}/pay:calculate`, (req) => {
  const { workerId = "", from = day(-6), to = day(0) } = rangeOf(req.body);
  const rows = daysIn(workerId, from, to);
  for (const d of rows) days.update(d.id, { calculatedAt: at(0) });
  return { workerId, from, to, timeZone: "UTC", days: rows.map((d) => ({ workDate: d.workDate, hours: d.hours, flags: d.flags, payLines: d.payLines })) };
});

route("POST", `${BASE}/pay:approve`, (req) => {
  const { workerId = "", from = day(-6), to = day(0) } = rangeOf(req.body);
  const rows = daysIn(workerId, from, to);
  const flagged = rows.find((d) => d.flags.length > 0 && d.status === "calculated");
  if (flagged) return reply.problem(422, "SF-0006", `${flagged.workDate} has open flags (${flagged.flags.join(", ")}); resolve them before approving.`);
  const out: ApprovedDay[] = rows.map((d) => {
    const newly = d.status === "calculated";
    if (newly) days.update(d.id, { status: "approved", approvedBy: "Jordan Avery", approvedAt: at(0) });
    return { workDate: d.workDate, hours: d.hours, status: "approved", newlyApproved: newly, payLines: d.payLines };
  });
  return { workerId, from, to, days: out };
});

route("POST", `${BASE}/pay:revise`, (req) => {
  const { workerId = "", from = day(-6), to = day(0), reason = "" } = rangeOf(req.body);
  const rows = daysIn(workerId, from, to).filter((d) => d.status === "approved");
  if (rows.length === 0) return reply.problem(409, "SF-0129", "Nothing in the range differs from what was approved.");
  const revision: PayRevision = {
    workerId,
    from,
    to,
    reason,
    days: rows.map((d) => {
      const rev = (d.revision ?? 0) + 1;
      days.update(d.id, { revision: rev, revisedBy: "Jordan Avery", revisedAt: at(0) });
      return { workDate: d.workDate, status: d.status, revision: rev, changed: false, reversedLines: [], addedLines: [] };
    }),
  };
  return revision;
});

let batchSeq = 20440;
const csvOf = (rows: (WorkerDay & { id: string })[]) =>
  ["personnel_number,worker,work_date,pay_type,hours,multiplier,amount,currency"]
    .concat(
      rows.flatMap((d) => {
        const p = personById(d.workerId);
        return d.payLines.map((l) => [p?.number ?? "", `"${d.workerName}"`, d.workDate, l.type, l.hours, l.multiplier, l.amount, l.currency].join(","));
      }),
    )
    .join("\n");

const exportsTable = table<PayExport & { id: string }>([]);
function exportRange(from: string, to: string, by: string, when: string): (PayExport & { id: string }) | null {
  const rows = days.all().filter((d) => d.status === "approved" && !d.exported && d.workDate >= from && d.workDate <= to);
  if (rows.length === 0) return null;
  batchSeq += 1 + (batchSeq % 5);
  const batchId = `PX-${batchSeq}`;
  for (const d of rows) days.update(d.id, { exported: batchId });
  return exportsTable.insert({ id: batchId, batchId, fromDate: from, toDate: to, lineCount: rows.reduce((n, d) => n + d.payLines.length, 0), createdBy: by, createdAt: when, csv: csvOf(rows) });
}
// The two weeks before last were exported on the Monday after; last week is approved and waiting for its export.
for (const weeksBack of [3, 2]) {
  const monday = new Date(todayStart - (((new Date(todayStart).getUTCDay() + 6) % 7) + weeksBack * 7) * DAY_MS);
  const from = monday.toISOString().slice(0, 10);
  const to = new Date(monday.getTime() + 6 * DAY_MS).toISOString().slice(0, 10);
  exportRange(from, to, PAYROLL, new Date(monday.getTime() + 7 * DAY_MS + 15.5 * HOUR).toISOString());
}

const listed = ({ id: _id, csv: _csv, ...b }: PayExport & { id: string }): PayExport => (void _id, void _csv, b);
route("GET", `${BASE}/pay-exports`, () => ({ items: exportsTable.all().sort((a, b) => b.createdAt.localeCompare(a.createdAt)).map(listed) }));
route("GET", `${BASE}/pay-exports/{id}`, (req) => {
  const b = exportsTable.get(req.params.id);
  if (!b) return reply.problem(404, "SF-0404", "Export batch not found.");
  const { id: _id, ...out } = b;
  return (void _id, out);
});
route("POST", `${BASE}/pay:export`, (req) => {
  const { from = day(-6), to = day(0) } = rangeOf(req.body);
  const batch = exportRange(from, to, "Jordan Avery", at(0));
  if (!batch) return reply.problem(409, "SF-0140", "No approved pay lines in the range are waiting for export.");
  const { id: _id, ...out } = batch;
  return reply.created((void _id, out));
});
