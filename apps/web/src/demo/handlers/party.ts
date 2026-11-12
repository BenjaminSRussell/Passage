// Party credit-limit approvals (/party/pending-changes): increases above the approval threshold, held until someone
// approves or rejects them. Fictional Cedar Ridge Pumps customers.
import { CUSTOMERS, LEGAL_ENTITY } from "../company";
import { reply, route } from "../backend";
import { at, uid } from "../store";

const K_PENDING = 721;

type Change = {
  id: string;
  customerId: string;
  legalEntityId: string;
  field: "credit_limit";
  currentValue: string | null;
  requestedValue: string;
  status: "pending" | "approved" | "rejected";
  requestedBy: string;
  requestedAt: string;
  decidedBy?: string;
  decidedAt?: string;
  reason?: string;
};

const CHANGES: Change[] = [
  { n: 1, customer: 2, current: 400000, requested: 550000, by: "maria.okafor", days: -1 },
  { n: 2, customer: 3, current: 600000, requested: 750000, by: "dev.raman", days: -3 },
].map((c) => ({
  id: uid(K_PENDING, c.n),
  customerId: CUSTOMERS[c.customer].id,
  legalEntityId: LEGAL_ENTITY.id,
  field: "credit_limit",
  currentValue: String(c.current),
  requestedValue: String(c.requested),
  status: "pending",
  requestedBy: c.by,
  requestedAt: at(c.days),
}));

route("GET", "/api/v1/party/pending-changes", () => ({ items: CHANGES.filter((c) => c.status === "pending") }));
route("GET", "/api/v1/party/pending-changes/{id}", ({ params }) => CHANGES.find((c) => c.id === params.id) ?? reply.problem(404, "PTY-CHANGE-NOT-FOUND", "No pending change with that id"));
for (const action of ["approve", "reject"] as const) {
  route("POST", `/api/v1/party/pending-changes/{id}:${action}`, ({ params, body }) => {
    const change = CHANGES.find((c) => c.id === params.id);
    if (!change) return reply.problem(404, "PTY-CHANGE-NOT-FOUND", "No pending change with that id");
    if (change.status !== "pending") return reply.problem(409, "PTY-CHANGE-DECIDED", "This change was already decided");
    change.status = action === "approve" ? "approved" : "rejected";
    change.decidedBy = "demo.user";
    change.decidedAt = at(0);
    const reason = (body as { reason?: string } | undefined)?.reason;
    if (reason) change.reason = reason;
    return change;
  });
}
