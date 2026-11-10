// Field service (/api/v1/service) for the demo: service cases on pumps Cedar Ridge sold, with the agreement each
// resolved to, SLA clocks relative to now, status moves that pause and resume them, and the work orders of a case.
// The screens print the first block of ids, so customer ids start with the account number (company.ts), and case,
// agreement and territory ids start with their number.
import type { CaseStatus, ServiceCase, SlaAlert, WorkOrder } from "../../../modules/service/api/client";
import { CUSTOMERS, LEGAL_ENTITY } from "../../company";
import { reply, route, type DemoRequest } from "../../backend";
import { at, dec, table } from "../../store";

const BASE = "/api/v1/service";
const HOUR = 1 / 24;

const caseId = (n: number) => `${String(n).padStart(8, "0")}-0000-4000-8000-${String(440_000_000 + n).padStart(12, "0")}`;
const woId = (n: number) => `${String(n).padStart(8, "0")}-0000-4000-8000-${String(441_000_000 + n).padStart(12, "0")}`;

const PROFILES: Record<string, { response: number; resolution: number; label: string }> = {
  "SLA-P1-4H": { response: 1, resolution: 4, label: "Critical, 4 hours" },
  "SLA-P2-1D": { response: 4, resolution: 24, label: "High, next business day" },
  "SLA-STD-3D": { response: 8, resolution: 72, label: "Standard, 3 business days" },
};

interface Seed {
  n: number;
  customer: number;
  channel: ServiceCase["channel"];
  priority: number;
  profile: keyof typeof PROFILES;
  agreement?: string;
  status: CaseStatus;
  /** Hours since the case was opened. */
  openedHoursAgo: number;
  respondedAfter?: number;
  resolvedAfter?: number;
  pausedHoursAgo?: number;
  pausedMinutes?: number;
  description: string;
  trace: string[];
}

function serviceCase(s: Seed): ServiceCase {
  const p = PROFILES[s.profile];
  const opened = -s.openedHoursAgo * HOUR;
  const paused = (s.pausedMinutes ?? 0) / 60;
  const pausedNow = s.pausedHoursAgo !== undefined ? s.pausedHoursAgo : 0;
  const responseDue = opened + p.response * HOUR;
  const resolutionDue = opened + (p.resolution + paused + pausedNow) * HOUR;
  const responded = s.respondedAfter !== undefined ? opened + s.respondedAfter * HOUR : undefined;
  const resolved = s.resolvedAfter !== undefined ? opened + s.resolvedAfter * HOUR : undefined;
  const alerts: SlaAlert[] = [];
  const end = resolved ?? 0;
  if ((responded ?? 0) > responseDue - p.response * 0.25 * HOUR) alerts.push("RESPONSE_WARNING");
  if ((responded ?? 0) > responseDue) alerts.push("RESPONSE_BREACH");
  if (end > resolutionDue - p.resolution * 0.25 * HOUR) alerts.push("RESOLUTION_WARNING");
  if (end > resolutionDue) alerts.push("RESOLUTION_BREACH");
  const breached = alerts.includes("RESOLUTION_BREACH") || alerts.includes("RESPONSE_BREACH");
  const state: ServiceCase["slaState"] = resolved !== undefined ? (breached ? "BREACHED" : "MET") : s.pausedHoursAgo !== undefined ? "PAUSED" : breached ? "BREACHED" : "RUNNING";
  return {
    id: caseId(s.n),
    number: `CASE-${String(s.n).padStart(6, "0")}`,
    customerPartyId: CUSTOMERS[s.customer].id,
    legalEntityId: LEGAL_ENTITY.id,
    serviceObjectId: null,
    channel: s.channel,
    priority: s.priority,
    description: s.description,
    agreementId: s.agreement ?? null,
    slaProfileId: s.profile,
    entitlementTrace: s.trace,
    openedAt: at(opened),
    respondedAt: responded === undefined ? null : at(responded),
    resolvedAt: resolved === undefined ? null : at(resolved),
    slaResponseDue: at(responseDue),
    slaResponseWarnAt: at(responseDue - p.response * 0.25 * HOUR),
    slaResolutionDue: at(resolutionDue),
    slaResolutionWarnAt: at(resolutionDue - p.resolution * 0.25 * HOUR),
    slaPausedMinutes: s.pausedMinutes ?? 0,
    slaPausedAt: s.pausedHoursAgo !== undefined ? at(-s.pausedHoursAgo * HOUR) : null,
    slaState: state,
    slaAlerts: alerts,
    status: s.status,
    version: 1 + (responded !== undefined ? 1 : 0) + (s.pausedHoursAgo !== undefined || s.pausedMinutes ? 1 : 0) + (resolved !== undefined ? 1 : 0) + (s.status === "CLOSED" ? 1 : 0),
  };
}

