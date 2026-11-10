// Segregation of duties (/api/v1/sod) for the demo: the rule set and the conflicts it finds in the staff's role
// assignments (finance/people.ts), with mitigations and accepted risks. A presenter can mitigate an open conflict or
// accept the risk of a low or medium one.
import type { Conflict, ConflictDetail, Mitigation, Rule } from "../../../modules/compliance/api/client";
import { reply, route } from "../../backend";
import { at, table, uid } from "../../store";
import { byUsername, ME } from "./people";

const BASE = "/api/v1/sod";
const K_CONFLICT = 625;
const K_MITIGATION = 626;

const rule = (ruleId: string, name: string, dutyA: string, dutyB: string, severity: Rule["severity"], riskId: string, description: string, maxMitigationDays = 180): Rule => ({
  ruleId,
  name,
  dutyA,
  dutyB,
  severity,
  riskId,
  description,
  mitigationAllowed: true,
  maxMitigationDays,
  status: "ACTIVE",
  version: 1,
});

const RULES: Rule[] = [
  rule("SOD-P2P-01", "Create purchase orders and post vendor invoices", "procurement.purchase-order.maintain", "receiving.vendor-invoice.post", "HIGH", "R-FIN-03", "One person could order goods and approve the invoice that pays for them."),
  rule("SOD-P2P-02", "Maintain vendors and approve payments", "party.vendor.maintain", "payment-proposal.approve", "HIGH", "R-FIN-01", "One person could add a vendor or change its bank account and release payments to it."),
  rule("SOD-P2P-03", "Post vendor invoices and approve payments", "receiving.vendor-invoice.post", "payment-proposal.approve", "HIGH", "R-FIN-02", "One person could enter an invoice and release its payment.", 90),
  rule("SOD-GL-01", "Enter and approve journals", "ledger.journal.write", "ledger.journal.approve", "MEDIUM", "R-FIN-07", "One person could enter a manual journal and approve it."),
  rule("SOD-O2C-01", "Post invoices and post customer payments", "billing.invoice.post", "receivables.payment.post", "MEDIUM", "R-FIN-05", "One person could bill a customer and apply the payment, hiding a diverted receipt."),
  rule("SOD-SEC-01", "Maintain users and assign roles", "identity.user.maintain", "identity.security-role.maintain", "MEDIUM", "R-IT-02", "One person could create an account and give it any role."),
  rule("SOD-HR-01", "Set pay and run payroll", "hr.compensation.admin", "payroll.run.post", "HIGH", "R-HR-01", "One person could change a salary and pay it out."),
  rule("SOD-INV-01", "Post inventory journals and supervise warehouse work", "inventory.journal.post", "warehouse.work.supervise", "LOW", "R-OPS-04", "One person could move stock and adjust the books to match."),
  rule("SOD-COST-01", "Change standard costs and post journals", "costing.version.write", "ledger.journal.post", "MEDIUM", "R-FIN-09", "One person could change a standard cost and post the revaluation."),
];

const ruleById = (id: string) => RULES.find((r) => r.ruleId === id)!;

interface Stored extends ConflictDetail {
  id: string;
}

let seq = 0;
function conflict(username: string, ruleId: string, state: Conflict["state"], detectedDaysAgo: number, extra: Partial<Stored> = {}): Stored {
  const r = ruleById(ruleId);
  const id = uid(K_CONFLICT, ++seq);
  return {
    id,
    conflictId: id,
    userId: byUsername(username).id,
    ruleId: r.ruleId,
    riskId: r.riskId,
    severity: r.severity,
    dutyA: r.dutyA,
    dutyB: r.dutyB,
    rolesA: [r.dutyA],
    rolesB: [r.dutyB],
    state,
    detectedAt: at(-detectedDaysAgo),
    updatedAt: at(-detectedDaysAgo),
    version: 1,
    mitigations: [],
    ...extra,
  };
}

function mitigation(conflictId: string, n: number, controlRef: string, reason: string, approver: string, approvedDaysAgo: number, expiresInDays: number): Mitigation {
  return { mitigationId: uid(K_MITIGATION, n), conflictId, controlRef, reason, approverId: byUsername(approver).id, approvedAt: at(-approvedDaysAgo), expiresAt: at(expiresInDays) };
}

