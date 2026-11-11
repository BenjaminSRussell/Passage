// HR organisation screens for the demo: positions, requisitions, personnel actions and compensation cycles of
// Cedar Ridge Pumps. People come from ./people so names match the shop floor and time screens.
import { LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, uid } from "../../store";
import { ELENA, JORDAN, RACHEL, TOM, displayName, person, personById } from "./people";

const K_POSITION = 700;
const K_EMPLOYMENT = 701;
const K_REQ = 704;
const K_ACTION = 705;
const K_CYCLE = 706;

const pos = (n: number) => uid(K_POSITION, n);
const PAGE = (items: unknown[]) => ({ items });

interface Pos {
  n: number;
  number: string;
  job: string;
  dept: string;
  fte: string;
  reportsTo?: number;
  from: number;
  overfill?: boolean;
  holders: string[];
}
const POSITIONS: Pos[] = [
  { n: 1, number: "POS-1001", job: "OPS-MGR", dept: "Operations", fte: "1.00", from: -2920, holders: ["W-0101"] },
  { n: 2, number: "POS-1010", job: "PROD-SUP", dept: "Production", fte: "1.00", reportsTo: 1, from: -3310, holders: ["W-0109"] },
  { n: 3, number: "POS-1020", job: "QA-ENG", dept: "Quality", fte: "1.00", reportsTo: 1, from: -1490, holders: ["W-0118"] },
  { n: 4, number: "POS-1030", job: "MACH-2", dept: "Production", fte: "1.00", reportsTo: 2, from: -2410, overfill: true, holders: ["W-0112"] },
  { n: 5, number: "POS-1031", job: "MACH-2", dept: "Production", fte: "1.00", reportsTo: 2, from: -400, holders: [] },
  { n: 6, number: "POS-1040", job: "ASM-2", dept: "Production", fte: "1.00", reportsTo: 2, from: -1290, overfill: true, holders: ["W-0141", "W-0175"] },
  { n: 7, number: "POS-1050", job: "TEST-TECH", dept: "Quality", fte: "1.00", reportsTo: 3, from: -1820, holders: ["W-0133"] },
  { n: 8, number: "POS-1060", job: "MAINT-TECH", dept: "Maintenance", fte: "1.00", reportsTo: 1, from: -2630, holders: ["W-0122"] },
];
const positionRow = (p: Pos) => ({
  id: pos(p.n),
  positionNumber: p.number,
  jobCode: p.job,
  departmentId: p.dept,
  legalEntityId: LEGAL_ENTITY.id,
  fte: p.fte,
  allowOverfill: p.overfill ?? false,
  validFrom: `${day(p.from)}T00:00:00Z`,
  ...(p.reportsTo ? { reportsToPositionId: pos(p.reportsTo) } : {}),
});
route("GET", "/api/v1/hr/positions", () => PAGE(POSITIONS.map(positionRow)));
route("GET", "/api/v1/hr/positions/{id}/skill-requirements", () => PAGE([]));
route("GET", "/api/v1/hr/positions/{id}", ({ params }) => {
  const p = POSITIONS.find((x) => pos(x.n) === params.id);
  if (!p) return reply.problem(404, "HCM-POS-NOT-FOUND", "No position with that id");
  return {
    ...positionRow(p),
    assignments: p.holders.map((number, i) => {
      const who = person(number);
      return {
        id: uid(K_EMPLOYMENT, p.n * 10 + i),
        positionId: pos(p.n),
        workerId: who.id,
        employmentId: uid(K_EMPLOYMENT, 100 + Number(number.slice(2))),
        fteShare: "1.00",
        isPrimary: true,
        validFrom: `${day(who.hired)}T00:00:00Z`,
      };
    }),
  };
});

