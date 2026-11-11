// Incident register and loaned items (tools, PPE and devices out on loan) for the demo.
import { LEGAL_ENTITY, PLANT } from "../../company";
import { reply, route } from "../../backend";
import { at, day, uid } from "../../store";
import { person } from "./people";

const K_INCIDENT = 715;
const K_LOAN = 716;
const PAGE = (items: unknown[]) => ({ items });

interface Incident {
  n: number;
  case: number;
  who?: string;
  type: string;
  occurred: number;
  location: string;
  parts: string[];
  treatment: boolean;
  away: number;
  restricted: number;
  recordable: boolean;
  outcome: string;
  privacy?: boolean;
  status: "REPORTED" | "UNDER_REVIEW" | "CLOSED";
  description: string;
  rootCause?: string;
  action?: string;
}
const INCIDENTS: Incident[] = [
  { n: 1, case: 9, who: "W-0158", type: "INJURY", occurred: -4, location: "Paint booth 2", parts: ["EYE"], treatment: true, away: 0, restricted: 0, recordable: true, outcome: "OTHER_RECORDABLE", status: "UNDER_REVIEW", description: "Solvent splash to the eye while changing a spray filter. Flushed at the eyewash station and sent to occupational health.", rootCause: "Face shield was not worn for the filter change." },
  { n: 2, case: 8, who: "W-0112", type: "INJURY", occurred: -19, location: "Machining cell 3", parts: ["HAND", "FINGER"], treatment: true, away: 3, restricted: 5, recordable: true, outcome: "DAYS_AWAY", status: "UNDER_REVIEW", description: "Laceration to the left hand from a sharp casting edge while unloading the vertical mill.", rootCause: "Burrs left on the casting after the rough cut.", action: "Add a deburr step to the routing and issue cut-resistant gloves." },
  { n: 3, case: 7, who: "W-0127", type: "INJURY", occurred: -41, location: "Assembly line 1", parts: ["BACK"], treatment: false, away: 0, restricted: 0, recordable: false, outcome: "NONE", status: "CLOSED", description: "Back strain reported after lifting a volute casing. First aid only.", action: "Lift assist installed at station 4." },
  { n: 4, case: 6, who: "W-0164", type: "HEARING_LOSS", occurred: -75, location: "Balancing bay", parts: ["EAR"], treatment: true, away: 0, restricted: 0, recordable: true, outcome: "OTHER_RECORDABLE", status: "CLOSED", description: "Annual audiogram showed a standard threshold shift.", action: "Hearing protection upgraded and refitted." },
  { n: 5, case: 5, type: "INJURY", occurred: -120, location: "Shipping dock", parts: ["KNEE"], treatment: true, away: 0, restricted: 12, recordable: true, outcome: "RESTRICTED", privacy: true, status: "CLOSED", description: "Privacy case." },
];
const incidentRow = (i: Incident) => {
  const p = i.who ? person(i.who) : undefined;
  return {
    id: uid(K_INCIDENT, i.n),
    caseNo: i.case,
    ...(p && !i.privacy ? { workerId: p.id, workerName: p.name } : {}),
    legalEntityId: LEGAL_ENTITY.id,
    siteId: PLANT.id,
    incidentType: i.type,
    occurredAt: at(i.occurred),
    reportedAt: at(i.occurred + 0.1),
    location: i.location,
    bodyParts: i.parts,
    death: false,
    lossOfConsciousness: false,
    medicalTreatment: i.treatment,
    daysAway: i.away,
    daysRestricted: i.restricted,
    oshaRecordable: i.recordable,
    outcome: i.outcome,
    privacyCase: i.privacy ?? false,
    status: i.status,
    reportedBy: "Nadia Petrov",
    ...(i.status === "CLOSED" ? { reviewedBy: "Nadia Petrov", closedBy: "Jordan Avery", closedAt: at(i.occurred + 14) } : {}),
    details: { description: i.description, witnesses: i.privacy ? [] : ["Tom Brennan"], treatments: i.treatment ? ["First aid on site", "Referred to occupational health"] : ["First aid on site"], ...(i.rootCause ? { rootCause: i.rootCause } : {}), ...(i.action ? { correctiveAction: i.action } : {}) },
  };
};
route("GET", "/api/v1/hr/incidents", ({ query }) => PAGE(INCIDENTS.filter((i) => !query.get("status") || i.status === query.get("status")).map(incidentRow)));
route("GET", "/api/v1/hr/incidents/{id}", ({ params }) => {
  const i = INCIDENTS.find((x) => uid(K_INCIDENT, x.n) === params.id);
  return i ? incidentRow(i) : reply.problem(404, "HCM-INC-NOT-FOUND", "No incident with that id");
});

// Loaned items still out.
const LOANS = [
  { n: 1, who: "W-0158", type: "TOOL", item: "Torque wrench, 20 to 100 ft-lb", serial: "TW-4471", issued: -64, due: -4, cond: "Calibrated" },
  { n: 2, who: "W-0122", type: "DEVICE", item: "Handheld vibration analyzer", serial: "VA-2208", issued: -30, due: 15, cond: "Good" },
  { n: 3, who: "W-0112", type: "TOOL", item: "Digital caliper, 0 to 8 in", serial: "DC-0932", issued: -12, due: 18, cond: "Good" },
  { n: 4, who: "W-0175", type: "PPE", item: "Safety glasses and hearing protection kit", serial: undefined, issued: -12, due: undefined, cond: "New" },
  { n: 5, who: "W-0118", type: "DEVICE", item: "Laptop, quality lab", serial: "LT-7715", issued: -300, due: 60, cond: "Good" },
  { n: 6, who: "W-0164", type: "TOOL", item: "Dial indicator set", serial: "DI-3320", issued: -9, due: 5, cond: "Good" },
];
const loanRow = (l: (typeof LOANS)[number]) => ({
  id: uid(K_LOAN, l.n),
  workerId: person(l.who).id,
  loanType: l.type,
  itemDescription: l.item,
  ...(l.serial ? { serial: l.serial } : {}),
  issuedOn: day(l.issued),
  ...(l.due !== undefined ? { dueOn: day(l.due) } : {}),
  conditionOut: l.cond,
  legalEntityId: LEGAL_ENTITY.id,
  status: "OUT",
});
route("GET", "/api/v1/hr/loaned-items/outstanding", ({ query }) => {
  const dueBy = query.get("dueBy");
  const rows = LOANS.filter((l) => !dueBy || (l.due !== undefined && day(l.due) <= dueBy));
  return PAGE([...rows].sort((a, b) => (a.due ?? 9999) - (b.due ?? 9999)).map(loanRow));
});
route("GET", "/api/v1/hr/workers/{id}/loaned-items", ({ params }) => PAGE(LOANS.filter((l) => person(l.who).id === params.id).map(loanRow)));
