// Ops control plane (/api/v1/ops) for the demo operations console: the two regions Cedar Ridge Pumps' platform runs in,
// break-glass windows (one open, one waiting for review, one reviewed), the service level report and the company licences.
// The operators are the IT people of finance/people.ts, so their names resolve through the user-name lookup.
import type { BreakGlass, LicenseItem, Region, SloLine, SloWindow } from "../../modules/ops/api";
import { reply, route } from "../backend";
import { at, day, uid } from "../store";
import { byUsername } from "./finance/people";

const BASE = "/api/v1/ops";
const K_BREAK_GLASS = 901;
const K_TENANT = 902;

const regions: Region[] = [
  { name: "us-east", role: "PRIMARY", lagSeconds: 0 },
  { name: "us-west", role: "PASSIVE", lagSeconds: 42 },
];

const tom = byUsername("tom.becker").id;
const jordan = byUsername("jordan.avery").id;

const windows: BreakGlass[] = [
  {
    id: uid(K_BREAK_GLASS, 1),
    userId: tom,
    reasonCode: "PRODUCTION_OUTAGE",
    ticketRef: "INC-2291",
    grantedAt: at(-0.01),
    expiresAt: at(0.03),
    reviewDueAt: at(3.03),
    uses: 2,
  },
  {
    id: uid(K_BREAK_GLASS, 2),
    userId: tom,
    reasonCode: "APPROVER_UNAVAILABLE",
    ticketRef: "INC-2240",
    grantedAt: at(-1.2),
    expiresAt: at(-1.15),
    reviewDueAt: at(1.85),
    uses: 1,
  },
  {
    id: uid(K_BREAK_GLASS, 3),
    userId: tom,
    reasonCode: "REGION_LOSS",
    ticketRef: "INC-2107",
    grantedAt: at(-21),
    expiresAt: at(-20.95),
    revokedAt: at(-20.97),
    reviewDueAt: at(-17.95),
    reviewedBy: jordan,
    reviewedAt: at(-20),
    reviewOutcome: "APPROVED",
    reviewNote: "Failover drill during the storm warning; steps match the runbook.",
    uses: 4,
  },
];

const SERVICES: [string, number, number, number][] = [
  // service, requests per hour, 5xx per hour, p95 seconds
  ["ledger", 18_400, 2, 0.18],
  ["sales", 9_200, 1, 0.24],
  ["purchasing", 6_100, 0, 0.21],
  ["inventory", 12_800, 3, 0.31],
  ["warehouse", 7_400, 19, 0.44],
  ["billing", 3_300, 0, 2.6],
  ["identity", 22_500, 1, 0.06],
  ["production", 4_900, 2, 0.38],
];
const HOURS: Record<SloWindow, number> = { "1h": 1, "6h": 6, "24h": 24, "7d": 168, "30d": 720 };

function sloLine([service, perHour, failedPerHour, p95]: [string, number, number, number], hours: number): SloLine {
  const requests = perHour * hours;
  const failed = failedPerHour * hours;
  const availability = 1 - failed / requests;
  const latencyTargetMs = service === "identity" ? 500 : 2000;
  const latencyP95Ms = Math.round(p95 * 1000);
  const availabilityBreached = availability < 0.999;
  const latencyBreached = latencyP95Ms > latencyTargetMs;
  const breached = availabilityBreached || latencyBreached;
  return {
    service,
    requests,
    availability,
    availabilityTarget: 0.999,
    latencyP95Ms,
    latencyTargetMs,
    errorBudgetRemaining: 1 - failed / requests / 0.001,
    availabilityBreached,
    latencyBreached,
    breached,
    ...(breached ? { breachedSince: at(-0.4) } : {}),
    noTraffic: false,
  };
}

const tenant = (n: number) => uid(K_TENANT, n);
const license = (n: number, name: string, expiresIn: number, full: number, active: number, state: LicenseItem["status"]["state"], codes: string[]): LicenseItem => ({
  license: { tenantId: tenant(n), issuedAt: day(expiresIn - 365), expiresAt: day(expiresIn), fullUsers: full, operationalUsers: 40, selfserviceUsers: 200 },
  version: 2,
  status: { state, daysToExpiry: expiresIn, codes },
  messages: [],
  usage: { activeFull: active, activeOperational: 31, activeSelfservice: 144, countedAt: at(-0.02), lastState: state },
  tenantName: name,
});
const licenses: LicenseItem[] = [
  license(1, "Cedar Ridge Pumps", 212, 60, 47, "VALID", []),
  license(2, "Cedar Ridge Pumps (training sandbox)", 18, 10, 6, "VALID", ["LIC-1801"]),
  license(3, "Cedar Ridge Pumps Canada", 95, 15, 17, "GRACE", ["LIC-1802"]),
];

route("GET", `${BASE}/regions`, () => ({ items: regions, maxLagSeconds: 300 }));
route("GET", `${BASE}/break-glass`, () => ({ items: windows }));
route("GET", `${BASE}/slo`, ({ query }) => {
  const window = (query.get("window") ?? "24h") as SloWindow;
  const hours = HOURS[window];
  if (hours === undefined) return reply.problem(422, "OPS-1726", "window must be one of 1h, 6h, 24h, 7d, 30d");
  const services = SERVICES.map((s) => sloLine(s, hours)).sort((a, b) => a.service.localeCompare(b.service));
  return { window, generatedAt: at(0), breached: services.filter((s) => s.breached).length, services };
});
route("GET", `${BASE}/licenses`, () => ({ items: licenses }));