// Requisitions.
interface Req {
  n: number;
  number: string;
  position: number;
  headcount: number;
  manager: string;
  start: number;
  status: "DRAFT" | "APPROVED" | "OPEN" | "FILLED";
  filled: number;
  created: number;
}
const REQS: Req[] = [
  { n: 1, number: "REQ-2026-014", position: 5, headcount: 1, manager: TOM, start: 21, status: "OPEN", filled: 0, created: -24 },
  { n: 2, number: "REQ-2026-015", position: 6, headcount: 2, manager: TOM, start: 35, status: "OPEN", filled: 1, created: -18 },
  { n: 3, number: "REQ-2026-016", position: 7, headcount: 1, manager: ELENA, start: 60, status: "APPROVED", filled: 0, created: -9 },
  { n: 4, number: "REQ-2026-017", position: 8, headcount: 1, manager: JORDAN, start: 75, status: "DRAFT", filled: 0, created: -3 },
  { n: 5, number: "REQ-2026-009", position: 4, headcount: 1, manager: TOM, start: -30, status: "FILLED", filled: 1, created: -95 },
];
const reqRow = (r: Req) => ({
  id: uid(K_REQ, r.n),
  requisitionNumber: r.number,
  positionId: pos(r.position),
  legalEntityId: LEGAL_ENTITY.id,
  headcount: r.headcount,
  hiringManagerWorkerId: r.manager,
  plannedStartDate: day(r.start),
  allowOverfill: false,
  status: r.status,
  filled: r.filled,
  createdAt: at(r.created),
});
route("GET", "/api/v1/hr/requisitions", ({ query }) => PAGE(REQS.filter((r) => !query.get("status") || r.status === query.get("status")).map(reqRow)));
route("GET", "/api/v1/hr/requisitions/{id}/candidates", ({ params, query }) => {
  const r = REQS.find((x) => uid(K_REQ, x.n) === params.id);
  const people: [string, string, string, string][] = [
    ["Brandon Whitaker", "INTERVIEW", "Referral", "b.whitaker@example.com"],
    ["Mei-Ling Cho", "SCREEN", "Job board", "meiling.cho@example.com"],
    ["Carlos Medina", "APPLIED", "Job board", "c.medina@example.com"],
  ];
  const items = r && r.status !== "DRAFT" ? people.map(([fullName, stage, source, email], i) => ({ id: uid(K_REQ, 100 + r.n * 10 + i), requisitionId: params.id, stage, source, fullName, email, erased: false, stageChangedAt: at(-2 - i) })) : [];
  return PAGE(items.filter((c) => !query.get("stage") || c.stage === query.get("stage")));
});
route("GET", "/api/v1/hr/requisitions/{id}", ({ params }) => {
  const r = REQS.find((x) => uid(K_REQ, x.n) === params.id);
  return r ? reqRow(r) : reply.problem(404, "HCM-REQ-NOT-FOUND", "No requisition with that id");
});

