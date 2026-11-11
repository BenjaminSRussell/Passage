import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { handleApprovalDecision } from "../src/api/decision.js";
import { createPersistedApprovalPort, EXPIRY_ACTOR } from "../src/runtime/approvals.js";
import { PgApprovalStore } from "../src/store/approvals.js";
import { usd, makeRequest, INVOKER, APPROVER, TENANT } from "./fakes.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";

const OTHER_TENANT = "00000000-0000-7000-8000-0000000000bb";
const NOW = new Date("2026-10-21T10:00:00.000Z");

describe.runIf(ownerUrl)("persisted approvals in Postgres", () => {
  let fx: Fixture;
  let approvals: PgApprovalStore;
  let n = 0;

  beforeAll(async () => {
    fx = await fixture();
    approvals = new PgApprovalStore(fx.store);
  });
  afterAll(async () => {
    await fx.close();
  });

  /** A run row (RUNNING) and one open approval on it. */
  const openOne = async (tenantId = TENANT, over: { expiryHours?: number } = {}) => {
    n += 1;
    const runId = `run-approval-${n}`;
    await fx.store.startRun(makeRequest({ runId, tenantId }));
    const actionId = await approvals.open({
      tenantId,
      runId,
      seq: 1,
      tool: "passage.ap.invoice.propose_coding",
      invokerUserId: INVOKER,
      approvalDuty: "ap.invoice.propose",
      amount: usd("1200.00"),
      selfApprovalLimit: usd("5000.00"),
      ...(over.expiryHours !== undefined ? { expiryHours: over.expiryHours } : {}),
      proposalJson: JSON.stringify({ tool: "x" }),
    });
    return { runId, actionId };
  };

  it("opens a request and finds it with its duty, amounts and no decision", async () => {
    const { runId, actionId } = await openOne();
    const found = await approvals.find(TENANT, runId, actionId);
    expect(found).toMatchObject({ invokerUserId: INVOKER, approvalDuty: "ap.invoice.propose", amount: usd("1200.00"), selfApprovalLimit: usd("5000.00") });
    expect(found?.decided).toBeUndefined();
    expect(await approvals.pendingForRun(TENANT, runId)).toEqual([expect.objectContaining({ actionId, seq: 1, tool: "passage.ap.invoice.propose_coding" })]);
  });

  it("records the first decision only: a second approver gets the stored one, and the decision endpoint answers 409 for a different decision", async () => {
    const { runId, actionId } = await openOne();
    const first = await approvals.record(TENANT, runId, actionId, { decision: "APPROVED", approverUserId: APPROVER, decidedAt: NOW.toISOString() });
    const second = await approvals.record(TENANT, runId, actionId, { decision: "REJECTED", approverUserId: "someone-else", decidedAt: NOW.toISOString() });
    expect(first).toEqual(second);
    expect(second).toMatchObject({ decision: "APPROVED", approverUserId: APPROVER });
    expect(await approvals.pendingForRun(TENANT, runId)).toEqual([]);
    await expect(
      handleApprovalDecision({
        principal: { userId: "00000000-0000-7000-8000-0000000000d1", tenantId: TENANT, duties: new Set(["ap.invoice.propose"]) },
        runId,
        actionId,
        body: { decision: "REJECTED" },
        store: approvals,
        now: () => NOW,
      }),
    ).rejects.toMatchObject({ status: 409, code: "AGT-0409" });
  });

  it("settles a race between two concurrent approvers on one winner", async () => {
    const { runId, actionId } = await openOne();
    const results = await Promise.all(
      ["a", "b", "c", "d"].map((who) => approvals.record(TENANT, runId, actionId, { decision: who === "a" ? "REJECTED" : "APPROVED", approverUserId: who, decidedAt: NOW.toISOString() })),
    );
    expect(new Set(results.map((r) => r.approverUserId)).size).toBe(1);
  });

  it("never shows or decides another tenant's request (row-level security)", async () => {
    const { runId, actionId } = await openOne();
    expect(await approvals.find(OTHER_TENANT, runId, actionId)).toBeUndefined();
    expect(await approvals.pendingForRun(OTHER_TENANT, runId)).toEqual([]);
    await expect(approvals.record(OTHER_TENANT, runId, actionId, { decision: "APPROVED", approverUserId: APPROVER, decidedAt: NOW.toISOString() })).rejects.toThrow();
    expect((await approvals.find(TENANT, runId, actionId))?.decided).toBeUndefined();
  });

  it("refuses an approval for a run that does not exist and a second request at the same seq", async () => {
    await expect(
      approvals.open({ tenantId: TENANT, runId: "no-such-run", seq: 1, tool: "t", invokerUserId: INVOKER, approvalDuty: "d", amount: usd("1"), selfApprovalLimit: usd("1"), proposalJson: "{}" }),
    ).rejects.toThrow();
    const { runId } = await openOne();
    await expect(
      approvals.open({ tenantId: TENANT, runId, seq: 1, tool: "t", invokerUserId: INVOKER, approvalDuty: "d", amount: usd("1"), selfApprovalLimit: usd("1"), proposalJson: "{}" }),
    ).rejects.toThrow();
  });

  it("the persisted port waits for a human decision recorded in the database", async () => {
    n += 1;
    const runId = `run-port-${n}`;
    await fx.store.startRun(makeRequest({ runId }));
    let opened = "";
    const port = createPersistedApprovalPort({
      inbox: approvals,
      tenantId: TENANT,
      invokerUserId: INVOKER,
      approvalDuty: () => "ap.invoice.propose",
      selfApprovalLimit: usd("5000"),
      nowMs: () => NOW.getTime(),
      now: () => NOW.toISOString(),
      onOpened: (id) => (opened = id),
      sleep: async () => {
        // Between polls the approver decides, as the decision endpoint would.
        await approvals.record(TENANT, runId, opened, { decision: "APPROVED", approverUserId: APPROVER, decidedAt: NOW.toISOString() });
      },
    });
    const decision = await port.request({ runId, seq: 1, tool: "t", args: {}, confidence: 0.9, amount: usd("10"), rationale: "r" });
    expect(decision).toEqual({ decision: "APPROVED", approverUserId: APPROVER });
  });

  it("the persisted port records REJECTED/EXPIRED when nobody decides in time (BR-47.3)", async () => {
    n += 1;
    const runId = `run-expiry-${n}`;
    await fx.store.startRun(makeRequest({ runId }));
    let clock = NOW.getTime();
    let opened = "";
    const port = createPersistedApprovalPort({
      inbox: approvals,
      tenantId: TENANT,
      invokerUserId: INVOKER,
      approvalDuty: () => "ap.invoice.propose",
      selfApprovalLimit: usd("5000"),
      expiryHours: 2,
      nowMs: () => clock,
      now: () => new Date(clock).toISOString(),
      onOpened: (id) => (opened = id),
      sleep: async () => {
        clock += 3 * 3_600_000;
      },
    });
    const decision = await port.request({ runId, seq: 1, tool: "t", args: {}, confidence: 0.9, amount: usd("10"), rationale: "r" });
    expect(decision).toEqual({ decision: "REJECTED", approverUserId: null, reason: "EXPIRED" });
    expect((await approvals.find(TENANT, runId, opened))?.decided).toMatchObject({ reason: "EXPIRED", code: "AGT-0208", approverUserId: EXPIRY_ACTOR });
  });

  it("getRun returns the run row of the tenant and nothing for another tenant", async () => {
    const { runId } = await openOne();
    expect(await fx.store.getRun(TENANT, runId)).toMatchObject({ runId, status: "RUNNING", agentId: "ap_coding" });
    expect(await fx.store.getRun(OTHER_TENANT, runId)).toBeUndefined();
  });
});
