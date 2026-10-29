import { decideApproval } from "../domain/approval.js";
import type { Money } from "../domain/money.js";
import { ApiError, asObject, type Principal, requireDuty, toApiError } from "./principal.js";

export type Decision = "APPROVED" | "REJECTED";

/** A decision as `agent_action` records it (approver_user_id, decision, decided_at). */
export interface RecordedDecision {
  decision: Decision;
  /** "EXPIRED" when BR-47.3 timed the request out (code AGT-0208). */
  reason?: "EXPIRED";
  code?: "AGT-0208";
  approverUserId: string;
  decidedAt: string;
}

/** An approval request as the store holds it (FR-47.8). */
export interface ApprovalRequest {
  invokerUserId: string;
  requestedAtMs: number;
  expiryHours?: number;
  approvalDuty: string;
  amount: Money;
  selfApprovalLimit: Money;
  /** Set once the action is decided; a decided action is never decided again. */
  decided?: RecordedDecision;
}

/**
 * The approval requests of the gated actions. Every call is scoped to the
 * caller's tenant, so one tenant can never read or decide another tenant's
 * action, whatever ids it sends (production: RLS on `agent_action`).
 */
export interface ApprovalStore {
  find(tenantId: string, runId: string, actionId: string): Promise<ApprovalRequest | undefined>;
  /**
   * Records the decision only if the action is still undecided, and returns
   * the decision that is stored afterwards: ours, or the one another approver
   * recorded first (production: `UPDATE ... WHERE decision IS NULL`).
   */
  record(tenantId: string, runId: string, actionId: string, decision: RecordedDecision): Promise<RecordedDecision>;
}

export interface DecisionResult extends RecordedDecision {
  runId: string;
  actionId: string;
}

/** Validates the POST body of `.../actions/{id}/decision`. */
export function parseDecision(raw: unknown): Decision {
  const decision = asObject(raw, ["decision"]).decision;
  if (decision === "APPROVED" || decision === "REJECTED") return decision;
  throw new ApiError(400, "AGT-0400", "decision must be APPROVED or REJECTED");
}

/**
 * Answers a repeated decision: the same decision again returns the recorded
 * one (idempotent by action id, 9.47.5), and so does any decision on an
 * expired request (BR-47.3). A different decision on a decided action is 409.
 */
function replay(stored: RecordedDecision, requested: Decision): RecordedDecision {
  if (stored.decision === requested || stored.reason === "EXPIRED") return stored;
  throw new ApiError(409, "AGT-0409", `this action was already ${stored.decision.toLowerCase()}`);
}

/**
 * Records an approver's decision on a gated action (FR-47.8). The approver is
 * the caller and must hold the action's approval duty (AGT-0203) before
 * anything about the request is disclosed. An approver who started the run
 * may not approve above the self-approval limit (AGT-0209). An expired
 * request is recorded and answered as REJECTED with reason EXPIRED (BR-47.3):
 * the source document stays unposted.
 */
export async function handleApprovalDecision(input: {
  principal: Principal;
  runId: string;
  actionId: string;
  body: unknown;
  store: ApprovalStore;
  now: () => Date;
}): Promise<DecisionResult> {
  const requested = parseDecision(input.body);
  const { tenantId, userId } = input.principal;
  const request = await input.store.find(tenantId, input.runId, input.actionId);
  if (request === undefined) {
    throw new ApiError(404, "AGT-0404", "no approval request for this action");
  }
  requireDuty(input.principal, request.approvalDuty, "this approval");
  let stored: RecordedDecision;
  if (request.decided !== undefined) {
    stored = replay(request.decided, requested);
  } else {
    const now = input.now();
    let outcome;
    try {
      outcome = decideApproval({
        requestedAtMs: request.requestedAtMs,
        nowMs: now.getTime(),
        expiryHours: request.expiryHours,
        invokerUserId: request.invokerUserId,
        approverUserId: userId,
        approverDuties: input.principal.duties,
        approvalDuty: request.approvalDuty,
        decision: requested,
        amount: request.amount,
        selfApprovalLimit: request.selfApprovalLimit,
      });
    } catch (err) {
      throw toApiError(err);
    }
    const ours: RecordedDecision = {
      decision: outcome.decision,
      ...(outcome.reason !== undefined ? { reason: outcome.reason } : {}),
      ...(outcome.code !== undefined ? { code: outcome.code } : {}),
      approverUserId: userId,
      decidedAt: now.toISOString(),
    };
    // A concurrent approver may have recorded first; theirs stands.
    stored = replay(await input.store.record(tenantId, input.runId, input.actionId, ours), requested);
  }
  return { runId: input.runId, actionId: input.actionId, ...stored };
}