// Personnel actions.
const ACTIONS = [
  { n: 1, type: "HIRE", who: "W-0175", eff: -12, status: "APPLIED", payload: { legalEntityId: LEGAL_ENTITY.id, employmentType: "FULL_TIME", assignmentKind: "PRIMARY", positionId: pos(6), fteShare: "1.00" }, reason: "New assembler hired against requisition REQ-2026-015", by: RACHEL, sub: -20 },
  { n: 2, type: "COMP_CHANGE", who: "W-0164", eff: 14, status: "PENDING", payload: { amount: "30.40", currency: "USD" }, reason: "Six-month review, balancing certification earned", by: TOM, sub: -2 },
  { n: 3, type: "TRANSFER", who: "W-0133", eff: 21, status: "PENDING", payload: { positionId: pos(7), fteShare: "1.00" }, reason: "Moves to final test from incoming inspection", by: ELENA, sub: -1 },
  { n: 4, type: "COMP_CHANGE", who: "W-0141", eff: -45, status: "APPLIED", payload: { amount: "27.40", currency: "USD" }, reason: "Annual merit increase", by: TOM, sub: -60 },
  { n: 5, type: "TERMINATE", who: "W-0171", eff: -5, status: "WITHDRAWN", payload: { terminationReason: "RESIGNATION" }, reason: "Resignation rescinded", by: TOM, sub: -15 },
] as const;
const actionRow = (a: (typeof ACTIONS)[number]) => {
  const by = personById(a.by);
  return {
    id: uid(K_ACTION, a.n),
    actionType: a.type,
    workerId: person(a.who).id,
    effectiveDate: day(a.eff),
    status: a.status,
    payload: a.payload,
    reason: a.reason,
    submittedBy: by ? displayName(by) : "Rachel Osei",
    submittedAt: at(a.sub),
    ...(a.status === "APPLIED" ? { appliedAt: at(a.eff) } : {}),
    ...(a.status === "WITHDRAWN" ? { withdrawnAt: at(a.sub + 2) } : {}),
  };
};
route("GET", "/api/v1/hr/actions", ({ query }) => PAGE(ACTIONS.filter((a) => !query.get("status") || a.status === query.get("status")).map(actionRow)));
route("GET", "/api/v1/hr/actions/{id}", ({ params }) => {
  const a = ACTIONS.find((x) => uid(K_ACTION, x.n) === params.id);
  return a ? actionRow(a) : reply.problem(404, "HCM-ACT-NOT-FOUND", "No personnel action with that id");
});

// Compensation cycles.
const matrix = [
  { rating: 5, compaMin: "0.80", compaMax: "1.20", pctMin: "5.0", pctMax: "7.0" },
  { rating: 4, compaMin: "0.80", compaMax: "1.20", pctMin: "3.5", pctMax: "5.0" },
  { rating: 3, compaMin: "0.80", compaMax: "1.20", pctMin: "2.0", pctMax: "3.5" },
  { rating: 2, compaMin: "0.80", compaMax: "1.20", pctMin: "0.0", pctMax: "1.5" },
  { rating: 1, compaMin: "0.80", compaMax: "1.20", pctMin: "0.0", pctMax: "0.0" },
];
const CYCLES = [
  { id: uid(K_CYCLE, 1), name: "2027 annual merit", kind: "MERIT", effectiveDate: day(52), currency: "USD", allowOverBudget: false, guidelineMatrix: matrix, budgets: [{ departmentId: "Production", budgetAmount: dec(61000) }, { departmentId: "Quality", budgetAmount: dec(18500) }, { departmentId: "Operations", budgetAmount: dec(14200) }, { departmentId: "Maintenance", budgetAmount: dec(9800) }], status: "MANAGER_INPUT", createdBy: "Rachel Osei" },
  { id: uid(K_CYCLE, 2), name: "2026 annual merit", kind: "MERIT", effectiveDate: day(-290), currency: "USD", allowOverBudget: false, guidelineMatrix: matrix, budgets: [{ departmentId: "Production", budgetAmount: dec(54000) }, { departmentId: "Quality", budgetAmount: dec(16000) }], status: "APPLIED", createdBy: "Rachel Osei", approvedBy: "Marcus Lee", approvedAt: at(-310), appliedAt: at(-290) },
  { id: uid(K_CYCLE, 3), name: "2026 plant performance bonus", kind: "BONUS", effectiveDate: day(25), currency: "USD", allowOverBudget: false, guidelineMatrix: matrix.slice(1, 4), budgets: [{ departmentId: "Production", budgetAmount: dec(42000) }, { departmentId: "Maintenance", budgetAmount: dec(8000) }], status: "PLANNING", createdBy: "Rachel Osei" },
];
route("GET", "/api/v1/hr/compensation/cycles", () => PAGE(CYCLES));
route("GET", "/api/v1/hr/compensation/cycles/{id}", ({ params }) => CYCLES.find((c) => c.id === params.id) ?? reply.problem(404, "HCM-CMP-NOT-FOUND", "No compensation cycle with that id"));
