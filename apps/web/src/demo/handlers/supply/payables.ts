// Payables service (/api/v1/payables) for the demo: vendor open items, payment proposals and payment runs.
// Typed against the module's own client types so shape drift fails `tsc`.
import type { OpenItem, PaymentRun, Proposal, ProposalGroup } from "../../../modules/payables/api/client";
import { LEGAL_ENTITY, VENDORS } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, newId, table, uid } from "../../store";

const BASE = "/api/v1/payables";
const K_OPEN = 340;
const K_PROPOSAL = 341;

const [IRON, VOLTA, KEYSTONE, ALLIED] = VENDORS;

function open(n: number, vendorId: string, invoice: string, amount: number, dueIn: number, patch: Partial<OpenItem> = {}): OpenItem {
  return {
    id: uid(K_OPEN, n),
    legalEntityId: LEGAL_ENTITY.id,
    vendorId,
    invoiceNumber: invoice,
    invoiceDate: day(dueIn - 30),
    dueDate: day(dueIn),
    currency: "USD",
    amount: dec(amount),
    openAmount: dec(amount),
    status: "OPEN",
    paymentHold: false,
    ...patch,
  };
}

const openItems = table<OpenItem>([
  open(1, IRON.id, "IBF-88120", 18640, -6),
  open(2, IRON.id, "IBF-88317", 9215.5, 8),
  open(3, VOLTA.id, "VMW-120771", 31200, 3),
  open(4, KEYSTONE.id, "KSG-40218", 2480.75, 12),
  open(5, ALLIED.id, "AFS-74402", 5120, 5, { paymentHold: true, paymentHoldReason: "Quantity short on the dock" }),
  open(6, ALLIED.id, "AFS-73990", 1860, -20, { status: "SETTLED", openAmount: "0.00" }),
]);

function group(n: number, vendorId: string, items: OpenItem[]): ProposalGroup {
  const gross = items.reduce((s, i) => s + Number(i.openAmount), 0);
  return {
    id: uid(K_PROPOSAL, 100 + n),
    vendorId,
    currency: "USD",
    paymentMethod: "TRANSFER",
    gross: dec(gross),
    discount: "0.00",
    net: dec(gross),
    valued: dec(gross),
    lines: items.map((i) => ({ openItemId: i.id, openAmount: i.openAmount, discount: "0.00", pay: i.openAmount, valued: i.openAmount })),
  };
}

function proposal(n: number, status: Proposal["status"], groups: ProposalGroup[], ago: number): Proposal {
  return {
    id: uid(K_PROPOSAL, n),
    legalEntityId: LEGAL_ENTITY.id,
    runDate: day(1),
    runId: `RUN-${n}`,
    dueBy: day(14),
    legalEntityCurrency: "USD",
    paymentMethod: "TRANSFER",
    status,
    createdBy: "ana.ortiz",
    createdAt: at(-ago),
    approvedBy: status === "DRAFT" ? undefined : "marcus.lee",
    approvedAt: status === "DRAFT" ? undefined : at(-ago + 0.1),
    totalValued: dec(groups.reduce((s, g) => s + Number(g.valued), 0)),
    groups,
  };
}

const proposals = table<Proposal>([
  proposal(1, "DRAFT", [group(1, IRON.id, [openItems.get(uid(K_OPEN, 2))!]), group(2, VOLTA.id, [openItems.get(uid(K_OPEN, 3))!])], 0.5),
  proposal(2, "APPROVED", [group(3, KEYSTONE.id, [openItems.get(uid(K_OPEN, 4))!])], 1),
]);

const runs = table<PaymentRun>([]);

const summary = (p: Proposal) => ({ ...p, groups: undefined });
const notFound = (what: string) => reply.problem(404, "COM-0404", `${what} not found`);

route("GET", `${BASE}/open-items`, ({ query }) => {
  const status = query.get("status");
  const vendor = query.get("vendor");
  const dueTo = query.get("due_to");
  const ids = query.get("source_invoices");
  const items = openItems
    .all()
    .filter((i) => (!status || i.status === status) && (!vendor || i.vendorId === vendor) && (!dueTo || i.dueDate <= dueTo) && (ids === null || ids.split(",").includes(i.sourceInvoiceId ?? "")))
    .sort((a, b) => a.dueDate.localeCompare(b.dueDate));
  return { items };
});

route("GET", `${BASE}/open-items/{id}`, ({ params }) => openItems.get(params.id) ?? notFound("open item"));

route("POST", `${BASE}/open-items/{id}:hold`, ({ params, body }) => {
  const reason = (body as { reason?: string } | undefined)?.reason;
  const found = openItems.get(params.id);
  if (!found) return notFound("open item");
  if (found.status === "SETTLED" || found.status === "REVERSED") return reply.problem(409, "AP-OPN-001", "settled or reversed items can't be held");
  return openItems.update(params.id, { paymentHold: true, paymentHoldReason: reason || undefined });
});

route("POST", `${BASE}/open-items/{id}:release`, ({ params }) => {
  const found = openItems.get(params.id);
  if (!found) return notFound("open item");
  if (found.status === "SETTLED" || found.status === "REVERSED") return reply.problem(409, "AP-OPN-001", "settled or reversed items can't be released");
  return openItems.update(params.id, { paymentHold: false, paymentHoldReason: undefined });
});

route("GET", `${BASE}/payment-proposals`, ({ query }) => {
  const status = query.get("status");
  return { items: proposals.all().filter((p) => !status || p.status === status).sort((a, b) => b.createdAt.localeCompare(a.createdAt)).map(summary) };
});

