import { AgentError } from "../errors.js";
import { canCallTool, selfApprovalBlocked } from "./authz.js";
import type { Money } from "./money.js";
import { isApprovalExpired } from "./policy.js";

/** BR-47.3 default; the tenant parameter `agent.approval_expiry_h` allows 1 to 240 (9.47.13). */
export const DEFAULT_APPROVAL_EXPIRY_H = 72;

const HOUR_MS = 3_600_000;

/** Rejects an `agent.approval_expiry_h` value outside 1 to 240 whole hours (9.47.13). */
export function assertApprovalExpiryHours(hours: number): void {
  if (!Number.isInteger(hours) || hours < 1 || hours > 240) {
    throw new Error("approval expiry must be an integer number of hours between 1 and 240");
  }
}

/** The instant at which an open approval request expires (BR-47.3). */
export function approvalExpiresAtMs(requestedAtMs: number, expiryHours: number = DEFAULT_APPROVAL_EXPIRY_H): number {
  assertApprovalExpiryHours(expiryHours);
  return requestedAtMs + expiryHours * HOUR_MS;
}

export interface PendingApproval {
  actionId: string;
  requestedAtMs: number;
}

/**
 * The hourly expiry sweeper's selection (BR-47.3, 9.47.16): pending approvals
 * past their expiry, oldest first. Each one is then REJECTED with reason
 * EXPIRED and the source document stays unposted.
 */
export function expiredApprovals(
  pending: readonly PendingApproval[],
  nowMs: number,
  expiryHours: number = DEFAULT_APPROVAL_EXPIRY_H,
): string[] {
  assertApprovalExpiryHours(expiryHours);
  return pending
    .filter((p) => isApprovalExpired(p.requestedAtMs, nowMs, expiryHours))
    .sort((a, b) => a.requestedAtMs - b.requestedAtMs)
    .map((p) => p.actionId);
}

export interface DecideApprovalInput {
  requestedAtMs: number;
  nowMs: number;
  expiryHours?: number;
  /** The run's `invoked_by_user_id`. */
  invokerUserId: string;
  approverUserId: string;
  /** The approver's duties; they must include the action's approver duty (9.47.5). */
  approverDuties: ReadonlySet<string>;
  approvalDuty: string;
  decision: "APPROVED" | "REJECTED";
  amount: Money;
  /** The wave-48 SoD self-approval limit. */
  selfApprovalLimit: Money;
}

export interface ApprovalOutcome {
  decision: "APPROVED" | "REJECTED";
  /** "EXPIRED" when BR-47.3 timed the request out; absent for a human decision. */
  reason?: "EXPIRED";
  code?: "AGT-0208";
}

/**
 * The decision endpoint's rules (9.47.5) for one pending approval. A caller
 * without the approver duty is refused with AGT-0203 before anything else, so
 * the request's state is not disclosed to them. Expiry then wins over any late
 * decision (BR-47.3: REJECTED with reason EXPIRED, AGT-0208, nothing posted).
 * An approver who is the invoker may not approve above the self-approval limit
 * (AGT-0209); rejecting their own run's action is always allowed.
 */
export function decideApproval(input: DecideApprovalInput): ApprovalOutcome {
  if (!canCallTool(input.approverDuties, input.approvalDuty)) {
    throw new AgentError("AGT-0203", `you do not hold duty ${input.approvalDuty} required to decide this approval`);
  }
  const expiryHours = input.expiryHours ?? DEFAULT_APPROVAL_EXPIRY_H;
  assertApprovalExpiryHours(expiryHours);
  if (isApprovalExpired(input.requestedAtMs, input.nowMs, expiryHours)) {
    return { decision: "REJECTED", reason: "EXPIRED", code: "AGT-0208" };
  }
  if (
    input.decision === "APPROVED" &&
    selfApprovalBlocked({
      approverUserId: input.approverUserId,
      invokerUserId: input.invokerUserId,
      amount: input.amount,
      selfApprovalLimit: input.selfApprovalLimit,
    })
  ) {
    throw new AgentError("AGT-0209", "you cannot approve an action from a run you started above the self-approval limit");
  }
  return { decision: input.decision };
}
