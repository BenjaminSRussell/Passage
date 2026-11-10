// Skills, certifications, courses and questionnaires for the demo.
import { reply, route } from "../../backend";
import { at, day, uid } from "../../store";
import { person } from "./people";

const K_SKILL = 710;
const K_COURSE = 711;
const K_SESSION = 712;
const K_CERT = 713;
const K_QUESTIONNAIRE = 714;
const PAGE = (items: unknown[]) => ({ items });

// Skills.
const SKILLS = [
  { n: 1, code: "CNC-MILL", name: "CNC milling", scale: 5, added: -900 },
  { n: 2, code: "CNC-LATHE", name: "CNC turning", scale: 5, added: -900 },
  { n: 3, code: "WELD-TIG", name: "TIG welding", scale: 5, added: -800 },
  { n: 4, code: "ROTOR-BAL", name: "Impeller and rotor balancing", scale: 5, added: -700 },
  { n: 5, code: "HYDRO-TEST", name: "Hydrostatic pressure testing", scale: 5, added: -650 },
  { n: 6, code: "PUMP-ASSY", name: "Centrifugal pump assembly", scale: 5, added: -900 },
  { n: 7, code: "COATING", name: "Industrial coating and painting", scale: 4, added: -500 },
  { n: 8, code: "GDT", name: "Geometric dimensioning and tolerancing", scale: 5, added: -400 },
  { n: 9, code: "LOTO", name: "Lockout and tagout", scale: 3, added: -900 },
];
const skillRow = (s: (typeof SKILLS)[number]) => ({ id: uid(K_SKILL, s.n), code: s.code, name: s.name, scaleMax: s.scale, createdAt: at(s.added) });
route("GET", "/api/v1/hr/skills", () => PAGE(SKILLS.map(skillRow)));

// Courses and sessions.
const OUTCOME = (code: string, rating: number) => {
  const s = SKILLS.find((x) => x.code === code)!;
  return { skillId: uid(K_SKILL, s.n), skillCode: s.code, skillName: s.name, rating };
};
const COURSES = [
  { n: 1, code: "SAF-101", name: "Lockout and tagout, authorized employee", group: "Safety", outcomes: [OUTCOME("LOTO", 3)], cert: ["LOTO-AUTH", "LOTO authorized employee"], months: 12, approval: false },
  { n: 2, code: "QA-210", name: "GD&T for pump casings", group: "Quality", outcomes: [OUTCOME("GDT", 3)], cert: undefined, months: 0, approval: false },
  { n: 3, code: "WLD-120", name: "TIG welding, stainless fabrication", group: "Fabrication", outcomes: [OUTCOME("WELD-TIG", 3)], cert: ["WELD-TIG-SS", "Stainless TIG welder"], months: 24, approval: true },
  { n: 4, code: "OPS-150", name: "Hydrostatic test procedure and safety", group: "Production", outcomes: [OUTCOME("HYDRO-TEST", 3)], cert: ["HYDRO-OP", "Hydro test operator"], months: 36, approval: false },
  { n: 5, code: "OPS-230", name: "Dynamic balancing of impellers", group: "Production", outcomes: [OUTCOME("ROTOR-BAL", 4)], cert: undefined, months: 0, approval: true },
];
const courseRow = (c: (typeof COURSES)[number]) => ({
  id: uid(K_COURSE, c.n),
  code: c.code,
  name: c.name,
  courseGroup: c.group,
  skillOutcomes: c.outcomes,
  ...(c.cert ? { certificationCode: c.cert[0], certificationName: c.cert[1] } : {}),
  validityMonths: c.months,
  requiresManagerApproval: c.approval,
  createdAt: at(-400),
});
const sessionRow = (c: (typeof COURSES)[number], i: number, startDays: number, capacity: number, confirmed: number) => ({
  id: uid(K_SESSION, c.n * 10 + i),
  courseId: uid(K_COURSE, c.n),
  courseCode: c.code,
  courseName: c.name,
  startsAt: at(startDays),
  endsAt: at(startDays + 0.3),
  location: "Cedar Rapids plant, training room B",
  instructorWorkerId: person("W-0150").id,
  capacity,
  waitlistCapacity: 5,
  costPerAttendee: "185.00",
  currency: "USD",
  confirmed,
  waitlisted: 0,
  requested: 1,
  seatsLeft: capacity - confirmed,
  createdAt: at(-30),
});
const sessionsOf = (c: (typeof COURSES)[number]) => [sessionRow(c, 1, 9 + c.n, 12, 7), sessionRow(c, 2, 40 + c.n, 12, 2)];
route("GET", "/api/v1/hr/courses", () => PAGE(COURSES.map(courseRow)));
route("GET", "/api/v1/hr/courses/sessions/{id}/registrations", ({ params }) => {
  const names = ["W-0112", "W-0127", "W-0141", "W-0158"];
  return PAGE(names.map((n, i) => ({ id: uid(K_SESSION, 900 + i), sessionId: params.id, workerId: person(n).id, personnelNumber: n, legalName: person(n).name, status: i < 3 ? "CONFIRMED" : "REQUESTED", queuedAt: at(-5 + i) })));
});
route("GET", "/api/v1/hr/courses/sessions/{id}", ({ params }) => {
  const found = COURSES.flatMap(sessionsOf).find((s) => s.id === params.id);
  return found ?? reply.problem(404, "HCM-CRS-NOT-FOUND", "No session with that id");
});
route("GET", "/api/v1/hr/courses/{id}/sessions", ({ params }) => {
  const c = COURSES.find((x) => uid(K_COURSE, x.n) === params.id);
  return PAGE(c ? sessionsOf(c) : []);
});
route("GET", "/api/v1/hr/courses/{id}", ({ params }) => {
  const c = COURSES.find((x) => uid(K_COURSE, x.n) === params.id);
  return c ? courseRow(c) : reply.problem(404, "HCM-CRS-NOT-FOUND", "No course with that id");
});

