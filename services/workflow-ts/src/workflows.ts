import {
  condition,
  defineQuery,
  defineSignal,
  proxyActivities,
  setHandler,
} from "@temporalio/workflow";
import type { PartyDecisionActivities } from "./activities.js";

export * from "./paymentRunWorkflow.js";
export * from "./ecoApprovalWorkflow.js";
export * from "./capaWorkflow.js";
export * from "./tenderWorkflow.js";

const { decidePartyChange } = proxyActivities<PartyDecisionActivities>({
  startToCloseTimeout: "30 seconds",
  retry: { maximumInterval: "1 minute" },
});

export interface PartyChangeRef {
  changeId: string;
  tenantId: string;
  legalEntityId: string;
}

export interface ApprovalLevel {
  id: string;
  approver: string;
  escalationApprover: string;
  slaMs: number;
}

export interface ApprovalInput {
  requestId: string;
  entityType: string;
  levels: ApprovalLevel[];
  requester?: string;
  partyChange?: PartyChangeRef;
}

export interface ApprovalHistoryEntry {
  action: "approved" | "rejected" | "delegated" | "escalated";
  levelId: string;
  actor: string;
  to?: string;
}

export interface ApprovalState {
  requestId: string;
  entityType: string;
  /** Set for a party change approval, so the decision route can check the caller's tenant and legal entity. */
  tenantId?: string;
  legalEntityId?: string;
  requester?: string;
  status: "pending" | "approved" | "rejected";
  levelId?: string;
  approver?: string;
  history: ApprovalHistoryEntry[];
}

export type ApprovalResult = ApprovalState;

/**
 * One decision on the current level. Only the decision route (src/decisionApi.ts) sends it, with the actor and legal-entity
 * scope of the gateway-verified principal that called it; the workflow never takes an actor from anywhere else.
 */
export interface ApprovalDecision {
  action: "approve" | "reject" | "delegate";
  actor: string;
  scope: string[];
  delegate?: string;
}

export const approvalDecisionSignal = defineSignal<[decision: ApprovalDecision]>("approval-decision");
export const approvalStateQuery = defineQuery<ApprovalState>("state");

export function approvalWorkflowId(requestId: string): string {
  return `approval:${requestId}`;
}

export async function approvalWorkflow(input: ApprovalInput): Promise<ApprovalResult> {
  validateInput(input);

  const state: ApprovalState = {
    requestId: input.requestId,
    entityType: input.entityType,
    ...(input.partyChange ? { tenantId: input.partyChange.tenantId, legalEntityId: input.partyChange.legalEntityId } : {}),
    ...(input.requester ? { requester: input.requester } : {}),
    status: "pending",
    history: [],
  };
  let decision: "approved" | "rejected" | undefined;
  let decidedScope: string[] = [];

  setHandler(approvalStateQuery, () => snapshot(state));
  // Decisions are queued and applied by the workflow once the current level is set: a signal delivered in the first
  // activation, before the loop has named the approver, would otherwise be dropped.
  const queued: ApprovalDecision[] = [];
  setHandler(approvalDecisionSignal, (incoming) => void queued.push(incoming));
  const apply = ({ action, actor, scope, delegate }: ApprovalDecision): void => {
    if (decision !== undefined || !state.levelId || actor !== state.approver || actor === input.requester) return;
    // A party change is decided under a service token for the actor, which may only name a legal entity in the actor's scope.
    if (input.partyChange && !inScope(scope, input.partyChange.legalEntityId)) return;
    if (action === "delegate") {
      if (!delegate || delegate === actor || delegate === input.requester) return;
      state.history.push({ action: "delegated", levelId: state.levelId, actor, to: delegate });
      state.approver = delegate;
      return;
    }
    decision = action === "approve" ? "approved" : "rejected";
    decidedScope = [...scope];
  };
  /** Applies queued decisions until one decides the level; false when `timeoutMs` passes first. */
  const decided = async (timeoutMs?: number): Promise<boolean> => {
    const deadline = timeoutMs === undefined ? undefined : Date.now() + timeoutMs;
    for (;;) {
      while (queued.length > 0 && decision === undefined) apply(queued.shift()!);
      if (decision !== undefined) return true;
      if (deadline === undefined) {
        await condition(() => queued.length > 0);
        continue;
      }
      const left = deadline - Date.now();
      if (left <= 0 || !(await condition(() => queued.length > 0, left))) return false;
    }
  };

  for (const level of input.levels) {
    state.levelId = level.id;
    state.approver = level.approver;
    decision = undefined;

    const decidedBeforeSla = await decided(level.slaMs);
    if (!decidedBeforeSla) {
      state.history.push({
        action: "escalated",
        levelId: level.id,
        actor: state.approver,
        to: level.escalationApprover,
      });
      state.approver = level.escalationApprover;
      await decided();
    }

    state.history.push({
      action: decision!,
      levelId: level.id,
      actor: state.approver,
    });
    if (decision === "rejected") {
      state.status = "rejected";
      await recordPartyDecision(input, "rejected", state.history, decidedScope);
      return snapshot(state);
    }
  }

  state.status = "approved";
  state.levelId = undefined;
  state.approver = undefined;
  await recordPartyDecision(input, "approved", state.history, decidedScope);
  return snapshot(state);
}

async function recordPartyDecision(
  input: ApprovalInput,
  decision: "approved" | "rejected",
  history: ApprovalHistoryEntry[],
  scope: string[],
): Promise<void> {
  const decidedBy = history[history.length - 1]?.actor;
  if (!input.partyChange || !decidedBy) return;
  await decidePartyChange({ ...input.partyChange, decision, decidedBy, scope });
}

function inScope(scope: readonly string[], legalEntityId: string): boolean {
  const wanted = legalEntityId.trim().toLowerCase();
  return !!wanted && scope.some((entry) => entry.trim().toLowerCase() === wanted);
}

export function validateInput(input: ApprovalInput): void {
  if (!input.requestId || !input.entityType || input.levels.length === 0) {
    throw new TypeError("requestId, entityType and at least one approval level are required");
  }
  if (input.partyChange && !input.requester) {
    throw new TypeError("a party change approval requires the requester");
  }
  for (const level of input.levels) {
    if (!level.id || !level.approver || !level.escalationApprover || level.slaMs <= 0) {
      throw new TypeError("approval levels require id, approver, escalationApprover and positive slaMs");
    }
    if (input.requester && (level.approver === input.requester || level.escalationApprover === input.requester)) {
      throw new TypeError("the requester cannot be an approver or escalation approver");
    }
  }
}

function snapshot(state: ApprovalState): ApprovalState {
  return {
    ...state,
    history: state.history.map((entry) => ({ ...entry })),
  };
}