const seeded: Stored[] = [
  conflict("kevin.brandt", "SOD-P2P-02", "OPEN", 9),
  conflict("ana.ortiz", "SOD-P2P-03", "OPEN", 31),
  conflict("dana.kim", "SOD-GL-01", "OPEN", 4),
  conflict("maya.chen", "SOD-O2C-01", "OPEN", 17),
  conflict("omar.siddiqui", "SOD-INV-01", "OPEN", 52),
  conflict("jordan.avery", "SOD-GL-01", "MITIGATED", 210),
  conflict("rita.alvarez", "SOD-HR-01", "MITIGATED", 140),
  conflict("tom.becker", "SOD-SEC-01", "ACCEPTED_RISK", 300, { decidedBy: "marcus.lee", decisionReason: "Two-person IT team; quarterly access review by the CFO covers account creation.", updatedAt: at(-290), version: 2 }),
  conflict("brian.kessler", "SOD-COST-01", "RESOLVED", 420, { resolvedAt: at(-120), updatedAt: at(-120), version: 2 }),
];
for (const c of seeded) {
  if (c.userId === ME.id) {
    const m = mitigation(c.id, 1, "CTRL-GL-04 Monthly review of manual journals by the CFO", "The controller approves batches others enter; the CFO reviews every journal the controller posts.", "marcus.lee", 200, 165);
    Object.assign(c, { mitigations: [m], mitigationId: m.mitigationId, expiresAt: m.expiresAt, decidedBy: "marcus.lee", updatedAt: m.approvedAt, version: 2 });
  }
  if (c.ruleId === "SOD-HR-01") {
    const m = mitigation(c.id, 2, "CTRL-HR-02 Payroll register sign-off by the CFO", "Every pay run is released only after the CFO signs the register and the change report.", "marcus.lee", 130, 12);
    Object.assign(c, { mitigations: [m], mitigationId: m.mitigationId, expiresAt: m.expiresAt, decidedBy: "marcus.lee", updatedAt: m.approvedAt, version: 2 });
  }
}

const conflicts = table<Stored>(seeded);
const plain = ({ id: _id, mitigations: _m, ...c }: Stored): Conflict => c;
const detail = ({ id: _id, ...c }: Stored): ConflictDetail => c;

const SEVERITY_ORDER = { HIGH: 0, MEDIUM: 1, LOW: 2 } as const;

route("GET", `${BASE}/rules`, () => ({ items: RULES }));
route("GET", `${BASE}/conflicts`, ({ query }) => {
  const state = query.get("state");
  const severity = query.get("severity");
  const userId = query.get("userId");
  const items = conflicts
    .all()
    .filter((c) => (!state || c.state === state) && (!severity || c.severity === severity) && (!userId || c.userId === userId))
    .sort((a, b) => SEVERITY_ORDER[a.severity] - SEVERITY_ORDER[b.severity] || a.detectedAt.localeCompare(b.detectedAt))
    .map(plain);
  return { items };
});
route("GET", `${BASE}/conflicts/{id}`, ({ params }) => {
  const c = conflicts.get(params.id);
  return c ? reply.versioned(detail(c), c.version) : reply.problem(404, "SOD-0404", "conflict not found");
});

function guard(id: string, headers: Headers): Stored | ReturnType<typeof reply.problem> {
  const c = conflicts.get(id);
  if (!c) return reply.problem(404, "SOD-0404", "conflict not found");
  const v = headers.get("If-Match");
  if (v !== null && v !== `"${c.version}"`) return reply.problem(412, "SOD-0412", "the conflict changed since you opened it");
  if (c.state !== "OPEN") return reply.problem(409, "SOD-0409", "only an open conflict can be decided");
  return c;
}

route("POST", `${BASE}/conflicts/{id}/mitigations`, ({ params, headers, body }) => {
  const c = guard(params.id, headers);
  if (!("conflictId" in c)) return c;
  const input = (body ?? {}) as { controlRef?: string; reason?: string; expiresAt?: string };
  const m: Mitigation = { mitigationId: crypto.randomUUID(), conflictId: c.conflictId, controlRef: input.controlRef ?? "", reason: input.reason ?? "", approverId: ME.id, approvedAt: at(0), expiresAt: input.expiresAt ?? at(90) };
  const next = conflicts.update(c.id, { state: "MITIGATED", mitigationId: m.mitigationId, expiresAt: m.expiresAt, decidedBy: ME.username, mitigations: [...c.mitigations, m], updatedAt: at(0), version: c.version + 1 })!;
  return reply.versioned(detail(next), next.version);
});
route("POST", `${BASE}/conflicts/{id}/accept-risk`, ({ params, headers, body }) => {
  const c = guard(params.id, headers);
  if (!("conflictId" in c)) return c;
  if (c.severity === "HIGH") return reply.problem(422, "SOD-0422", "a high-severity conflict cannot be accepted; mitigate it or remove a role");
  const next = conflicts.update(c.id, { state: "ACCEPTED_RISK", decidedBy: ME.username, decisionReason: ((body ?? {}) as { reason?: string }).reason ?? "", updatedAt: at(0), version: c.version + 1 })!;
  return reply.versioned(detail(next), next.version);
});
