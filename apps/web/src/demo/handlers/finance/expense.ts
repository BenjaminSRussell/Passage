// Expense reports (/api/v1/expense/reports) for the demo: trips the plant crew make to customer sites, training and
// supplier audits, each with lines checked against the service's default policy rules (receipt from 25.00, no
// alcohol, meals over 75.00 a day without attendees need a justification). Drafts accept new lines, removal and
// submit, which re-runs the checks.
import type { ExpenseReport, PolicyResult, ReportLine } from "../../../modules/expense/api/client";
import { CUSTOMERS, LEGAL_ENTITY, WORKERS } from "../../company";
import { reply, route } from "../../backend";
import { at, day, newId, table, uid } from "../../store";

const BASE = "/api/v1/expense/reports";
const K_REPORT = 635;
const K_LINE = 636;

const cents = (s: string): number => Math.round(Number(s) * 100);
const money = (c: number): string => (c / 100).toFixed(2);

type LineInput = Omit<ReportLine, "id" | "lineNo" | "policyResults">;

/** The service's default rules (services/expense/internal/domain/policy.go DefaultRules), per line. */
function check(line: LineInput & { id: string }, sameDayMeals: number): PolicyResult[] {
  const out: PolicyResult[] = [];
  if (cents(line.amount) >= 2500 && !line.hasReceipt) {
    out.push({ lineId: line.id, ruleId: "EXP-RCPT-025", ruleVersion: 1, outcome: "REQUIRE_RECEIPT", code: "EXP-REC-001", message: "Receipt required for lines of 25.00 or more", satisfied: false });
  }
  if (line.category === "ALCOHOL") {
    out.push({ lineId: line.id, ruleId: "EXP-ALC-001", ruleVersion: 1, outcome: "BLOCK", code: "EXP-POL-001", message: "Alcohol is not reimbursable", satisfied: false });
  }
  if (line.category === "MEALS" && line.attendees === 0 && sameDayMeals > 7500) {
    out.push({ lineId: line.id, ruleId: "EXP-MEAL-075", ruleVersion: 3, outcome: "REQUIRE_JUSTIFICATION", code: "EXP-POL-001", message: "Meals over 75.00 a day without attendees need a justification", satisfied: line.justification.trim() !== "" });
  }
  return out;
}

function withChecks(r: Omit<ExpenseReport, "total" | "reimbursable" | "companyPaid" | "submittable">): ExpenseReport {
  const lines = r.lines.map((l, i) => {
    const sameDayMeals = r.lines.filter((x) => x.category === "MEALS" && x.attendees === 0 && x.txnDate === l.txnDate).reduce((s, x) => s + cents(x.amount), 0);
    return { ...l, lineNo: i + 1, policyResults: check(l, sameDayMeals) };
  });
  const total = lines.reduce((s, l) => s + cents(l.amount), 0);
  const company = lines.filter((l) => l.paidBy === "COMPANY_CARD").reduce((s, l) => s + cents(l.amount), 0);
  const open = lines.some((l) => l.policyResults.some((p) => !p.satisfied));
  return { ...r, lines, total: money(total), companyPaid: money(company), reimbursable: money(total - company), submittable: r.status === "DRAFT" && lines.length > 0 && !open };
}

type Seed = [category: string, daysAgo: number, amount: number, merchant: string, extra?: Partial<LineInput>];

let lineSeq = 1;
function seedLines(rows: Seed[]): ReportLine[] {
  return rows.map(([category, daysAgo, amount, merchant, extra]) => ({
    id: uid(K_LINE, lineSeq++),
    lineNo: 0,
    category,
    txnDate: day(-daysAgo),
    amount: amount.toFixed(2),
    merchant,
    attendees: 0,
    hasReceipt: true,
    justification: "",
    paidBy: "EMPLOYEE" as const,
    policyResults: [],
    ...extra,
  }));
}

const worker = (number: string) => {
  const w = WORKERS.find((x) => x.number === number);
  if (!w) throw new Error(`demo worker ${number} not in company.ts`);
  return w.id;
};
const customer = (number: string) => CUSTOMERS.find((c) => c.number === number)!;

function report(n: number, workerNumber: string, purpose: string, status: ExpenseReport["status"], createdDaysAgo: number, lines: Seed[], submittedDaysAgo?: number): ExpenseReport {
  return withChecks({
    id: uid(K_REPORT, n),
    legalEntityId: LEGAL_ENTITY.id,
    workerId: worker(workerNumber),
    purpose,
    currency: "USD",
    status,
    version: status === "DRAFT" ? 2 : 4,
    createdAt: at(-createdDaysAgo),
    submittedAt: submittedDaysAgo === undefined ? null : at(-submittedDaysAgo),
    lines: seedLines(lines),
  });
}

const coastal = customer("C-10063");
const bluewater = customer("C-10021");
const harlan = customer("C-10048");

