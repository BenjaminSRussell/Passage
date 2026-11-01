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
  status: "pending" | "approved" | "rejected";
  levelId?: string;
  approver?: string;
  history: ApprovalHistoryEntry[];
}

export type ApprovalResult = ApprovalState;

export const approveSignal = defineSignal<[actor: string]>("approve");
export const rejectSignal = defineSignal<[actor: string]>("reject");
export const delegateSignal = defineSignal<[actor: string, delegate: string]>("delegate");
export const approvalStateQuery = defineQuery<ApprovalState>("state");

export async function approvalWorkflow(input: ApprovalInput): Promise<ApprovalResult> {
  validateInput(input);

  const state: ApprovalState = {
    requestId: input.requestId,
    entityType: input.entityType,
    status: "pending",
    history: [],
  };
  let decision: "approved" | "rejected" | undefined;

  setHandler(approvalStateQuery, () => snapshot(state));
  setHandler(approveSignal, (actor) => {
    if (actor === state.approver && actor !== input.requester) decision = "approved";
  });
  setHandler(rejectSignal, (actor) => {
    if (actor === state.approver && actor !== input.requester) decision = "rejected";
  });
  setHandler(delegateSignal, (actor, delegate) => {
    if (actor !== state.approver || delegate.length === 0 || delegate === input.requester || !state.levelId) return;
    state.history.push({
      action: "delegated",
      levelId: state.levelId,
      actor,
      to: delegate,
    });
    state.approver = delegate;
  });

  for (const level of input.levels) {
    state.levelId = level.id;
    state.approver = level.approver;
    decision = undefined;

    const decidedBeforeSla = await condition(() => decision !== undefined, level.slaMs);
    if (!decidedBeforeSla) {
      state.history.push({
        action: "escalated",
        levelId: level.id,
        actor: state.approver,
        to: level.escalationApprover,
      });
      state.approver = level.escalationApprover;
      await condition(() => decision !== undefined);
    }

    state.history.push({
      action: decision!,
      levelId: level.id,
      actor: state.approver,
    });
    if (decision === "rejected") {
      state.status = "rejected";
      await recordPartyDecision(input, "rejected", state.history);
      return snapshot(state);
    }
  }

  state.status = "approved";
  state.levelId = undefined;
  state.approver = undefined;
  await recordPartyDecision(input, "approved", state.history);
  return snapshot(state);
}

async function recordPartyDecision(
  input: ApprovalInput,
  decision: "approved" | "rejected",
  history: ApprovalHistoryEntry[],
): Promise<void> {
  const decidedBy = history[history.length - 1]?.actor;
  if (!input.partyChange || !decidedBy) return;
  await decidePartyChange({ ...input.partyChange, decision, decidedBy });
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
