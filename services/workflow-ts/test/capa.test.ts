import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { WorkflowFailedError, type WorkflowHandle } from "@temporalio/client";
import { bundleWorkflowCode, Worker, type WorkflowBundle } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import { createCapaActivities } from "../src/capaActivities.js";
import { createServiceTokenSigner } from "../src/serviceToken.js";
import {
  capaEffectivenessSignal,
  capaStateQuery,
  type CapaState,
  capaStepDoneSignal,
  capaWorkflow,
  capaWorkflowId,
  validateCapaInput,
  type CapaInput,
  type EffectivenessResult,
} from "../src/workflows.js";
import { FakeQuality, NCR_ID } from "./fakeQuality.js";

const DAY_MS = 24 * 60 * 60 * 1000;

const input: CapaInput = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  legalEntityId: "00000000-0000-0000-0000-0000000000a1",
  ncrId: NCR_ID,
  startedBy: "quality-lead",
  effectivenessCheckDays: 30,
  stepTimeoutMs: 14 * DAY_MS,
  effectivenessTimeoutMs: 14 * DAY_MS,
  maxReopens: 1,
};

const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 5).toString("base64") });

describe("CAPA workflow", () => {
  let environment: TestWorkflowEnvironment;
  let workflowBundle: WorkflowBundle;
  let sequence = 0;

  beforeAll(async () => {
    environment = await TestWorkflowEnvironment.createTimeSkipping();
    workflowBundle = await bundleWorkflowCode({ workflowsPath: fileURLToPath(new URL("../src/workflows.ts", import.meta.url)) });
  }, 120_000);

  afterAll(async () => {
    await environment?.teardown();
  });

  type Handle = WorkflowHandle<typeof capaWorkflow>;

  async function run(fake: FakeQuality, drive: (handle: Handle) => Promise<void>, override: Partial<CapaInput> = {}) {
    sequence += 1;
    const taskQueue = `capa-${sequence}`;
    fake.skewMs = (await environment.currentTimeMs()) - Date.now();
    const worker = await Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowBundle,
      maxCachedWorkflows: 0,
      activities: { ...createCapaActivities("http://quality", signer, fake.fetch) },
    });
    return worker.runUntil(async () => {
      const args = { ...input, ...override };
      const handle = await environment.client.workflow.start(capaWorkflow, { workflowId: `${capaWorkflowId(args.ncrId)}:${taskQueue}`, taskQueue, args: [args] });
      await drive(handle);
      return handle.result();
    });
  }

  const steps = (handle: Handle, from: number, to: number, actor = "engineer") => {
    const sends = [];
    for (let step = from; step <= to; step += 1) sends.push(() => handle.signal(capaStepDoneSignal, { step, notes: `D${step} done`, actor }));
    return sends.reduce((chain, send) => chain.then(send), Promise.resolve());
  };
  const until = async (handle: Handle, ready: (state: CapaState) => boolean) => {
    for (let attempt = 0; attempt < 400; attempt += 1) {
      if (ready(await handle.query(capaStateQuery))) return;
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    throw new Error("workflow never reached the expected state");
  };
  const whenDue = async (fake: FakeQuality, handle: Handle, previouslyReopened = 0) => {
    await until(handle, (state) => state.status === "effectiveness_pending" && state.reopened === previouslyReopened);
    await environment.sleep(31 * DAY_MS);
    fake.skewMs = (await environment.currentTimeMs()) - Date.now();
    await until(handle, (state) => state.status === "awaiting_effectiveness");
  };
  const check = (handle: Handle, actor: string, result: EffectivenessResult = "EFFECTIVE") =>
    handle.signal(capaEffectivenessSignal, { result, note: `${result} per audit`, actor });
  const closeOut = async (fake: FakeQuality, handle: Handle, actor = "reviewer") => {
    await steps(handle, 1, 7);
    await whenDue(fake, handle);
    await check(handle, actor);
  };

  it("walks D1 to D7, waits out the effectiveness period and closes on an independent EFFECTIVE check", async () => {
    const fake = new FakeQuality();
    const result = await run(fake, (handle) => closeOut(fake, handle));
    expect(result).toEqual({ ncrId: NCR_ID, tenantId: input.tenantId, status: "closed", currentStep: 8, reopened: 0, implementer: "engineer", refusedChecks: 0 });
    expect(fake.ncr).toMatchObject({ status: "CLOSED", correction: { status: "CLOSED", effectivenessResult: "EFFECTIVE" } });
    expect(fake.effects).toEqual({ corrections: 1, stepWrites: 7, effectivenessChecks: 1 });
    expect(fake.tokens.map((claims) => claims.act)).toEqual([
      "quality-lead",
      "quality-lead",
      ...Array.from({ length: 7 }, () => "engineer").flatMap((actor) => [actor, actor]),
      "reviewer",
      "reviewer",
    ]);
    expect(new Set(fake.tokens.map((claims) => claims.duties.join()))).toEqual(new Set(["quality.capa.execute"]));
  }, 60_000);

  it("takes no authorization decision from the signal: the service refuses the D7 implementer and the workflow keeps waiting", async () => {
    const fake = new FakeQuality();
    const result = await run(fake, async (handle) => {
      await steps(handle, 1, 7);
      await whenDue(fake, handle);
      await check(handle, "engineer");
    });
    expect(result).toMatchObject({ status: "stalled", stalledAt: "effectiveness", refusedChecks: 1 });
    expect(fake.effects.effectivenessChecks).toBe(0);
    expect(fake.calls["check-effectiveness"]).toBe(1);
  }, 60_000);

  it("then accepts an independent check after the refused one", async () => {
    const fake = new FakeQuality();
    const result = await run(fake, async (handle) => {
      await steps(handle, 1, 7);
      await whenDue(fake, handle);
      await check(handle, "engineer");
      await until(handle, (state) => state.refusedChecks === 1);
      await check(handle, "reviewer");
    });
    expect(result).toMatchObject({ status: "closed", refusedChecks: 1 });
  }, 60_000);

  it("drops an effectiveness check that arrives before the due date", async () => {
    const fake = new FakeQuality();
    const result = await run(fake, async (handle) => {
      await steps(handle, 1, 7);
      await until(handle, (state) => state.status === "effectiveness_pending");
      await check(handle, "early-reviewer");
      await whenDue(fake, handle);
      await check(handle, "reviewer");
    });
    expect(result.status).toBe("closed");
    expect(fake.calls["check-effectiveness"]).toBe(1);
    expect(fake.tokens.at(-1)?.act).toBe("reviewer");
  }, 60_000);

  it("reopens at D4 after a NOT_EFFECTIVE check, drops checks left from that round, and closes on the next round's check", async () => {
    const fake = new FakeQuality();
    const result = await run(fake, async (handle) => {
      await steps(handle, 1, 7);
      await whenDue(fake, handle);
      await check(handle, "reviewer", "NOT_EFFECTIVE");
      await check(handle, "reviewer", "EFFECTIVE");
      await until(handle, (state) => state.reopened === 1 && state.currentStep === 4);
      await steps(handle, 4, 7);
      await whenDue(fake, handle, 1);
      await check(handle, "reviewer");
    });
    expect(result).toMatchObject({ status: "closed", reopened: 1 });
    expect(fake.effects).toEqual({ corrections: 1, stepWrites: 11, effectivenessChecks: 2 });
    expect(fake.ncr.correction?.effectivenessResult).toBe("EFFECTIVE");
  }, 60_000);

  it("escalates instead of looping forever once the reopen budget is spent", async () => {
    const fake = new FakeQuality();
    const result = await run(
      fake,
      async (handle) => {
        await steps(handle, 1, 7);
        await whenDue(fake, handle);
        await check(handle, "reviewer", "NOT_EFFECTIVE");
      },
      { maxReopens: 0 },
    );
    expect(result).toMatchObject({ status: "escalated", reopened: 1 });
    expect(fake.ncr.status).toBe("IN_CORRECTION");
  }, 60_000);

  it("stalls and says where when the next step never arrives", async () => {
    const fake = new FakeQuality();
    const result = await run(fake, (handle) => steps(handle, 1, 2));
    expect(result).toMatchObject({ ncrId: NCR_ID, status: "stalled", currentStep: 3, reopened: 0, stalledAt: "step" });
    expect(fake.effects.stepWrites).toBe(2);
  }, 60_000);

  it("applies steps in order even when the signals arrive out of order", async () => {
    const fake = new FakeQuality();
    const result = await run(fake, async (handle) => {
      for (const step of [3, 1, 2, 4, 6, 5, 7]) await handle.signal(capaStepDoneSignal, { step, notes: `D${step}`, actor: "engineer" });
      await whenDue(fake, handle);
      await check(handle, "reviewer");
    });
    expect(result.status).toBe("closed");
    expect(fake.effects.stepWrites).toBe(7);
  }, 60_000);

  it("retries an effectiveness check the service says is not due yet", async () => {
    const fake = new FakeQuality();
    fake.refuse("check-effectiveness", 422, "QM-0174", 2);
    const result = await run(fake, (handle) => closeOut(fake, handle));
    expect(result.status).toBe("closed");
    expect(fake.calls["check-effectiveness"]).toBe(3);
    expect(fake.effects.effectivenessChecks).toBe(1);
  }, 60_000);

  it.each(["start", "complete-step", "check-effectiveness"] as const)("a reply lost after %s committed does not repeat its effect", async (action) => {
    const fake = new FakeQuality();
    let lost = false;
    fake.after(action, async () => {
      if (lost) return;
      lost = true;
      throw new TypeError("fetch failed");
    });
    const result = await run(fake, (handle) => closeOut(fake, handle));
    expect(result.status).toBe("closed");
    expect(fake.effects).toEqual({ corrections: 1, stepWrites: 7, effectivenessChecks: 1 });
    expect(lost).toBe(true);
  }, 60_000);

  it("re-reads the ETag and retries when another writer moved the version", async () => {
    const fake = new FakeQuality();
    const result = await run(fake, async (handle) => {
      fake.staleOnce();
      await closeOut(fake, handle);
    });
    expect(result.status).toBe("closed");
    expect(fake.effects.stepWrites).toBe(7);
  }, 60_000);

  it("fails the workflow on a rule the service refuses terminally", async () => {
    const fake = new FakeQuality();
    fake.refuse("complete-step", 400, "COM-0001");
    await expect(run(fake, (handle) => steps(handle, 1, 1))).rejects.toBeInstanceOf(WorkflowFailedError);
    expect(fake.calls["complete-step"]).toBe(1);
  }, 60_000);

  it("fails when the nonconformance is not approved, so no correction can start", async () => {
    const fake = new FakeQuality();
    fake.ncr.status = "CLOSED";
    await expect(run(fake, async () => {})).rejects.toBeInstanceOf(WorkflowFailedError);
    expect(fake.effects.corrections).toBe(0);
  }, 60_000);

  it("reports its state to a query", async () => {
    const fake = new FakeQuality();
    let seen: unknown;
    await run(fake, async (handle) => {
      await steps(handle, 1, 2);
      seen = await handle.query(capaStateQuery);
    });
    expect(seen).toMatchObject({ ncrId: NCR_ID, status: "in_correction" });
  }, 60_000);

  it("refuses to start without its people or with non-positive timeouts", () => {
    expect(() => validateCapaInput({ ...input, startedBy: "" })).toThrow(/required/);
    expect(() => validateCapaInput({ ...input, stepTimeoutMs: 0 })).toThrow(/positive/);
    expect(() => validateCapaInput({ ...input, maxReopens: -1 })).toThrow(/maxReopens/);
    expect(() => validateCapaInput(input)).not.toThrow();
  });
});
