import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { PgApprovalStore } from "../src/store/approvals.js";
import { INVOKER, makeRequest, TENANT, usd } from "./fakes.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";

describe.runIf(ownerUrl)("approval.requested reaches the shared inbox through the outbox (FR-47.8)", () => {
  let fx: Fixture;
  let approvals: PgApprovalStore;
  beforeAll(async () => {
    fx = await fixture();
    approvals = new PgApprovalStore(fx.store);
  });
  afterAll(async () => {
    await fx.close();
  });

  const open = async (runId: string, extra: { confidence?: number; riskScore?: number }) => {
    await fx.store.startRun(makeRequest({ runId, tenantId: TENANT }));
    return approvals.open({
      tenantId: TENANT,
      runId,
      seq: 1,
      tool: "passage.ap.invoice.propose_coding",
      invokerUserId: INVOKER,
      approvalDuty: "ap.invoice.propose",
      amount: usd("1200.00"),
      selfApprovalLimit: usd("5000.00"),
      proposalJson: JSON.stringify({ tool: "passage.ap.invoice.propose_coding", confidence: 0.82, amount: usd("1200.00") }),
      ...extra,
    });
  };
  const rows = async (actionId: string) =>
    (await fx.owner.query<{ type: string; aggregate_id: string; payload: Record<string, unknown> }>("select type, aggregate_id, payload from outbox where aggregate_id = $1", [actionId])).rows;

  it("writes the event with the request, with the ids, the confidence and the risk score and no real values", async () => {
    const actionId = await open("run-outbox-1", { confidence: 0.82, riskScore: 0.18 });
    const [row, ...more] = await rows(actionId);
    expect(more).toHaveLength(0);
    expect(row?.type).toBe("dev.passage.agent.approval.requested.v1");
    expect(row?.payload).toMatchObject({ run_id: "run-outbox-1", action_id: actionId, tool_name: "passage.ap.invoice.propose_coding", confidence: 0.82, risk_score: 0.18 });
  });

  it("announces nothing for a request opened without a confidence (older callers)", async () => {
    expect(await rows(await open("run-outbox-2", {}))).toHaveLength(0);
  });
});
