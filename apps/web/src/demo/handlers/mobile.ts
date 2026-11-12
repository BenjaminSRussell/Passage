// Mobile warehouse service (/api/v1/mobile) for the demo: the flow designer (save a draft, publish, the device menu) and the
// supervisor's queue of offline conflicts. Typed against the module's own client types; the flows are the bundled core
// flows, the conflicts are confirmations two fictional devices queued at the Cedar Ridge Pumps distribution centre.
import type { MobileMenu, OfflineConflict, SavedFlow } from "../../modules/mobile-flows/api/client";
import { CORE_FLOWS } from "../../modules/mobile-flows/domain/coreFlows";
import type { FlowDraft } from "../../modules/mobile-flows/domain/flowGraph";
import { validateFlow } from "../../modules/mobile-flows/domain/flowGraph";
import { reply, route } from "../backend";
import { at, newId, table, uid } from "../store";

const BASE = "/api/v1/mobile";
const K_FLOW = 386;

const flows = table<SavedFlow & { id: string }>(
  CORE_FLOWS.map((flow, i) => ({ ...flow, id: uid(K_FLOW, i + 1), flowId: uid(K_FLOW, i + 1), version: 1, status: "PUBLISHED" as const })),
);

const asFlow = ({ id: _id, ...flow }: SavedFlow & { id: string }): SavedFlow => flow;

route("POST", `${BASE}/flows`, ({ body }) => {
  const draft = body as FlowDraft;
  if (!draft || typeof draft.code !== "string" || !Array.isArray(draft.steps)) return reply.problem(400, "COM-0001", "The flow is not valid.");
  const issues = validateFlow(draft);
  if (issues.length > 0) return reply.problem(422, "MOB-0113", issues[0]!.message);
  const existing = flows.all().find((f) => f.code === draft.code && f.status === "DRAFT");
  if (existing) return reply.ok(asFlow(flows.update(existing.id, { ...draft, version: existing.version + 1 })!));
  const latest = Math.max(0, ...flows.all().filter((f) => f.code === draft.code).map((f) => f.version));
  const id = newId();
  return reply.created(asFlow(flows.insert({ ...draft, id, flowId: id, version: latest + 1, status: "DRAFT" })));
});

route("POST", `${BASE}/flows/{id}:publish`, ({ params, headers }) => {
  const flow = flows.get(params.id);
  if (!flow) return reply.problem(404, "MOB-0100", "Flow not found.");
  if (headers.get("If-Match") !== `"${flow.version}"`) return reply.problem(412, "COM-0412", "The flow changed since you read it.");
  for (const other of flows.all()) if (other.code === flow.code && other.status === "PUBLISHED" && other.id !== flow.id) flows.update(other.id, { status: "RETIRED" });
  return reply.ok(asFlow(flows.update(flow.id, { status: "PUBLISHED" })!));
});

route("GET", `${BASE}/menus/{id}`, ({ params }): MobileMenu => ({ id: params.id, flows: flows.all().filter((f) => f.status === "PUBLISHED").map(asFlow) }));

const K_CONFLICT = 387;
const conflicts = table<OfflineConflict & { id: string }>(
  [
    { device: "HH-DC-07", seq: 41, command: "ConfirmWorkLine", reason: "line was CANCELLED by Dana Whitfield", hours: -3.2 },
    { device: "HH-DC-07", seq: 42, command: "ConfirmWorkLine", reason: "line was already confirmed by Marcus Bell", hours: -3.1 },
    { device: "HH-DC-12", seq: 9, command: "ConfirmWorkLine", reason: "location PICK-B-14 no longer holds the item", hours: -1.4 },
  ].map((c, i) => ({
    id: uid(K_CONFLICT, i + 1),
    idempotencyKey: `${c.device}:${c.seq}`,
    deviceId: c.device,
    command: c.command,
    capturedAt: at(c.hours / 24),
    conflictReason: c.reason,
    version: 1,
  })),
);

route("GET", `${BASE}/offline/conflicts`, () => ({ items: conflicts.all().map(({ id: _id, ...c }) => c).sort((a, b) => a.capturedAt.localeCompare(b.capturedAt)) }));

route("POST", `${BASE}/offline/conflicts/{key}:resolve`, ({ params, headers, body }) => {
  const row = conflicts.all().find((c) => c.idempotencyKey === decodeURIComponent(params.key));
  if (!row) return reply.problem(404, "MOB-0107", "Conflict not found.");
  if (headers.get("If-Match") !== `"${row.version}"`) return reply.problem(412, "COM-0412", "Another supervisor changed this conflict.");
  const action = (body as { action?: string } | undefined)?.action;
  if (action !== "reapply" && action !== "reject") return reply.problem(400, "COM-0001", "The action must be reapply or reject.");
  conflicts.remove(row.id);
  return reply.ok({ idempotencyKey: row.idempotencyKey, status: action === "reapply" ? "APPLIED" : "REJECTED" });
});
