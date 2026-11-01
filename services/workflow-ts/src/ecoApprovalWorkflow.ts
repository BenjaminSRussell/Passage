import { ActivityFailure, condition, defineQuery, defineSignal, metricMeter, proxyActivities, setHandler, sleep } from "@temporalio/workflow";
import type { EcoActivities } from "./ecoActivities.js";

export interface EcoApprovalInput {
  tenantId: string;
  legalEntityId: string;
  ecoId: string;
  requester: string;
  approver: string;
  approvalTimeoutMs: number;
}

export interface EcoRef {
  tenantId: string;
  legalEntityId: string;
  ecoId: string;
  actor: string;
}

export interface EcoSnapshot {
  id: string;
  number: string;
  state: string;
  releaseDate?: string;
  decidedBy?: string;
}

export interface EcoApprovalState {
  ecoId: string;
  status: "awaiting_approval" | "approved" | "released" | "rejected" | "timed_out" | "release_failed";
  decidedBy?: string;
  failureReason?: string;
}

export const ecoApproveSignal = defineSignal<[actor: string]>("eco-approve");
export const ecoRejectSignal = defineSignal<[actor: string]>("eco-reject");
export const ecoApprovalStateQuery = defineQuery<EcoApprovalState>("eco-approval-state");

const activities = proxyActivities<EcoActivities>({
  startToCloseTimeout: "1 minute",
  scheduleToCloseTimeout: "1 hour",
  retry: { initialInterval: "1 second", maximumInterval: "1 minute", nonRetryableErrorTypes: ["ServiceCallRejected"] },
});

export function ecoApprovalWorkflowId(ecoId: string): string {
  return `eco-approval:${ecoId}`;
}

export function validateEcoApprovalInput(input: EcoApprovalInput): void {
  if (!input.tenantId || !input.legalEntityId || !input.ecoId || !input.requester || !input.approver) {
    throw new TypeError("tenantId, legalEntityId, ecoId, requester and approver are required");
  }
  if (input.approver === input.requester) throw new TypeError("the requester cannot approve their own ECO");
  if (!(input.approvalTimeoutMs > 0)) throw new TypeError("approvalTimeoutMs must be positive");
}

export async function ecoApprovalWorkflow(input: EcoApprovalInput): Promise<EcoApprovalState> {
  validateEcoApprovalInput(input);

  const state: EcoApprovalState = { ecoId: input.ecoId, status: "awaiting_approval" };
  let decision: "approved" | "rejected" | undefined;
  setHandler(ecoApprovalStateQuery, () => ({ ...state }));
  setHandler(ecoApproveSignal, (actor) => {
    if (actor === input.approver && !decision) decision = "approved";
  });
  setHandler(ecoRejectSignal, (actor) => {
    if (actor === input.approver && !decision) decision = "rejected";
  });

  const as = (actor: string): EcoRef => ({ tenantId: input.tenantId, legalEntityId: input.legalEntityId, ecoId: input.ecoId, actor });

  if (!(await condition(() => decision !== undefined, input.approvalTimeoutMs))) {
    const cancelled = await activities.cancelEco(as(input.requester));
    return Object.assign(state, { status: "timed_out" as const, decidedBy: cancelled.decidedBy });
  }
  if (decision === "rejected") {
    const rejected = await activities.rejectEco(as(input.approver));
    return Object.assign(state, { status: "rejected" as const, decidedBy: rejected.decidedBy });
  }

  const approved = await activities.approveEco(as(input.approver));
  state.status = "approved";
  state.decidedBy = approved.decidedBy;
  try {
    let eco = await activities.releaseEco(as(input.approver));
    if (eco.state === "scheduled" && eco.releaseDate) {
      await sleep(Math.max(0, Date.parse(`${eco.releaseDate}T00:00:00Z`) - Date.now()));
      eco = await activities.releaseEco(as(input.approver));
    }
    state.status = eco.state === "released" ? "released" : "release_failed";
    if (state.status === "release_failed") state.failureReason = `ECO is ${eco.state} after release`;
  } catch (error) {
    if (!(error instanceof ActivityFailure)) throw error;
    metricMeter.createCounter("passage_eco_release_failed", "1", "Approved ECOs that could not be released").add(1, { eco_id: input.ecoId });
    state.status = "release_failed";
    state.failureReason = (error.cause?.message ?? error.message).slice(0, 500);
  }
  return { ...state };
}