const covered = (agreement: string, profile: string) => [
  `Customer has active agreement ${agreement}.`,
  `The agreement covers the pump model on the case; SLA profile ${profile} (${PROFILES[profile].label}).`,
];
const billable = (profile: string) => ["No active service agreement covers this customer.", `Fallback SLA profile ${profile} applies; the work is billable.`];

const cases = table<ServiceCase>([
  serviceCase({ n: 418, customer: 3, channel: "IOT", priority: 1, profile: "SLA-P1-4H", agreement: "SVA26014", status: "IN_PROGRESS", openedHoursAgo: 3.2, respondedAfter: 0.4, description: "BP-300 booster skid at the Corpus Christi plant raised a discharge pressure alarm; pump 2 tripped on high vibration.", trace: covered("SVA26014", "SLA-P1-4H") }),
  serviceCase({ n: 417, customer: 0, channel: "PHONE", priority: 2, profile: "SLA-P2-1D", agreement: "SVA26009", status: "NEW", openedHoursAgo: 1.5, description: "CP-400 at the Lakeside lift station is noisy at start-up; operator suspects a bearing.", trace: covered("SVA26009", "SLA-P2-1D") }),
  serviceCase({ n: 416, customer: 2, channel: "EMAIL", priority: 2, profile: "SLA-P2-1D", agreement: "SVA26011", status: "WAITING_PARTS", openedHoursAgo: 30, respondedAfter: 2, pausedHoursAgo: 20, description: "Mechanical seal leak on a CP-400 dewatering pump after 300 hours. Seal kit SEAL-MK-125 on order; see NCR NC-24034.", trace: covered("SVA26011", "SLA-P2-1D") }),
  serviceCase({ n: 415, customer: 1, channel: "PORTAL", priority: 3, profile: "SLA-STD-3D", status: "WAITING_CUSTOMER", openedHoursAgo: 52, respondedAfter: 5, pausedHoursAgo: 26, pausedMinutes: 90, description: "SP-150 well pump delivers low flow. Asked the co-op for the well depth and the drawdown reading.", trace: billable("SLA-STD-3D") }),
  serviceCase({ n: 414, customer: 4, channel: "PHONE", priority: 2, profile: "SLA-P2-1D", status: "IN_PROGRESS", openedHoursAgo: 29, respondedAfter: 6.5, description: "Two SP-150 pumps on a centre-pivot system cycle on and off; controller wiring suspected.", trace: billable("SLA-P2-1D") }),
  serviceCase({ n: 412, customer: 5, channel: "INTERNAL", priority: 3, profile: "SLA-STD-3D", agreement: "SVA26003", status: "RESOLVED", openedHoursAgo: 120, respondedAfter: 2, resolvedAfter: 40, description: "Commissioning of the DP-100 diaphragm pumps in the Metro Facilities central plant.", trace: covered("SVA26003", "SLA-STD-3D") }),
  serviceCase({ n: 409, customer: 0, channel: "PHONE", priority: 1, profile: "SLA-P1-4H", agreement: "SVA26009", status: "CLOSED", openedHoursAgo: 260, respondedAfter: 0.5, resolvedAfter: 5.5, description: "CP-200 at the north reservoir would not start after a power outage; motor starter replaced.", trace: covered("SVA26009", "SLA-P1-4H") }),
  serviceCase({ n: 405, customer: 2, channel: "EMAIL", priority: 3, profile: "SLA-STD-3D", agreement: "SVA26011", status: "CLOSED", openedHoursAgo: 470, respondedAfter: 3, resolvedAfter: 50, description: "Request for a spare-parts list for the CP-400 fleet at the Elko mine.", trace: covered("SVA26011", "SLA-STD-3D") }),
]);

route("GET", `${BASE}/cases`, ({ query }) => {
  const status = query.get("status");
  const sla = query.get("slaState");
  return { items: cases.all().filter((c) => (!status || c.status === status) && (!sla || c.slaState === sla)).sort((a, b) => b.openedAt.localeCompare(a.openedAt)) };
});

route("GET", `${BASE}/cases/{id}`, ({ params }) => cases.get(params.id) ?? reply.problem(404, "SVC-0404", "Service case not found"));

const MOVES: Record<CaseStatus, CaseStatus[]> = {
  NEW: ["IN_PROGRESS", "CANCELED"],
  IN_PROGRESS: ["WAITING_CUSTOMER", "WAITING_PARTS", "RESOLVED", "CANCELED"],
  WAITING_CUSTOMER: ["IN_PROGRESS", "RESOLVED", "CANCELED"],
  WAITING_PARTS: ["IN_PROGRESS", "RESOLVED", "CANCELED"],
  RESOLVED: ["CLOSED", "IN_PROGRESS"],
  CLOSED: [],
  CANCELED: [],
};

