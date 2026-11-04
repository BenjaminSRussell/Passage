import type { IncomingMessage, RequestListener, ServerResponse } from "node:http";
import { WorkflowNotFoundError, type Client } from "@temporalio/client";
import {
  approvalDecisionSignal,
  approvalStateQuery,
  approvalWorkflowId,
  capaEffectivenessSignal,
  capaStateQuery,
  capaStepDoneSignal,
  capaWorkflowId,
  ecoApprovalStateQuery,
  ecoApprovalWorkflowId,
  ecoDecisionSignal,
  partyMergeRequestId,
} from "./workflows.js";
import { inLegalEntityScope } from "./serviceToken.js";

export const ECO_DECIDE_DUTY = "engineering.eco.approve";
export const CAPA_DECIDE_DUTY = "QM_CAPA_MAINTAIN";
export const PARTY_DECIDE_DUTY = "party.approve";
export const PARTY_MERGE_DECIDE_DUTY = "party.merge.approve";
export const TENANT_ADMIN_DUTY = "tenant.administer";
/**
 * Segregation of duties (#450): a merge moves vendor roles and their bank accounts to the survivor, so whoever approves
 * merges may not also approve or execute payment runs. Holding both is refused, whatever the request.
 */
export const MERGE_SOD_CONFLICTS = ["payment-proposal.approve", "payment-run.execute"] as const;

const MAX_BODY_BYTES = 8192;
const NIL_UUID = "00000000-0000-0000-0000-000000000000";
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const ECO_ROUTE = /^\/v1\/workflows\/eco-approvals\/([^/:]+):(approve|reject)$/;
const CAPA_ROUTE = /^\/v1\/workflows\/capa\/([^/:]+):(step-done|effectiveness)$/;
const PARTY_ROUTE = /^\/v1\/workflows\/party-changes\/([^/:]+):(approve|reject|delegate)$/;
const MERGE_ROUTE = /^\/v1\/workflows\/party-merges\/([^/:]+):(approve|reject|delegate)$/;

interface Principal {
  tenantId: string;
  userId: string;
  scope: string[];
  duties: Set<string>;
}

class Refusal extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

export function createDecisionHandler(client: Pick<Client, "workflow">): RequestListener {
  return (request, response) => {
    handle(client, request, response).catch(() => reply(response, new Refusal(502, "WF-0502", "the workflow service could not be reached")));
  };
}

async function handle(client: Pick<Client, "workflow">, request: IncomingMessage, response: ServerResponse): Promise<void> {
  const path = (request.url ?? "").split("?")[0] ?? "";
  if (path === "/healthz" || path === "/readyz") return void response.writeHead(200).end();
  const eco = ECO_ROUTE.exec(path);
  const capa = CAPA_ROUTE.exec(path);
  const party = PARTY_ROUTE.exec(path);
  const merge = MERGE_ROUTE.exec(path);
  if (!eco && !capa && !party && !merge) return reply(response, new Refusal(404, "COM-0404", "no such route"));
  if (request.method !== "POST") return reply(response, new Refusal(405, "COM-0405", "POST only"));
  try {
    const principal = principalOf(request);
    const body = await readJson(request);
    if (party) await decidePartyChange(client, principal, party[1]!, party[2] as "approve" | "reject" | "delegate", body);
    else if (merge) await decidePartyMerge(client, principal, merge[1]!, merge[2] as "approve" | "reject" | "delegate", body);
    else if (eco) await decideEco(client, principal, eco[1]!, eco[2] as "approve" | "reject", body);
    else await advanceCapa(client, principal, capa![1]!, capa![2] as "step-done" | "effectiveness", body);
    response.writeHead(202, { "Content-Type": "application/json" }).end(JSON.stringify({ accepted: true }));
  } catch (error) {
    reply(response, error instanceof Refusal ? error : new Refusal(502, "WF-0502", "the workflow service could not be reached"));
  }
}

function principalOf(request: IncomingMessage): Principal {
  if (request.headers["x-passage-service-token"] !== undefined) throw new Refusal(401, "AUTH-0001", "decisions are made by people, not services");
  const tenantId = String(request.headers["x-passage-tenant"] ?? "");
  const userId = String(request.headers["x-passage-user"] ?? "").trim();
  if (!UUID.test(tenantId) || !userId) throw new Refusal(401, "AUTH-0001", "missing tenant or user");
  const duties = new Set(String(request.headers["x-passage-duties"] ?? "").split(",").map((duty) => duty.trim()).filter(Boolean));
  const scope = String(request.headers["x-passage-legal-entity"] ?? "")
    .split(",")
    .map((entry) => entry.trim().toLowerCase())
    .filter((entry) => UUID.test(entry) && entry !== NIL_UUID);
  return { tenantId, userId, scope, duties };
}

