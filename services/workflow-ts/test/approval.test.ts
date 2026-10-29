import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { Worker } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import type { WorkflowHandle } from "@temporalio/client";
import {
  approvalStateQuery,
  approveSignal,
  approvalWorkflow,
  delegateSignal,
  validateInput,
  type ApprovalInput,
  type ApprovalState,
} from "../src/workflows.js";

describe("approval workflow", () => {
  let environment: TestWorkflowEnvironment;

  beforeAll(async () => {
    environment = await TestWorkflowEnvironment.createTimeSkipping();
  });

  afterAll(async () => {
    await environment?.teardown();
  });

  it("completes three levels with delegation and SLA escalation", async () => {
    const taskQueue = "approval-test";
    const worker = await Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowsPath: fileURLToPath(new URL("../src/workflows.ts", import.meta.url)),
    });

    await worker.runUntil(async () => {
      const handle = await environment.client.workflow.start(approvalWorkflow, {
        workflowId: "approval:req-1",
        taskQueue,
        args: [{
          requestId: "req-1",
          entityType: "purchase-requisition",
          levels: [
            { id: "manager", approver: "alice", escalationApprover: "finance", slaMs: 3_600_000 },
            { id: "director", approver: "bob", escalationApprover: "finance", slaMs: 3_600_000 },
            { id: "controller", approver: "carol", escalationApprover: "cfo", slaMs: 3_600_000 },
          ],
        }],
      });

      await handle.signal(approveSignal, "alice");
      await waitForLevel(handle, "director");
      await handle.signal(delegateSignal, "bob", "dana");
      await handle.signal(approveSignal, "dana");
      await waitForLevel(handle, "controller");

      await environment.sleep(3_600_001);
      await waitForApprover(handle, "cfo");
      await handle.signal(approveSignal, "cfo");

      const result = await handle.result();
      expect(result.status).toBe("approved");
      expect(result.history).toEqual([
        { action: "approved", levelId: "manager", actor: "alice" },
        { action: "delegated", levelId: "director", actor: "bob", to: "dana" },
        { action: "approved", levelId: "director", actor: "dana" },
        { action: "escalated", levelId: "controller", actor: "carol", to: "cfo" },
        { action: "approved", levelId: "controller", actor: "cfo" },
      ]);
    });
  }, 30_000);
});

describe("validateInput", () => {
  const base: ApprovalInput = {
    requestId: "req-2",
    entityType: "party-change",
    requester: "rita",
    partyChange: { changeId: "chg-1", tenantId: "t1", legalEntityId: "le1" },
    levels: [{ id: "l1", approver: "alice", escalationApprover: "finance", slaMs: 1000 }],
  };

  it("accepts levels that exclude the requester", () => {
    expect(() => validateInput(base)).not.toThrow();
  });

  it("refuses a level whose approver is the requester", () => {
    const levels = [{ ...base.levels[0]!, approver: "rita" }];
    expect(() => validateInput({ ...base, levels })).toThrow(/requester/);
  });

  it("refuses a level whose escalation approver is the requester", () => {
    const levels = [{ ...base.levels[0]!, escalationApprover: "rita" }];
    expect(() => validateInput({ ...base, levels })).toThrow(/requester/);
  });
});

async function waitForLevel(
  handle: WorkflowHandle<typeof approvalWorkflow>,
  levelId: string,
): Promise<void> {
  await waitForState(handle, (state) => state.levelId === levelId);
}

async function waitForApprover(
  handle: WorkflowHandle<typeof approvalWorkflow>,
  approver: string,
): Promise<void> {
  await waitForState(handle, (state) => state.approver === approver);
}

async function waitForState(
  handle: WorkflowHandle<typeof approvalWorkflow>,
  predicate: (state: ApprovalState) => boolean,
): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const state = await handle.query(approvalStateQuery);
    if (predicate(state)) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error("workflow state did not advance");
}