route("POST", `${BASE}/payment-proposals`, ({ body }) => {
  const input = body as { runDate: string; runId: string; dueBy?: string; paymentMethod: Proposal["paymentMethod"]; legalEntityCurrency: string };
  const due = openItems.all().filter((i) => i.status === "OPEN" && !i.paymentHold && !i.proposalId && (!input.dueBy || i.dueDate <= input.dueBy));
  if (due.length === 0) return reply.problem(422, "AP-PRP-005", "nothing is due on the run date");
  const byVendor = new Map<string, OpenItem[]>();
  for (const i of due) byVendor.set(i.vendorId, [...(byVendor.get(i.vendorId) ?? []), i]);
  const id = newId();
  const groups = [...byVendor.entries()].map(([vendorId, items], n) => ({ ...group(n, vendorId, items), id: newId(), paymentMethod: input.paymentMethod }));
  const created: Proposal = { ...proposal(0, "DRAFT", groups, 0), id, runDate: input.runDate, runId: input.runId, dueBy: input.dueBy ?? input.runDate, paymentMethod: input.paymentMethod, createdBy: "dev" };
  for (const i of due) openItems.update(i.id, { proposalId: id });
  return reply.created(proposals.insert(created));
});

route("GET", `${BASE}/payment-proposals/{id}`, ({ params }) => proposals.get(params.id) ?? notFound("payment proposal"));

route("POST", `${BASE}/payment-proposals/{id}:approve`, ({ params }) => {
  const p = proposals.get(params.id);
  if (!p) return notFound("payment proposal");
  if (p.status !== "DRAFT") return reply.problem(409, "AP-PRP-002", "payment proposal is not a draft");
  return proposals.update(params.id, { status: "APPROVED", approvedBy: "marcus.lee", approvedAt: at(0) });
});

route("POST", `${BASE}/payment-proposals/{id}:cancel`, ({ params }) => {
  const p = proposals.get(params.id);
  if (!p) return notFound("payment proposal");
  if (p.status !== "DRAFT") return reply.problem(409, "AP-PRP-002", "payment proposal is not a draft");
  for (const g of p.groups) for (const l of g.lines) openItems.update(l.openItemId, { proposalId: undefined });
  return proposals.update(params.id, { status: "CANCELLED" });
});

route("POST", `${BASE}/payment-runs`, ({ body }) => {
  const proposalId = (body as { proposalId: string }).proposalId;
  const existing = runs.all().find((r) => r.proposalId === proposalId);
  if (existing) return reply.ok(existing, { "Idempotent-Replayed": "true" });
  const p = proposals.get(proposalId);
  if (!p) return notFound("payment proposal");
  if (p.status !== "APPROVED") return reply.problem(409, "AP-PRP-002", "payment proposal is not approved");
  proposals.update(proposalId, { status: "EXECUTING" });
  const run: PaymentRun = {
    id: newId(),
    legalEntityId: p.legalEntityId,
    proposalId,
    status: "RUNNING",
    requestedBy: "dev",
    createdAt: at(0),
    payments: p.groups.map((g, i) => ({ groupId: g.id, vendorId: g.vendorId, currency: g.currency, paymentMethod: g.paymentMethod, endToEndId: `E2E-${proposalId.slice(-4)}-${i + 1}`, amount: g.net, valuedAmount: g.valued })),
  };
  return reply.created(runs.insert(run), { Location: `${BASE}/payment-runs/${run.id}` });
});

route("GET", `${BASE}/payment-runs/{id}/file`, ({ params }) => {
  const r = runs.get(params.id);
  if (!r?.fileGeneratedAt) return notFound("payment file");
  return reply.ok({ note: "Bank file (demo)" });
});

route("GET", `${BASE}/payment-runs/{id}`, ({ params }) => runs.get(params.id) ?? notFound("payment run"));

route("POST", `${BASE}/payment-runs/{id}:generate-file`, ({ params }) => {
  const r = runs.get(params.id);
  if (!r) return notFound("payment run");
  if (r.status !== "RUNNING") return reply.problem(409, "AP-RUN-011", "the run is not running");
  return runs.update(params.id, { fileGeneratedAt: at(0), fileNumber: `PF-${params.id.slice(0, 6).toUpperCase()}`, paymentCount: r.payments.length });
});

route("POST", `${BASE}/payment-runs/{id}:post-journal`, ({ params }) => {
  const r = runs.get(params.id);
  if (!r) return notFound("payment run");
  if (!r.fileGeneratedAt) return reply.problem(409, "AP-RUN-012", "generate the file first");
  return runs.update(params.id, { journalRequestedAt: at(0), journalPostedAt: at(0), journalEntryId: newId() });
});

route("POST", `${BASE}/payment-runs/{id}:settle`, ({ params }) => {
  const r = runs.get(params.id);
  if (!r) return notFound("payment run");
  if (!r.journalPostedAt) return reply.problem(409, "AP-RUN-012", "post the journal first");
  const p = proposals.get(r.proposalId);
  for (const g of p?.groups ?? []) for (const l of g.lines) openItems.update(l.openItemId, { status: "SETTLED", openAmount: "0.00" });
  return runs.update(params.id, { settledAt: at(0) });
});

route("POST", `${BASE}/payment-runs/{id}:complete`, ({ params }) => {
  const r = runs.get(params.id);
  if (!r) return notFound("payment run");
  if (!r.settledAt) return reply.problem(409, "AP-RUN-012", "settle the invoices first");
  proposals.update(r.proposalId, { status: "EXECUTED" });
  return runs.update(params.id, { status: "COMPLETED", completedAt: at(0) });
});

route("POST", `${BASE}/payment-runs/{id}:fail`, ({ params, body }) => {
  const r = runs.get(params.id);
  if (!r) return notFound("payment run");
  proposals.update(r.proposalId, { status: "FAILED" });
  return runs.update(params.id, { status: "FAILED", failureReason: (body as { reason?: string }).reason });
});