function requireDuty(principal: Principal, duty: string): void {
  if (!principal.duties.has(duty)) throw new Refusal(403, "AUTH-0003", `requires ${duty}`);
}

async function readJson(request: IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of request) {
    size += (chunk as Buffer).length;
    if (size > MAX_BODY_BYTES) throw new Refusal(413, "COM-0413", "body too large");
    chunks.push(chunk as Buffer);
  }
  if (size === 0) return {};
  try {
    const parsed: unknown = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) return parsed as Record<string, unknown>;
  } catch {
    // falls through to the refusal below
  }
  throw new Refusal(400, "COM-0001", "body must be a JSON object");
}

function text(body: Record<string, unknown>, field: string, max: number, required: boolean): string | undefined {
  const value = body[field];
  if (value === undefined && !required) return undefined;
  if (typeof value !== "string" || value.trim() === "" || [...value].length > max) throw new Refusal(400, "COM-0001", `${field} must be 1 to ${max} characters`);
  return value.trim();
}

async function stateOf<T extends { tenantId: string; legalEntityId: string }>(read: () => Promise<T>, principal: Principal): Promise<T> {
  let state: T;
  try {
    state = await read();
  } catch (error) {
    if (error instanceof WorkflowNotFoundError) throw new Refusal(404, "WF-0404", "no workflow is running for this record");
    throw error;
  }
  if (state.tenantId !== principal.tenantId) throw new Refusal(404, "WF-0404", "no workflow is running for this record");
  if (!inLegalEntityScope(principal.scope, state.legalEntityId)) throw new Refusal(403, "AUTH-0003", "the record belongs to a legal entity outside your scope");
  return state;
}

async function decideEco(client: Pick<Client, "workflow">, principal: Principal, ecoId: string, action: "approve" | "reject", body: Record<string, unknown>): Promise<void> {
  requireDuty(principal, ECO_DECIDE_DUTY);
  const reason = action === "reject" ? text(body, "reason", 200, false) : undefined;
  const handle = client.workflow.getHandle(ecoApprovalWorkflowId(ecoId));
  const state = await stateOf(() => handle.query(ecoApprovalStateQuery), principal);
  if (state.status !== "awaiting_approval") throw new Refusal(409, "WF-0409", `the ECO approval is ${state.status}`);
  if (state.requester === principal.userId) throw new Refusal(403, "ENG-ECO-SELF-APPROVAL", "the user who created the ECO cannot decide it");
  await handle.signal(ecoDecisionSignal, { decision: action === "approve" ? "approved" : "rejected", actor: principal.userId, scope: principal.scope, ...(reason ? { reason } : {}) });
}

async function advanceCapa(client: Pick<Client, "workflow">, principal: Principal, ncrId: string, action: "step-done" | "effectiveness", body: Record<string, unknown>): Promise<void> {
  requireDuty(principal, CAPA_DECIDE_DUTY);
  const handle = client.workflow.getHandle(capaWorkflowId(ncrId));
  if (action === "step-done") {
    const step = body.step;
    if (!Number.isInteger(step) || (step as number) < 1 || (step as number) > 7) throw new Refusal(400, "COM-0001", "step must be 1 to 7");
    const notes = text(body, "notes", 2000, true)!;
    const state = await stateOf(() => handle.query(capaStateQuery), principal);
    if (state.status !== "in_correction" || state.currentStep !== step) throw new Refusal(409, "WF-0409", `D${state.currentStep} is the current step (${state.status})`);
    await handle.signal(capaStepDoneSignal, { step: step as number, notes, actor: principal.userId, scope: principal.scope });
    return;
  }
  const result = body.result;
  if (result !== "EFFECTIVE" && result !== "NOT_EFFECTIVE") throw new Refusal(400, "COM-0001", "result must be EFFECTIVE or NOT_EFFECTIVE");
  const note = text(body, "note", 2000, true)!;
  const state = await stateOf(() => handle.query(capaStateQuery), principal);
  if (state.status !== "awaiting_effectiveness") throw new Refusal(409, "WF-0409", `the effectiveness check is not open (${state.status})`);
  if (!state.implementer || state.implementer === principal.userId) throw new Refusal(403, "QM-0175", "the effectiveness check must be made by a user other than the one who completed D7");
  await handle.signal(capaEffectivenessSignal, { result, note, actor: principal.userId, scope: principal.scope });
}