// Certificates expiring soon.
const CERTS = [
  { n: 1, who: "W-0112", code: "LOTO-AUTH", name: "LOTO authorized employee", issuer: "Cedar Ridge EHS", left: 6 },
  { n: 2, who: "W-0127", code: "LOTO-AUTH", name: "LOTO authorized employee", issuer: "Cedar Ridge EHS", left: 19 },
  { n: 3, who: "W-0133", code: "HYDRO-OP", name: "Hydro test operator", issuer: "Cedar Ridge Quality", left: 27 },
  { n: 4, who: "W-0122", code: "WELD-TIG-SS", name: "Stainless TIG welder", issuer: "American Welding Society", left: 41 },
  { n: 5, who: "W-0158", code: "FORKLIFT", name: "Powered industrial truck operator", issuer: "Cedar Ridge EHS", left: 52 },
  { n: 6, who: "W-0164", code: "LOTO-AUTH", name: "LOTO authorized employee", issuer: "Cedar Ridge EHS", left: 88 },
];
route("GET", "/api/v1/hr/certifications/expiring", ({ query }) => {
  const within = Number(query.get("withinDays") ?? "60");
  const asOf = query.get("asOf") ?? day(0);
  return {
    asOf,
    withinDays: within,
    items: CERTS.filter((c) => c.left <= within).map((c) => {
      const p = person(c.who);
      return { id: uid(K_CERT, c.n), workerId: p.id, code: c.code, name: c.name, issuer: c.issuer, issuedOn: day(c.left - 365), expiresOn: day(c.left), valid: true, personnelNumber: p.number, legalName: p.name, daysLeft: c.left };
    }),
  };
});

// Questionnaires.
const QUESTIONNAIRES = [
  {
    id: uid(K_QUESTIONNAIRE, 1),
    code: "SAFETY-PULSE",
    name: "Quarterly shop floor safety pulse",
    anonymous: true,
    questions: [
      { code: "Q1", text: "I feel safe doing my job on the floor.", type: "SCALE", scaleMax: 5, weight: "1.0", mandatory: true, resultGroup: "SAFETY" },
      { code: "Q2", text: "Near misses are reported and acted on quickly.", type: "SCALE", scaleMax: 5, weight: "1.0", mandatory: true, resultGroup: "SAFETY" },
      { code: "Q3", text: "Which area needs the most attention?", type: "SINGLE", options: [{ value: "guarding", label: "Machine guarding" }, { value: "forklift", label: "Forklift traffic" }, { value: "ppe", label: "PPE supply" }], weight: "1.0", mandatory: false },
      { code: "Q4", text: "What one change would make your area safer?", type: "TEXT", weight: "1.0", mandatory: false },
    ],
  },
  {
    id: uid(K_QUESTIONNAIRE, 2),
    code: "ONBOARD-30",
    name: "New hire 30-day check-in",
    anonymous: false,
    questions: [
      { code: "Q1", text: "My training prepared me for the work I do.", type: "SCALE", scaleMax: 5, weight: "1.0", mandatory: true, resultGroup: "TRAINING" },
      { code: "Q2", text: "My supervisor has been available when I had questions.", type: "SCALE", scaleMax: 5, weight: "1.0", mandatory: true, resultGroup: "SUPPORT" },
      { code: "Q3", text: "How many hours of training have you completed?", type: "NUMERIC", weight: "1.0", mandatory: false },
    ],
  },
  {
    id: uid(K_QUESTIONNAIRE, 3),
    code: "ENGAGE-2026",
    name: "Annual engagement survey",
    anonymous: true,
    questions: [
      { code: "Q1", text: "I would recommend Cedar Ridge Pumps as a place to work.", type: "SCALE", scaleMax: 10, weight: "1.0", mandatory: true, resultGroup: "ENGAGEMENT" },
      { code: "Q2", text: "I see a path to grow my career here.", type: "SCALE", scaleMax: 5, weight: "1.0", mandatory: true, resultGroup: "GROWTH" },
    ],
  },
];
route("GET", "/api/v1/hr/questionnaires", () => PAGE(QUESTIONNAIRES));
route("GET", "/api/v1/hr/questionnaires/schedules/open", () => PAGE([]));
route("GET", "/api/v1/hr/questionnaires/{id}", ({ params }) => QUESTIONNAIRES.find((q) => q.id === params.id) ?? reply.problem(404, "HCM-QST-NOT-FOUND", "No questionnaire with that id"));
