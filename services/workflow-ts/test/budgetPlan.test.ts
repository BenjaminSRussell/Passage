import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { bundleWorkflowCode, Worker, type WorkflowBundle } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy, type Client } from "@temporalio/client";
import { createBudgetPlanActivities } from "../src/budgetPlanActivities.js";
import { handlePlanSubmitted, planSubmittedSource, planSubmittedType, toBudgetPlanInput } from "../src/budgetPlanTriggers.js";
import { createServiceTokenSigner } from "../src/serviceToken.js";
import {
  budgetPlanDecisionSignal,
  budgetPlanStateQuery,
  budgetPlanWorkflow,
  budgetPlanWorkflowId,
  REFUSED_DECISIONS_BEFORE_RESTART,
  validateBudgetPlanInput,
  type BudgetPlanDecision,
  type BudgetPlanInput,
} from "../src/workflows.js";
import { FakeBudgeting, PLAN_ID, SCENARIO_ID, UNIT_A, UNIT_APPROVE, UNIT_B } from "./fakeBudgeting.js";

const TENANT = "00000000-0000-0000-0000-000000000001";
const ENTITY = "00000000-0000-0000-0000-0000000000a1";

const input: BudgetPlanInput = {
  tenantId: TENANT,
  legalEntityId: ENTITY,
  planId: PLAN_ID,
  submission: 1,
  requester: "pat",
};

type Handle = {
  signal: (...args: any[]) => Promise<void>;
  query: (...args: any[]) => Promise<any>;
};
const review = (handle: Handle, actor: string, orgUnitId: string, scope = [ENTITY]) =>
  handle.signal(budgetPlanDecisionSignal, {
    decision: "sign-off",
    stage: "review",
    orgUnitId,
    actor,
    scope,
  } satisfies BudgetPlanDecision);
const approve = (handle: Handle, actor: string, scope = [ENTITY]) =>
  handle.signal(budgetPlanDecisionSignal, {
    decision: "sign-off",
    stage: "approve",
    orgUnitId: UNIT_APPROVE,
    scenarioId: SCENARIO_ID,
    actor,
    scope,
  } satisfies BudgetPlanDecision);
const reject = (handle: Handle, actor: string, reason = "too high") =>
  handle.signal(budgetPlanDecisionSignal, {
    decision: "reject",
    reason,
    actor,
    scope: [ENTITY],
  } satisfies BudgetPlanDecision);

async function untilRefused(handle: Handle, count: number): Promise<void> {
  for (let i = 0; i < 200; i += 1) {
    if ((await handle.query(budgetPlanStateQuery)).refusedDecisions >= count) return;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`the workflow did not refuse ${count} decisions`);
}

const signer = createServiceTokenSigner({
  seedBase64: Buffer.alloc(32, 7).toString("base64"),
});