async function decidePartyChange(
  client: Pick<Client, "workflow">,
  principal: Principal,
  changeId: string,
  action: "approve" | "reject" | "delegate",
  body: Record<string, unknown>,
): Promise<void> {
  requireDuty(principal, PARTY_DECIDE_DUTY);
  const delegate = action === "delegate" ? text(body, "delegate", 200, true) : undefined;
  const handle = client.workflow.getHandle(approvalWorkflowId(changeId));
  const state = await stateOf(async () => {
    const read = await handle.query(approvalStateQuery);
    if (!read.tenantId || !read.legalEntityId) throw new Refusal(404, "WF-0404", "no party change approval is running for this record");
    return { ...read, tenantId: read.tenantId, legalEntityId: read.legalEntityId };
  }, principal);
  if (state.status !== "pending") throw new Refusal(409, "WF-0409", `the party change approval is ${state.status}`);
  if (state.requester === principal.userId) throw new Refusal(403, "PTY-PENDING-SELF-APPROVAL", "the user who requested the change cannot decide it");
  if (state.approver !== principal.userId) throw new Refusal(403, "WF-0403", "only the current approver of this level can decide it");
  if (delegate !== undefined && (delegate === principal.userId || delegate === state.requester)) {
    throw new Refusal(400, "COM-0001", "delegate to someone other than yourself and the requester");
  }
  await handle.signal(approvalDecisionSignal, { action, actor: principal.userId, scope: principal.scope, ...(delegate ? { delegate } : {}) });
}

async function decidePartyMerge(
  client: Pick<Client, "workflow">,
  principal: Principal,
  mergeRequestId: string,
  action: "approve" | "reject" | "delegate",
  body: Record<string, unknown>,
): Promise<void> {
  requireDuty(principal, PARTY_MERGE_DECIDE_DUTY);
  const conflict = MERGE_SOD_CONFLICTS.find((duty) => principal.duties.has(duty));
  if (conflict) throw new Refusal(403, "PTY-MERGE-SOD", `a merge approver may not also hold ${conflict}`);
  // A merge spans legal entities, as in party: only a tenant administrator with no legal-entity claim decides one.
  if (!principal.duties.has(TENANT_ADMIN_DUTY) || principal.scope.length > 0) {
    throw new Refusal(403, "AUTH-0003", "a merge is decided by a tenant.administer user with no legal-entity claim");
  }
  const delegate = action === "delegate" ? text(body, "delegate", 200, true) : undefined;
  const handle = client.workflow.getHandle(approvalWorkflowId(partyMergeRequestId(mergeRequestId)));
  let state;
  try {
    state = await handle.query(approvalStateQuery);
  } catch (error) {
    if (error instanceof WorkflowNotFoundError) throw new Refusal(404, "WF-0404", "no party merge approval is running for this record");
    throw error;
  }
  if (!state.mergeRequestId || state.tenantId !== principal.tenantId) throw new Refusal(404, "WF-0404", "no party merge approval is running for this record");
  if (state.status !== "pending") throw new Refusal(409, "WF-0409", `the party merge approval is ${state.status}`);
  if (state.requester === principal.userId) throw new Refusal(403, "PTY-MERGE-SELF-APPROVAL", "the user who requested the merge cannot decide it");
  if (state.approver !== principal.userId) throw new Refusal(403, "WF-0403", "only the current approver of this level can decide it");
  if (delegate !== undefined && (delegate === principal.userId || delegate === state.requester)) {
    throw new Refusal(400, "COM-0001", "delegate to someone other than yourself and the requester");
  }
  await handle.signal(approvalDecisionSignal, { action, actor: principal.userId, scope: [], ...(delegate ? { delegate } : {}) });
}

function reply(response: ServerResponse, refusal: Refusal): void {
  response.writeHead(refusal.status, { "Content-Type": "application/problem+json" }).end(
    JSON.stringify({ type: `https://passage.dev/problems/${refusal.code}`, title: refusal.message, status: refusal.status, code: refusal.code, detail: refusal.message }),
  );
}
