import { ActivityFailure, condition, defineQuery, defineSignal, metricMeter, proxyActivities, setHandler, sleep } from "@temporalio/workflow";
import type { EcoActivities } from "./ecoActivities.js";

export const ECO_TIMEOUT_ACTOR = "system:eco-timeout";

export interface EcoApprovalInput {
  tenantId: string;
  legalEntityId: string;
  ecoId: string;
  requester: string;
  approvalTimeoutMs: number;
}

export interface EcoRef {
  tenantId: string;
  legalEntityId: string;
  ecoId: string;
  actor: string;
  scope: string[];
  reason?: string;
}

export interface EcoSnapshot {
  id: string;
  number: string;
  state: string;
  releaseDate?: string;
  decidedBy?: string;
}

export interface EcoDecision {
  decision: "approved" | "rejected";
  actor: string;
  scope: string[];
  reason?: string;
}

export interface EcoApprovalState {
  ecoId: string;
  tenantId: string;
  legalEntityId: string;
  requester: string;
  status: "awaiting_approval" | "approved" | "released" | "rejected" | "timed_out" | "release_failed";
  decidedBy?: string;
  refusedDecisions: number;
  failureReason?: string;
}

export const ecoDecisionSignal = defineSignal<[decision: EcoDecision]>("eco-decision");
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
  if (!input.tenantId || !input.legalEntityId || !input.ecoId || !input.requester) {
    throw new TypeError("tenantId, legalEntityId, ecoId and requester are required");
  }
  if (!(input.approvalTimeoutMs > 0)) throw new TypeError("approvalTimeoutMs must be positive");
}

export async function ecoApprovalWorkflow(input: EcoApprovalInput): Promise<EcoApprovalState> {
  validateEcoApprovalInput(input);

  const state: EcoApprovalState = {
    ecoId: input.ecoId,
    tenantId: input.tenantId,
    legalEntityId: input.legalEntityId,
    requester: input.requester,
    status: "awaiting_approval",
    refusedDecisions: 0,
  };
  const decisions: EcoDecision[] = [];
  setHandler(ecoApprovalStateQuery, () => ({ ...state }));
  setHandler(ecoDecisionSignal, (decision) => void decisions.push(decision));

  const as = (actor: string, scope: string[], reason?: string): EcoRef => ({
    tenantId: input.tenantId,
    legalEntityId: input.legalEntityId,
    ecoId: input.ecoId,
    actor,
    scope,
    ...(reason ? { reason } : {}),
  });

  const deadline = Date.now() + input.approvalTimeoutMs;
  for (;;) {
    const arrived = await condition(() => decisions.length > 0, Math.max(1, deadline - Date.now()));
    if (!arrived) {
      const cancelled = await activities.cancelEco(as(ECO_TIMEOUT_ACTOR, [input.legalEntityId], "no decision within the approval window"));
      return Object.assign(state, { status: "timed_out" as const, decidedBy: cancelled.decidedBy });
    }
    const decision = decisions.shift()!;
    try {
      const decided = await (decision.decision === "rejected"
        ? activities.rejectEco(as(decision.actor, decision.scope, decision.reason))
        : activities.approveEco(as(decision.actor, decision.scope)));
      state.decidedBy = decided.decidedBy;
      if (decision.decision === "rejected") return Object.assign(state, { status: "rejected" as const });
      state.status = "approved";
      return await release(decision.actor, decision.scope);
    } catch (error) {
      if (!(error instanceof ActivityFailure) || state.status === "approved") throw error;
      state.refusedDecisions += 1;
    }
  }

  async function release(actor: string, scope: string[]): Promise<EcoApprovalState> {
    try {
      let eco = await activities.releaseEco(as(actor, scope));
      if (eco.state === "scheduled" && eco.releaseDate) {
        await sleep(Math.max(0, Date.parse(`${eco.releaseDate}T00:00:00Z`) - Date.now()));
        eco = await activities.releaseEco(as(actor, scope));
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
}