describe("budget plan approval workflow", () => {
  let environment: TestWorkflowEnvironment;
  let workflowBundle: WorkflowBundle;
  let sequence = 0;

  beforeAll(async () => {
    environment = await TestWorkflowEnvironment.createTimeSkipping();
    workflowBundle = await bundleWorkflowCode({
      workflowsPath: fileURLToPath(new URL("../src/workflows.ts", import.meta.url)),
    });
  }, 120_000);

  afterAll(async () => {
    await environment?.teardown();
  });

  async function run(fake: FakeBudgeting, drive: (handle: Awaited<ReturnType<typeof start>>) => Promise<void>, override: Partial<BudgetPlanInput> = {}) {
    sequence += 1;
    const taskQueue = `budget-plan-${sequence}`;
    const worker = await Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowBundle,
      maxCachedWorkflows: 0,
      activities: {
        ...createBudgetPlanActivities("http://budgeting", signer, fake.fetch),
      },
    });
    return worker.runUntil(async () => {
      const handle = await start(taskQueue, { ...input, ...override });
      await drive(handle);
      return environment.client.workflow.getHandle(handle.workflowId).result();
    });
  }

  function start(taskQueue: string, args: BudgetPlanInput) {
    return environment.client.workflow.start(budgetPlanWorkflow, {
      workflowId: `${budgetPlanWorkflowId(args.planId, args.submission)}:${taskQueue}`,
      taskQueue,
      args: [args],
    });
  }

  it("carries the plan through review by every org unit and approval, and the approved scenario becomes the register", async () => {
    const fake = new FakeBudgeting();
    const result = await run(fake, async (handle) => {
      await review(handle, "rev-a", UNIT_A);
      await review(handle, "rev-b", UNIT_B);
      await approve(handle, "boss");
    });
    expect(result).toMatchObject({
      planId: PLAN_ID,
      status: "approved",
      approvedScenarioId: SCENARIO_ID,
      registerEntryId: expect.any(String),
      decidedBy: "boss",
      refusedDecisions: 0,
    });
    expect(fake.plan.status).toBe("Approved");
    expect(fake.effects).toBe(1);
    expect(fake.calls).toEqual({ "sign-off": 3, reject: 0 });
    expect(fake.tokens.map((claims) => [claims.act, claims.tid, claims.le, claims.duties])).toEqual([
      ["rev-a", TENANT, ENTITY, ["budget.plan.workflow"]],
      ["rev-b", TENANT, ENTITY, ["budget.plan.workflow"]],
      ["boss", TENANT, ENTITY, ["budget.plan.workflow"]],
    ]);
    expect(fake.keys).toEqual([
      `budget-plan:${PLAN_ID}:sign-off:review:${UNIT_A}:1:rev-a`,
      `budget-plan:${PLAN_ID}:sign-off:review:${UNIT_B}:1:rev-b`,
      `budget-plan:${PLAN_ID}:sign-off:approve:${UNIT_APPROVE}:1:boss`,
    ]);
  }, 60_000);

  it("ends with the plan back in Draft when an owner of the open stage rejects it", async () => {
    const fake = new FakeBudgeting();
    const result = await run(fake, async (handle) => {
      await review(handle, "rev-a", UNIT_A);
      await review(handle, "rev-b", UNIT_B);
      await reject(handle, "boss", "scenario too optimistic");
    });
    expect(result).toMatchObject({ status: "rejected", decidedBy: "boss" });
    expect(fake.plan.status).toBe("Draft");
    expect(fake.owners.every((o) => !o.signedBy)).toBe(true);
    expect(fake.effects).toBe(0);
  }, 60_000);

  it("takes no authorization decision from the signal: budgeting refuses a non-owner and the submitter, and the workflow keeps waiting", async () => {
    const fake = new FakeBudgeting();
    const result = await run(fake, async (handle) => {
      await review(handle, "mallory", UNIT_A);
      await review(handle, "pat", UNIT_A);
      await review(handle, "rev-b", UNIT_A);
      await untilRefused(handle, 3);
      await review(handle, "rev-a", UNIT_A);
      await review(handle, "rev-b", UNIT_B);
      await approve(handle, "boss");
    });
    expect(result).toMatchObject({ status: "approved", refusedDecisions: 3 });
    expect(fake.calls["sign-off"]).toBe(6);
  }, 60_000);

  it("refuses an approval signalled before the review is complete and waits for the stages in order", async () => {
    const fake = new FakeBudgeting();
    const result = await run(fake, async (handle) => {
      await approve(handle, "boss");
      await untilRefused(handle, 1);
      expect((await handle.query(budgetPlanStateQuery)).status).toBe("awaiting_review");
      await review(handle, "rev-a", UNIT_A);
      await review(handle, "rev-b", UNIT_B);
      await approve(handle, "boss");
    });
    expect(result).toMatchObject({ status: "approved", refusedDecisions: 1 });
    expect(fake.effects).toBe(1);
  }, 60_000);

  it("ignores a signal for a submission the plan has moved past, and the next valid one completes", async () => {
    const fake = new FakeBudgeting();
    fake.plan.submission = 2;
    const result = await run(fake, async (handle) => {
      await review(handle, "rev-a", UNIT_A);
      await untilRefused(handle, 1);
      fake.plan.submission = 1;
      await review(handle, "rev-a", UNIT_A);
      await review(handle, "rev-b", UNIT_B);
      await approve(handle, "boss");
    });
    expect(result).toMatchObject({ status: "approved", refusedDecisions: 1 });
  }, 60_000);

  it("never mints a token for a legal entity outside the decider's scope, so nothing reaches budgeting", async () => {
    const fake = new FakeBudgeting();
    const result = await run(fake, async (handle) => {
      await review(handle, "rev-a", UNIT_A, ["00000000-0000-0000-0000-0000000000ff"]);
      await untilRefused(handle, 1);
      expect(fake.calls["sign-off"]).toBe(0);
      await review(handle, "rev-a", UNIT_A);
      await review(handle, "rev-b", UNIT_B);
      await approve(handle, "boss");
    });
    expect(result).toMatchObject({ status: "approved", refusedDecisions: 1 });
  }, 60_000);

  it.each(["sign-off"] as const)(
    "a reply lost after %s committed re-runs the step under the same key without a second effect",
    async (action) => {
      const fake = new FakeBudgeting();
      let lost = false;
      fake.after(action, async () => {
        if (lost) return;
        lost = true;
        throw new TypeError("fetch failed");
      });
      const result = await run(fake, async (handle) => {
        await review(handle, "rev-a", UNIT_A);
        await review(handle, "rev-b", UNIT_B);
        await approve(handle, "boss");
      });
      expect(result.status).toBe("approved");
      expect(fake.calls[action]).toBe(4);
      expect(new Set(fake.keys).size).toBe(3);
      expect(fake.effects).toBe(1);
    },
    60_000,
  );

  it("retries a server fault and still completes", async () => {
    const fake = new FakeBudgeting();
    fake.refuse("sign-off", 503, "BUD-9999", 2);
    const result = await run(fake, async (handle) => {
      await review(handle, "rev-a", UNIT_A);
      await review(handle, "rev-b", UNIT_B);
      await approve(handle, "boss");
    });
    expect(result.status).toBe("approved");
    expect(fake.calls["sign-off"]).toBe(5);
  }, 60_000);

  it("restarts its history after a run of refused decisions and keeps the stage it was in", async () => {
    const fake = new FakeBudgeting();
    const result = await run(fake, async (handle) => {
      await review(handle, "rev-a", UNIT_A);
      await review(handle, "rev-b", UNIT_B);
      for (let i = 0; i < REFUSED_DECISIONS_BEFORE_RESTART; i += 1) await review(handle, "mallory", UNIT_A);
      for (let i = 0; i < 400; i += 1) {
        const state = await handle.query(budgetPlanStateQuery);
        if (state.refusedDecisions === 0 && fake.calls["sign-off"] >= 2 + REFUSED_DECISIONS_BEFORE_RESTART) break;
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      expect((await handle.query(budgetPlanStateQuery)).status).toBe("awaiting_approval");
      await approve(handle, "boss");
    });
    expect(result).toMatchObject({ status: "approved", refusedDecisions: 0 });
    expect(fake.effects).toBe(1);
  }, 120_000);

  it("exposes the awaiting state to a query", async () => {
    const fake = new FakeBudgeting();
    let seen: unknown;
    await run(fake, async (handle) => {
      seen = await handle.query(budgetPlanStateQuery);
      await reject(handle, "rev-a");
    });
    expect(seen).toEqual({
      planId: PLAN_ID,
      tenantId: TENANT,
      legalEntityId: ENTITY,
      submission: 1,
      requester: "pat",
      status: "awaiting_review",
      refusedDecisions: 0,
    });
  }, 60_000);

  it("refuses to start without the tenant, plan or requester, or with no submission", () => {
    expect(() => validateBudgetPlanInput({ ...input, requester: "" })).toThrow(/required/);
    expect(() => validateBudgetPlanInput({ ...input, submission: 0 })).toThrow(/submission/);
    expect(() => validateBudgetPlanInput(input)).not.toThrow();
  });
});

