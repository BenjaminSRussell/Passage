import { DEFAULT_APPROVAL_EXPIRY_H } from "../domain/approval.js";
import type { Money } from "../domain/money.js";
import { isApprovalExpired } from "../domain/policy.js";
import type { ApprovalStore } from "../api/decision.js";
import type { NewApproval } from "../store/approvals.js";
import type { ApprovalDecision, ApprovalPort, ApprovalRequest } from "./engine.js";

/** The slice of PgApprovalStore the port needs; tests give an in-memory one. */
export interface ApprovalInbox extends ApprovalStore {
  open(a: NewApproval): Promise<string>;
}

export interface PersistedApprovalOptions {
  inbox: ApprovalInbox;
  tenantId: string;
  invokerUserId: string;
  /** The duty an approver must hold for a tool, from the tool catalog. */
  approvalDuty: (tool: string) => string;
  selfApprovalLimit: Money;
  expiryHours?: number;
  nowMs: () => number;
  now: () => string;
  /** Called with the new request's action id, so a caller (or a test) can see what waits. */
  onOpened?: (actionId: string) => void;
  sleep?: (ms: number) => Promise<void>;
  pollMs?: number;
}

/** Recorded as approver of a request that expired with nobody deciding it. */
export const EXPIRY_ACTOR = "system:expiry";

/**
 * The engine's approval gate backed by `approval_request` (FR-47.8, BR-47.3): the request is stored
 * with the redacted proposal, then the port polls for the first recorded decision. When the request
 * ages past `agent.approval_expiry_h` it records REJECTED/EXPIRED itself; a human decision racing
 * with it is whichever the store recorded first.
 */
export function createPersistedApprovalPort(o: PersistedApprovalOptions): ApprovalPort {
  const sleep = o.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  const pollMs = o.pollMs ?? 2000;
  return {
    async request(req: ApprovalRequest): Promise<ApprovalDecision> {
      const requestedAtMs = o.nowMs();
      const expiryHours = o.expiryHours ?? DEFAULT_APPROVAL_EXPIRY_H;
      const actionId = await o.inbox.open({
        tenantId: o.tenantId,
        runId: req.runId,
        seq: req.seq,
        tool: req.tool,
        invokerUserId: o.invokerUserId,
        approvalDuty: o.approvalDuty(req.tool),
        amount: req.amount,
        selfApprovalLimit: o.selfApprovalLimit,
        ...(o.expiryHours !== undefined ? { expiryHours: o.expiryHours } : {}),
        // req.args holds real values (the engine restored them for the commit call); the inbox
        // row must stay PII-free, so only the tool, the confidence and the amount are stored.
        proposalJson: JSON.stringify({ tool: req.tool, confidence: req.confidence, amount: req.amount }),
      });
      o.onOpened?.(actionId);
      for (;;) {
        const found = await o.inbox.find(o.tenantId, req.runId, actionId);
        if (found === undefined) throw new Error(`approval ${actionId} disappeared`);
        if (found.decided !== undefined) return toDecision(found.decided);
        if (isApprovalExpired(requestedAtMs, o.nowMs(), expiryHours)) {
          const stored = await o.inbox.record(o.tenantId, req.runId, actionId, {
            decision: "REJECTED",
            reason: "EXPIRED",
            code: "AGT-0208",
            approverUserId: EXPIRY_ACTOR,
            decidedAt: o.now(),
          });
          return toDecision(stored);
        }
        await sleep(pollMs);
      }
    },
  };
}

function toDecision(d: { decision: "APPROVED" | "REJECTED"; reason?: "EXPIRED"; approverUserId: string }): ApprovalDecision {
  return {
    decision: d.decision,
    approverUserId: d.reason === "EXPIRED" ? null : d.approverUserId,
    ...(d.reason !== undefined ? { reason: d.reason } : {}),
  };
}
