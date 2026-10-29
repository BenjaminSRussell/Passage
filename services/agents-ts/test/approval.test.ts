import { describe, expect, it } from "vitest";
import { AgentError } from "../src/errors.js";
import { approvalExpiresAtMs, decideApproval, expiredApprovals } from "../src/domain/approval.js";
import { AGENT_EVENT_TYPES, approvalRequestedEvent } from "../src/domain/events.js";

const TENANT = "0190f3a2-7c1d-7b2e-8f00-000000000001";
const HOUR = 3_600_000;
const T0 = Date.UTC(2026, 0, 1);
const USD = (amount: string) => ({ amount, currency: "USD" });
const AP_DUTY = "AgentApprove.ap_coding";

describe("approval expiry (BR-47.3)", () => {
  it("expires exactly at requestedAt + expiry hours, 72 h by default", () => {
    expect(approvalExpiresAtMs(T0, 24)).toBe(T0 + 24 * HOUR);
    expect(approvalExpiresAtMs(T0)).toBe(T0 + 72 * HOUR);
    expect(expiredApprovals([{ actionId: "a", requestedAtMs: T0 }], T0 + 72 * HOUR - 1)).toEqual([]);
    expect(expiredApprovals([{ actionId: "a", requestedAtMs: T0 }], T0 + 72 * HOUR)).toEqual(["a"]);
  });

  it.each([0, 241, 1.5, Number.NaN])("rejects expiry %s outside 1..240 integer hours", (hours) => {
    expect(() => approvalExpiresAtMs(T0, hours)).toThrow("between 1 and 240");
    expect(() => expiredApprovals([], T0, hours)).toThrow("between 1 and 240");
  });

  it("sweeper selects only expired requests, oldest first", () => {
    const pending = [
      { actionId: "a-new", requestedAtMs: T0 + 70 * HOUR },
      { actionId: "a-old", requestedAtMs: T0 },
      { actionId: "a-mid", requestedAtMs: T0 + 10 * HOUR },
    ];
    expect(expiredApprovals(pending, T0 + 80 * HOUR)).toEqual(["a-old"]);
    expect(expiredApprovals(pending, T0 + 82 * HOUR)).toEqual(["a-old", "a-mid"]);
    expect(expiredApprovals(pending, T0 + 1 * HOUR)).toEqual([]);
  });
});

describe("decideApproval", () => {
  const base = {
    requestedAtMs: T0,
    nowMs: T0 + HOUR,
    invokerUserId: "user-invoker",
    approverUserId: "user-approver",
    approverDuties: new Set([AP_DUTY]),
    approvalDuty: AP_DUTY,
    decision: "APPROVED" as const,
    amount: USD("800.00"),
    selfApprovalLimit: USD("500.00"),
  };

  it("passes an approver's decision through within the window", () => {
    expect(decideApproval(base)).toEqual({ decision: "APPROVED" });
    expect(decideApproval({ ...base, decision: "REJECTED" })).toEqual({ decision: "REJECTED" });
  });

  it("expiry overrides a late approval and reports EXPIRED with AGT-0208", () => {
    const out = decideApproval({ ...base, nowMs: T0 + 73 * HOUR });
    expect(out).toEqual({ decision: "REJECTED", reason: "EXPIRED", code: "AGT-0208" });
  });

  it("requires the agent's approval duty (AGT-0203), even on an expired request", () => {
    const codeOf = (patch: Partial<typeof base>): string | undefined => {
      try {
        decideApproval({ ...base, ...patch });
        return undefined;
      } catch (e) {
        return e instanceof AgentError ? e.code : "not an AgentError";
      }
    };
    expect(codeOf({ approverDuties: new Set(["Other"]) })).toBe("AGT-0203");
    expect(codeOf({ approverDuties: new Set(), nowMs: T0 + 100 * HOUR })).toBe("AGT-0203");
  });

  it("honours the tenant's expiry parameter and refuses one out of range", () => {
    expect(decideApproval({ ...base, nowMs: T0 + 2 * HOUR, expiryHours: 1 })).toMatchObject({ reason: "EXPIRED" });
    expect(() => decideApproval({ ...base, expiryHours: 0 })).toThrow("between 1 and 240");
  });

  it("blocks self-approval above the limit (AGT-0209) but not self-rejection", () => {
    const self = { ...base, approverUserId: base.invokerUserId };
    expect(() => decideApproval(self)).toThrow(/AGT-0209/);
    expect(decideApproval({ ...self, decision: "REJECTED" })).toEqual({ decision: "REJECTED" });
  });

  it("allows self-approval at or below the limit", () => {
    const self = { ...base, approverUserId: base.invokerUserId, amount: USD("500.00") };
    expect(decideApproval(self)).toEqual({ decision: "APPROVED" });
  });
});

describe("approvalRequestedEvent", () => {
  const input = {
    tenantId: TENANT,
    runId: "run-1",
    actionId: "act-1",
    toolName: "passage.ap.invoice.propose_coding",
    proposalRedacted: "account 6100 cost centre CC-10 tax S1 for [EMAIL-1]",
    confidence: 0.8,
    riskScore: 0.2,
  };

  it("builds the requested event with the redacted proposal", () => {
    const evt = approvalRequestedEvent(input);
    expect(evt.type).toBe(AGENT_EVENT_TYPES.approvalRequested);
    expect(evt.type).toBe("dev.passage.agent.approval.requested.v1");
    expect(evt.tenantId).toBe(TENANT);
    expect(evt.data).toEqual({
      run_id: "run-1",
      action_id: "act-1",
      tool_name: "passage.ap.invoice.propose_coding",
      proposal: input.proposalRedacted,
      confidence: 0.8,
      risk_score: 0.2,
    });
  });

  it("refuses a proposal that still contains an IBAN", () => {
    expect(() => approvalRequestedEvent({ ...input, proposalRedacted: "pay DE89370400440532013000" })).toThrow(/AGT-0210/);
  });

  it.each([
    ["confidence above 1", { confidence: 1.01 }],
    ["negative risk", { riskScore: -0.1 }],
    ["NaN confidence", { confidence: Number.NaN }],
    ["non-uuid tenant", { tenantId: "tenant-a" }],
    ["blank tool", { toolName: " " }],
    ["blank run id", { runId: "" }],
    ["blank action id", { actionId: " " }],
  ])("rejects %s", (_name, patch) => {
    expect(() => approvalRequestedEvent({ ...input, ...patch })).toThrow();
  });
});
