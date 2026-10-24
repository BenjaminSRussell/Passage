import {
  condition,
  defineQuery,
  defineSignal,
  setHandler,
} from "@temporalio/workflow";

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
    if (actor === state.approver) decision = "approved";
  });
  setHandler(rejectSignal, (actor) => {
    if (actor === state.approver) decision = "rejected";
  });
  setHandler(delegateSignal, (actor, delegate) => {
    if (actor !== state.approver || delegate.length === 0 || !state.levelId) return;
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
      return snapshot(state);
    }
  }

  state.status = "approved";
  state.levelId = undefined;
  state.approver = undefined;
  return snapshot(state);
}

function validateInput(input: ApprovalInput): void {
  if (!input.requestId || !input.entityType || input.levels.length === 0) {
    throw new TypeError("requestId, entityType and at least one approval level are required");
  }
  for (const level of input.levels) {
    if (!level.id || !level.approver || !level.escalationApprover || level.slaMs <= 0) {
      throw new TypeError("approval levels require id, approver, escalationApprover and positive slaMs");
    }
  }
}

function snapshot(state: ApprovalState): ApprovalState {
  return {
    ...state,
    history: state.history.map((entry) => ({ ...entry })),
  };
}