describe("plan.submitted trigger", () => {
  const event = {
    type: planSubmittedType,
    source: planSubmittedSource,
    tenantid: TENANT,
    data: {
      plan_id: PLAN_ID,
      legal_entity_id: ENTITY,
      submission: 2,
      submitted_by: "pat",
    },
  };

  it("starts one workflow per submission and takes the requester from the event", async () => {
    const start = vi.fn().mockResolvedValue({});
    const client = { workflow: { start } } as unknown as Pick<Client, "workflow">;
    expect(await handlePlanSubmitted(client, "passage-approvals", event)).toEqual({ workflowId: `budget-plan:${PLAN_ID}:2`, started: true });
    expect(start).toHaveBeenCalledWith(budgetPlanWorkflow, {
      workflowId: `budget-plan:${PLAN_ID}:2`,
      taskQueue: "passage-approvals",
      args: [
        {
          tenantId: TENANT,
          legalEntityId: ENTITY,
          planId: PLAN_ID,
          submission: 2,
          requester: "pat",
        },
      ],
      workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE,
    });
  });

  it("reports a redelivered event as already started", async () => {
    const start = vi.fn().mockRejectedValue(new WorkflowExecutionAlreadyStartedError("started", "id", "budgetPlanWorkflow"));
    const client = { workflow: { start } } as unknown as Pick<Client, "workflow">;
    expect(await handlePlanSubmitted(client, "q", event)).toMatchObject({
      started: false,
    });
  });

  it("refuses an event from another source or of another type", () => {
    expect(() => toBudgetPlanInput({ ...event, source: "/passage/other" })).toThrow(/only from/);
    expect(() =>
      toBudgetPlanInput({
        ...event,
        type: "dev.passage.budget.plan.approved.v1",
      }),
    ).toThrow(/expected/);
  });
});
