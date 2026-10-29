import { compareMoney, type Money } from "./money.js";

/**
 * An agent acts with the invoking user's duties and never more (FR-47.2).
 * The agent's effective duties are exactly the user's, so the check is the
 * user's own duty set.
 */
export function canCallTool(userDuties: ReadonlySet<string>, requiredDuty: string): boolean {
  return userDuties.has(requiredDuty);
}

/**
 * Self-approval rule (9.47.5, AGT-0209): an approver who is also the invoker
 * may not approve an action above the self-approval limit.
 */
export function selfApprovalBlocked(d: {
  approverUserId: string | null;
  invokerUserId: string;
  amount: Money;
  selfApprovalLimit: Money;
}): boolean {
  if (d.approverUserId === null || d.approverUserId !== d.invokerUserId) return false;
  return compareMoney(d.amount, d.selfApprovalLimit) > 0;
}
