// Self-service and approval queues for the demo: the signed-in manager's profile and team, profile changes awaiting
// a decision, and absence requests. The signed-in user is Jordan Avery, plant operations manager.
import { LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { at, day, uid } from "../../store";
import { JORDAN, displayName, partyIdOf, person, personById, reportsOf, type Person } from "./people";

const K_CHANGE = 707;
const K_ABSENCE = 708;
const K_ABSENCE_PLAN = 709;
const K_EMPLOYMENT = 701;
const PAGE = (items: unknown[]) => ({ items });

const worker = (p: Person) => ({
  id: p.id,
  personnelNumber: p.number,
  workerType: p.employmentType === "CONTRACTOR" ? "CONTRACTOR" : "EMPLOYEE",
  partyPersonId: partyIdOf(p),
  legalName: p.name,
  ...(p.preferredName ? { preferredName: p.preferredName } : {}),
  createdAt: at(p.hired),
  updatedAt: at(-3),
});
const asOf = (p: Person) => ({
  workerId: p.id,
  date: day(0),
  employments: [
    {
      id: uid(K_EMPLOYMENT, 100 + Number(p.number.slice(2))),
      workerId: p.id,
      legalEntityId: LEGAL_ENTITY.id,
      employmentType: p.employmentType,
      assignmentKind: "PRIMARY",
      hireDate: day(p.hired),
      validFrom: day(p.hired),
      status: "ACTIVE",
    },
  ],
  positionId: null,
  positionNumber: null,
  fteShare: p.employmentType === "PART_TIME" ? "0.60" : "1.00",
  managerWorkerId: p.managerId,
});

const me = person("W-0101");
route("GET", "/api/v1/hr/me", () => ({
  ...worker(me),
  pii: { nationalId: "***-**-4417", bankAccount: "****2290", homeAddress: "Marion, IA 52302", masked: true },
  asOf: asOf(me),
}));

const team = reportsOf(JORDAN);
route("GET", "/api/v1/hr/me/team", () => PAGE(team.map(worker)));

// Profile changes: the manager's queue holds address changes of direct reports, HR's holds every change.
interface Change {
  n: number;
  who: string;
  kind: "PREFERRED_NAME" | "HOME_ADDRESS" | "BANK_ACCOUNT";
  status: "PENDING" | "APPLIED" | "APPROVED" | "REJECTED";
  summary: string;
  created: number;
  decidedBy?: string;
  note?: string;
}
const CHANGES: Change[] = [
  { n: 1, who: "W-0122", kind: "HOME_ADDRESS", status: "PENDING", summary: "418 Maple Ridge Dr, Marion, IA 52302", created: -1 },
  { n: 2, who: "W-0146", kind: "HOME_ADDRESS", status: "PENDING", summary: "2210 Prairie View Ct, Cedar Rapids, IA 52402", created: -2 },
  { n: 3, who: "W-0118", kind: "BANK_ACCOUNT", status: "PENDING", summary: "Checking ending 6048", created: -1 },
  { n: 4, who: "W-0150", kind: "PREFERRED_NAME", status: "APPLIED", summary: "Nadia Petrov", created: -20 },
  { n: 5, who: "W-0109", kind: "HOME_ADDRESS", status: "APPROVED", summary: "75 Ridgeway Ln, Hiawatha, IA 52233", created: -12, decidedBy: "Jordan Avery" },
  { n: 6, who: "W-0112", kind: "BANK_ACCOUNT", status: "REJECTED", summary: "Savings ending 1180", created: -9, decidedBy: "Rachel Osei", note: "Account holder name does not match payroll record." },
];
const changeRow = (c: Change) => {
  const p = person(c.who);
  return {
    id: uid(K_CHANGE, c.n),
    workerId: p.id,
    workerName: p.name,
    personnelNumber: p.number,
    kind: c.kind,
    status: c.status,
    summary: c.summary,
    requestedBy: p.name,
    ...(c.decidedBy ? { decidedBy: c.decidedBy, decidedAt: at(c.created + 1) } : {}),
    ...(c.note ? { decisionNote: c.note } : {}),
    createdAt: at(c.created),
  };
};
const teamIds = new Set(team.map((p) => p.number));
route("GET", "/api/v1/hr/me/team/profile-changes", ({ query }) =>
  PAGE(CHANGES.filter((c) => c.kind === "HOME_ADDRESS" && teamIds.has(c.who) && (!query.get("status") || c.status === query.get("status"))).map(changeRow)),
);
route("GET", "/api/v1/hr/me/team/{id}", ({ params }) => {
  const p = team.find((x) => x.id === params.id);
  return p ? { ...worker(p), asOf: asOf(p) } : reply.problem(404, "HCM-WRK-NOT-FOUND", "That worker is not on your team");
});
route("GET", "/api/v1/hr/profile-changes", ({ query }) => PAGE(CHANGES.filter((c) => !query.get("status") || c.status === query.get("status")).map(changeRow)));
route("GET", "/api/v1/hr/me/profile-changes", () => PAGE([]));
const decide = (queue: string) =>
  route("POST", `/api/v1/hr/${queue}/{id}/decide`, ({ params, body }) => {
    const c = CHANGES.find((x) => uid(K_CHANGE, x.n) === params.id);
    if (!c) return reply.problem(404, "HCM-CHG-NOT-FOUND", "No change with that id");
    const input = (body ?? {}) as { decision?: string; note?: string };
    c.status = input.decision === "REJECT" ? "REJECTED" : "APPROVED";
    c.decidedBy = displayName(personById(JORDAN)!);
    if (input.note) c.note = input.note;
    return changeRow(c);
  });
decide("me/team/profile-changes");
decide("profile-changes");

// Absence requests.
const PLANS = { PTO: { n: 1, unit: "HOURS" }, SICK: { n: 2, unit: "HOURS" } } as const;
interface Absence {
  n: number;
  who: string;
  plan: keyof typeof PLANS;
  from: number;
  to: number;
  qty: string;
  status: "SUBMITTED" | "APPROVED" | "REJECTED" | "CANCELED";
  reason: string;
  created: number;
}
const ABSENCES: Absence[] = [
  { n: 1, who: "W-0112", plan: "PTO", from: 12, to: 16, qty: "40.00", status: "SUBMITTED", reason: "Family vacation", created: -3 },
  { n: 2, who: "W-0141", plan: "PTO", from: 20, to: 20, qty: "8.00", status: "SUBMITTED", reason: "Moving day", created: -2 },
  { n: 3, who: "W-0164", plan: "SICK", from: 5, to: 6, qty: "16.00", status: "SUBMITTED", reason: "Scheduled dental surgery", created: -1 },
  { n: 4, who: "W-0158", plan: "PTO", from: 30, to: 34, qty: "40.00", status: "SUBMITTED", reason: "Wedding travel", created: -4 },
  { n: 5, who: "W-0127", plan: "PTO", from: -14, to: -12, qty: "24.00", status: "APPROVED", reason: "Long weekend", created: -30 },
  { n: 6, who: "W-0133", plan: "PTO", from: 8, to: 9, qty: "16.00", status: "REJECTED", reason: "Overlaps the pump qualification test window", created: -10 },
];
const absenceRow = (a: Absence) => {
  const p = person(a.who);
  return {
    id: uid(K_ABSENCE, a.n),
    workerId: p.id,
    personnelNumber: p.number,
    workerName: p.name,
    planId: uid(K_ABSENCE_PLAN, PLANS[a.plan].n),
    planCode: a.plan,
    unit: PLANS[a.plan].unit,
    from: day(a.from),
    to: day(a.to),
    quantity: a.qty,
    status: a.status,
    reason: a.reason,
    requestedBy: p.name,
    ...(a.status !== "SUBMITTED" ? { decidedBy: "Tom Brennan", decidedAt: at(a.created + 1) } : {}),
    createdAt: at(a.created),
  };
};
route("GET", "/api/v1/hr/absence/requests", ({ query }) => PAGE(ABSENCES.filter((a) => (!query.get("status") || a.status === query.get("status")) && (!query.get("workerId") || person(a.who).id === query.get("workerId"))).map(absenceRow)));
for (const verb of ["decide", "cancel"] as const) {
  route("POST", `/api/v1/hr/absence/requests/{id}/${verb}`, ({ params, body }) => {
    const a = ABSENCES.find((x) => uid(K_ABSENCE, x.n) === params.id);
    if (!a) return reply.problem(404, "HCM-ABS-NOT-FOUND", "No absence request with that id");
    a.status = verb === "cancel" ? "CANCELED" : (body as { decision?: string } | undefined)?.decision === "REJECT" ? "REJECTED" : "APPROVED";
    return absenceRow(a);
  });
}