const reports = table<ExpenseReport>([
  report(1, "W-0133", `Commissioning CP-400 pumps at ${coastal.name}, ${coastal.city}`, "SUBMITTED", 6, [
    ["AIRFARE", 12, 486.2, "Gulf Coast Airways", { paidBy: "COMPANY_CARD" }],
    ["LODGING", 9, 387.0, "Bayfront Suites Corpus Christi"],
    ["TRAVEL", 9, 214.55, "Coastal Car Rental"],
    ["MEALS", 10, 58.4, "Water Street Oyster Bar"],
    ["MEALS", 9, 142.75, "Harbor Grill", { attendees: 3, justification: "Dinner with the plant operators after the performance test" }],
    ["MEALS", 8, 31.2, "Airport Market"],
  ], 5),
  report(2, "W-0164", "Dynamic balancing certification course, Milwaukee", "APPROVED", 21, [
    ["TRAVEL", 24, 168.9, "Interstate mileage and tolls"],
    ["LODGING", 23, 612.0, "Lakeshore Conference Hotel", { paidBy: "COMPANY_CARD" }],
    ["MEALS", 23, 46.15, "Third Ward Deli"],
    ["MEALS", 22, 52.3, "Riverwalk Bistro"],
  ], 19),
  report(3, "W-0127", `Site survey for the lift station retrofit, ${bluewater.name}, ${bluewater.city}`, "DRAFT", 2, [
    ["MILEAGE", 4, 289.8, "Personal car, Cedar Rapids to Duluth and back (420 mi)"],
    ["LODGING", 4, 149.0, "Canal Park Lodge", { hasReceipt: false }],
    ["MEALS", 4, 88.6, "Northern Waters Smokehaus"],
  ]),
  report(4, "W-0112", "Precision machining trade show, Chicago", "POSTED", 48, [
    ["TRAVEL", 52, 96.0, "Amtrak Hiawatha"],
    ["LODGING", 51, 534.0, "McCormick Place Hotel", { paidBy: "COMPANY_CARD" }],
    ["MEALS", 51, 39.5, "Loop Sandwich Co."],
  ], 46),
  report(5, "W-0141", `Pump installation support, ${harlan.name}, ${harlan.city}`, "PAID", 63, [
    ["MILEAGE", 66, 213.9, "Personal car, Cedar Rapids to Harlan and back (310 mi)"],
    ["MEALS", 66, 24.85, "Main Street Diner"],
  ], 61),
  report(6, "W-0158", "Coatings supplier audit, Riverbend Steel Service, Davenport", "REJECTED", 15, [
    ["MILEAGE", 17, 109.02, "Personal car, Cedar Rapids to Davenport and back (158 mi)"],
    ["MEALS", 17, 61.2, "Front Street Brewery", { attendees: 2 }],
    ["ALCOHOL", 17, 34.0, "Front Street Brewery", { attendees: 2 }],
  ], 14),
]);

// --- routes -------------------------------------------------------------------------------------

const notFound = () => reply.problem(404, "EXP-0404", "expense report not found");

function guard(id: string, headers: Headers): ExpenseReport | ReturnType<typeof notFound> {
  const r = reports.get(id);
  if (!r) return notFound();
  const v = headers.get("If-Match");
  if (v !== null && v !== `"${r.version}"`) return reply.problem(412, "EXP-0412", "the report changed since you opened it");
  return r;
}

route("GET", BASE, ({ query }) => {
  const status = query.get("status");
  const workerId = query.get("workerId");
  const items = reports
    .all()
    .filter((r) => (!status || r.status === status) && (!workerId || r.workerId === workerId))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items, nextCursor: "" };
});

route("POST", BASE, ({ body }) => {
  const input = (body ?? {}) as { legalEntityId?: string; workerId?: string; purpose?: string; currency?: string };
  if (!WORKERS.some((w) => w.id === input.workerId)) return reply.problem(422, "EXP-0422", "no worker with that id", { workerId: "No worker has this id." });
  return reply.created(
    reports.insert(withChecks({ id: newId(), legalEntityId: input.legalEntityId ?? LEGAL_ENTITY.id, workerId: input.workerId!, purpose: input.purpose ?? "", currency: input.currency ?? "USD", status: "DRAFT", version: 1, createdAt: at(0), submittedAt: null, lines: [] })),
  );
});

route("GET", `${BASE}/{id}`, ({ params }) => reports.get(params.id) ?? notFound());

route("POST", `${BASE}/{id}/lines`, ({ params, body }) => {
  const r = reports.get(params.id);
  if (!r) return notFound();
  if (r.status !== "DRAFT") return reply.problem(409, "EXP-0409", "only a draft report takes new lines");
  const input = (body ?? {}) as LineInput;
  const line: ReportLine = { id: newId(), lineNo: 0, policyResults: [], ...input, amount: Number(input.amount).toFixed(2) };
  return reply.created(reports.update(r.id, withChecks({ ...r, lines: [...r.lines, line], version: r.version + 1 })));
});

route("DELETE", `${BASE}/{id}/lines/{lineId}`, ({ params, headers }) => {
  const r = guard(params.id, headers);
  if (!("lines" in r)) return r;
  return reports.update(r.id, withChecks({ ...r, lines: r.lines.filter((l) => l.id !== params.lineId), version: r.version + 1 }));
});

route("POST", `${BASE}/{id}/submit`, ({ params, headers }) => {
  const r = guard(params.id, headers);
  if (!("lines" in r)) return r;
  if (!r.submittable) return reply.problem(422, "EXP-POL-001", "the report has open policy results; resolve them before submitting");
  return reports.update(r.id, { ...r, status: "SUBMITTED", submittedAt: at(0), submittable: false, version: r.version + 1 });
});