route("POST", `${BASE}/cases/{id}:transition`, (req: DemoRequest) => {
  const c = cases.get(req.params.id);
  if (!c) return reply.problem(404, "SVC-0404", "Service case not found");
  const v = Number((req.headers.get("If-Match") ?? "").replace(/"/g, "")) || undefined;
  if (v !== undefined && v !== c.version) return reply.problem(409, "SVC-0409", "The case changed since you opened it. Reload it and try again.");
  const to = String((req.body as { to?: string } | undefined)?.to ?? "") as CaseStatus;
  if (!MOVES[c.status]?.includes(to)) return reply.problem(409, "SVC-CASE-002", `A ${c.status.toLowerCase().replace(/_/g, " ")} case cannot move to ${to.toLowerCase().replace(/_/g, " ")}`);
  const now = at(0);
  const waiting = to === "WAITING_CUSTOMER" || to === "WAITING_PARTS";
  const wasPaused = c.slaPausedAt != null;
  const pausedMinutes = wasPaused && !waiting ? c.slaPausedMinutes + Math.round((Date.now() - Date.parse(c.slaPausedAt!)) / 60_000) : c.slaPausedMinutes;
  const resolutionDue = wasPaused && !waiting ? new Date(Date.parse(c.slaResolutionDue) + (pausedMinutes - c.slaPausedMinutes) * 60_000).toISOString() : c.slaResolutionDue;
  const resolved = to === "RESOLVED" ? now : to === "IN_PROGRESS" && c.status === "RESOLVED" ? null : c.resolvedAt;
  const late = resolved != null && resolved > resolutionDue;
  const next: ServiceCase = {
    ...c,
    status: to,
    respondedAt: c.respondedAt ?? (to === "IN_PROGRESS" ? now : c.respondedAt),
    resolvedAt: resolved,
    slaPausedAt: waiting ? (c.slaPausedAt ?? now) : null,
    slaPausedMinutes: pausedMinutes,
    slaResolutionDue: resolutionDue,
    slaState: to === "RESOLVED" || to === "CLOSED" ? (late || c.slaState === "BREACHED" ? "BREACHED" : "MET") : waiting ? "PAUSED" : c.slaState === "BREACHED" ? "BREACHED" : "RUNNING",
    version: c.version + 1,
  };
  return cases.update(c.id, next);
});

// ---- Work orders of the cases -------------------------------------------------------------------------------

function workOrder(n: number, caseN: number, status: WorkOrder["status"], hours: number, lines: [WorkOrder["lines"][number]["kind"], number, number, number][], extra: Partial<WorkOrder> = {}): WorkOrder {
  const c = cases.get(caseId(caseN))!;
  return {
    id: woId(n),
    number: `WO-${String(n).padStart(6, "0")}`,
    caseId: c.id,
    agreementId: c.agreementId,
    customerPartyId: c.customerPartyId,
    legalEntityId: c.legalEntityId,
    territoryId: c.customerPartyId === CUSTOMERS[3].id ? "GULFCST1" : "MIDWEST1",
    estDurationMinutes: Math.round(hours * 60),
    currency: "USD",
    status,
    lines: lines.map(([kind, qty, price, cost], i) => ({ id: `${woId(n).slice(0, 8)}-L${i + 1}`, kind, qty: dec(qty), unitPrice: dec(price), unitCost: dec(cost) })),
    technicianId: status === "UNSCHEDULED" ? null : "Samir Haddad",
    version: 2,
    ...extra,
  };
}

const workOrders = table<WorkOrder>([
  workOrder(2207, 418, "TRAVELING", 4, [["LABOR", 4, 165, 72], ["TRAVEL", 2, 95, 40], ["PART", 2, 24, 7.4]], { windowStart: at(0), windowEnd: at(6 * HOUR), address: "Corpus Christi desalination plant, TX" }),
  workOrder(2205, 416, "ON_HOLD", 3, [["LABOR", 3, 165, 72], ["PART", 1, 129, 48.5]], { address: "Summit Mining, Elko NV", technicianNote: "Waiting for the seal kit from stock." }),
  workOrder(2204, 414, "SCHEDULED", 2.5, [["LABOR", 2.5, 165, 72], ["TRAVEL", 3, 95, 40]], { windowStart: at(0.8), windowEnd: at(0.9), address: "Prairie Irrigation, Kearney NE" }),
  workOrder(2198, 412, "POSTED", 16, [["LABOR", 16, 175, 72], ["FEE", 1, 0, 0]], { completedAt: at(-3.4), firstTimeFix: true }),
  workOrder(2189, 409, "POSTED", 3, [["LABOR", 3, 165, 72], ["PART", 1, 310, 180]], { completedAt: at(-10.6), firstTimeFix: true }),
]);

route("GET", `${BASE}/work-orders`, ({ query }) => {
  const status = query.get("status");
  const cid = query.get("caseId");
  return { items: workOrders.all().filter((w) => (!status || w.status === status) && (!cid || w.caseId === cid)).sort((a, b) => b.number.localeCompare(a.number)) };
});
