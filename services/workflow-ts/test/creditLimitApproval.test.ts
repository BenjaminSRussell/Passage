import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { bundleWorkflowCode, Worker, type WorkflowBundle } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import type { WorkflowHandle } from "@temporalio/client";
import { createActivities, type PartyClient, type PartyDecision } from "../src/activities.js";
import { changeRequestedType, handleChangeRequested, type ChangeRequestedEvent } from "../src/changeRequested.js";
import { approvalDecisionSignal, approvalStateQuery, approvalWorkflow, type ApprovalDecision, type ApprovalLevel, type ApprovalState } from "../src/workflows.js";

const as = (action: ApprovalDecision["action"], actor: string, scope = ["le-1"]): ApprovalDecision => ({ action, actor, scope });

const SLA_MS = 3_600_000;
const levels: ApprovalLevel[] = [{ id: "finance", approver: "alice", escalationApprover: "cfo", slaMs: SLA_MS }];

function event(changeId: string): ChangeRequestedEvent {
  return {
    type: changeRequestedType,
    tenantid: "tenant-1",
    data: { change_id: changeId, legal_entity_id: "le-1", field: "credit_limit", requested_by: "requester" },
  };
}

describe("credit limit increase approval", () => {
  let environment: TestWorkflowEnvironment;
  let workflowBundle: WorkflowBundle;
  const decisions: PartyDecision[] = [];
  const party: PartyClient = { decide: async (decision) => void decisions.push(decision) };

  beforeAll(async () => {
    environment = await TestWorkflowEnvironment.createTimeSkipping();
    workflowBundle = await bundleWorkflowCode({ workflowsPath: fileURLToPath(new URL("../src/workflows.ts", import.meta.url)) });
  }, 120_000);

  afterAll(async () => {
    await environment?.teardown();
  }, 60_000);

  async function run(taskQueue: string, changeId: string, body: (handle: WorkflowHandle<typeof approvalWorkflow>) => Promise<void>): Promise<void> {
    decisions.length = 0;
    const worker = await Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowBundle,
      maxCachedWorkflows: 0,
      activities: createActivities(party),
    });
    await worker.runUntil(async () => {
      await handleChangeRequested(environment.client, taskQueue, levels, event(changeId));
      await body(environment.client.workflow.getHandle<typeof approvalWorkflow>(`approval:${changeId}`));
    });
  }

  async function waitForState(handle: WorkflowHandle<typeof approvalWorkflow>, ready: (state: ApprovalState) => boolean): Promise<void> {
    for (let attempt = 0; attempt < 200; attempt += 1) {
      if (ready(await handle.query(approvalStateQuery))) return;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    throw new Error("workflow state did not advance");
  }

  it("takes no decision on the party change until an approver approves", async () => {
    await run("credit-blocked", "c-blocked", async (handle) => {
      await waitForState(handle, (state) => state.approver === "alice");
      await handle.signal(approvalDecisionSignal, as("approve", "requester"));
      expect((await handle.query(approvalStateQuery)).status).toBe("pending");
      expect(decisions).toEqual([]);

      await handle.signal(approvalDecisionSignal, as("approve", "alice"));
      expect((await handle.result()).status).toBe("approved");
      expect(decisions).toEqual([{ changeId: "c-blocked", tenantId: "tenant-1", legalEntityId: "le-1", decision: "approved", decidedBy: "alice", scope: ["le-1"] }]);
    });
  }, 60_000);

  it("escalates past the SLA without deciding, then approves only through the escalation approver", async () => {
    await run("credit-timeout", "c-timeout", async (handle) => {
      await waitForState(handle, (state) => state.approver === "alice");
      await environment.sleep(SLA_MS + 1);
      await waitForState(handle, (state) => state.approver === "cfo");
      expect(decisions).toEqual([]);

      await handle.signal(approvalDecisionSignal, as("approve", "alice"));
      expect((await handle.query(approvalStateQuery)).status).toBe("pending");
      await handle.signal(approvalDecisionSignal, as("approve", "cfo"));

      const result = await handle.result();
      expect(result.history.map((entry) => entry.action)).toEqual(["escalated", "approved"]);
      expect(decisions).toEqual([{ changeId: "c-timeout", tenantId: "tenant-1", legalEntityId: "le-1", decision: "approved", decidedBy: "cfo", scope: ["le-1"] }]);
    });
  }, 60_000);

  it("rejects the party change when the escalation approver rejects after the timeout", async () => {
    await run("credit-timeout-reject", "c-timeout-no", async (handle) => {
      await waitForState(handle, (state) => state.approver === "alice");
      await environment.sleep(SLA_MS + 1);
      await waitForState(handle, (state) => state.approver === "cfo");
      await handle.signal(approvalDecisionSignal, as("reject", "cfo"));

      expect((await handle.result()).status).toBe("rejected");
      expect(decisions.map((d) => [d.decision, d.decidedBy])).toEqual([["rejected", "cfo"]]);
    });
  }, 60_000);

  it("ignores a decision from the approver signed for a legal entity outside the change", async () => {
    await run("credit-scope", "c-scope", async (handle) => {
      await waitForState(handle, (state) => state.approver === "alice");
      await handle.signal(approvalDecisionSignal, as("approve", "alice", ["le-2"]));
      await handle.signal(approvalDecisionSignal, as("reject", "bob"));
      const state = await handle.query(approvalStateQuery);
      expect(state).toMatchObject({ status: "pending", approver: "alice", tenantId: "tenant-1", legalEntityId: "le-1", requester: "requester" });
      expect(decisions).toEqual([]);

      await handle.signal(approvalDecisionSignal, as("approve", "alice", ["le-2", "LE-1"]));
      expect((await handle.result()).status).toBe("approved");
      expect(decisions.map((d) => [d.decision, d.decidedBy, d.scope])).toEqual([["approved", "alice", ["le-2", "LE-1"]]]);
    });
  }, 60_000);
});
