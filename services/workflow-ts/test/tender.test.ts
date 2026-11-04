import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { WorkflowFailedError, type WorkflowHandle, type WorkflowHandleWithFirstExecutionRunId } from "@temporalio/client";
import { ApplicationFailure } from "@temporalio/activity";
import { bundleWorkflowCode, Worker, type WorkflowBundle } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import { createTenderActivities } from "../src/tenderActivities.js";
import { createServiceTokenSigner } from "../src/serviceToken.js";
import { tenderResponseSignal, tenderStateQuery, tenderWorkflow, tenderWorkflowId, validateTenderInput, type TenderInput, type TenderState } from "../src/workflows.js";
import { CARRIERS, FakeTransport, LOAD_ID } from "./fakeTransport.js";

const MINUTE_MS = 60_000;
const TIMEOUT_MIN = 30;

const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 9).toString("base64") });

const input: TenderInput = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  legalEntityId: "00000000-0000-0000-0000-0000000000a1",
  legalEntityScope: ["00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"],
  loadId: LOAD_ID,
  carriers: CARRIERS.map((carrierId) => ({ carrierId })),
  timeoutMin: TIMEOUT_MIN,
  requestedBy: "planner",
};

describe("tender workflow", () => {
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

  type Handle = WorkflowHandle<typeof tenderWorkflow>;

  async function run(fake: FakeTransport, drive: (handle: Handle) => Promise<void>, override: Partial<TenderInput> = {}, prepare?: (fake: FakeTransport) => void) {
    sequence += 1;
    const taskQueue = `tender-${sequence}`;
    fake.skewMs = (await environment.currentTimeMs()) - Date.now();
    prepare?.(fake);
    const worker = await Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowBundle,
      maxCachedWorkflows: 0,
      activities: createTenderActivities("http://transport", signer, fake.fetch),
    });
    return worker.runUntil(async () => {
      const handle = await environment.client.workflow.start(tenderWorkflow, { workflowId: `${tenderWorkflowId(input.tenantId, LOAD_ID)}:${taskQueue}`, taskQueue, args: [{ ...input, ...override }] });
      await drive(handle);
      return handle.result();
    });
  }

  const until = async (handle: Handle, ready: (state: TenderState) => boolean) => {
    for (let attempt = 0; attempt < 400; attempt += 1) {
      if (ready(await handle.query(tenderStateQuery))) return;
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    throw new Error("workflow never reached the expected state");
  };
  const pass = async (fake: FakeTransport, minutes: number) => {
    await environment.sleep(minutes * MINUTE_MS);
    fake.skewMs = (await environment.currentTimeMs()) - Date.now();
  };

  it("offers the next carrier when the first times out, and finishes when it accepts", async () => {
    const fake = new FakeTransport();
    const result = await run(fake, async (handle) => {
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[0]);
      await pass(fake, TIMEOUT_MIN + 1);
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[1]);
      fake.respond(CARRIERS[1]!, true);
      await handle.signal(tenderResponseSignal);
    });
    expect(result).toMatchObject({ status: "accepted", acceptedCarrierId: CARRIERS[1], attempted: [CARRIERS[0], CARRIERS[1]] });
    expect(fake.attempts.map((attempt) => attempt.response)).toEqual(["EXPIRED", "ACCEPTED"]);
    expect(fake.starts).toBe(1);
  }, 60_000);

  it("cascades past a decline without waiting out its timeout", async () => {
    const fake = new FakeTransport();
    const result = await run(fake, async (handle) => {
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[0]);
      fake.respond(CARRIERS[0]!, false);
      await handle.signal(tenderResponseSignal);
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[1]);
      fake.respond(CARRIERS[1]!, false);
      await handle.signal(tenderResponseSignal);
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[2]);
      fake.respond(CARRIERS[2]!, true);
      await handle.signal(tenderResponseSignal);
    });
    expect(result).toMatchObject({ status: "accepted", acceptedCarrierId: CARRIERS[2] });
    expect(fake.attempts.map((attempt) => attempt.response)).toEqual(["DECLINED", "DECLINED", "ACCEPTED"]);
    expect(fake.sweeps).toBe(0);
  }, 60_000);

  it("escalates to the planner when every carrier declines or times out", async () => {
    const fake = new FakeTransport();
    const result = await run(fake, async (handle) => {
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[0]);
      fake.respond(CARRIERS[0]!, false);
      await handle.signal(tenderResponseSignal);
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[1]);
      await pass(fake, TIMEOUT_MIN + 1);
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[2]);
      await pass(fake, TIMEOUT_MIN + 1);
    });
    expect(result).toMatchObject({ status: "escalated", attempted: CARRIERS });
    expect(result.acceptedCarrierId).toBeUndefined();
    expect(fake.status).toBe("TENDER_FAILED");
    expect(fake.attempts.map((attempt) => attempt.response)).toEqual(["DECLINED", "EXPIRED", "EXPIRED"]);
  }, 60_000);

  it("calls transport with a workflow-ts service token for the requesting user in the load's legal entity", async () => {
    const fake = new FakeTransport();
    await run(fake, async (handle) => {
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[0]);
      fake.respond(CARRIERS[0]!, true);
      await handle.signal(tenderResponseSignal);
    });
    expect(fake.tokens.length).toBeGreaterThan(1);
    expect(new Set(fake.tokens.map((claims) => JSON.stringify({ tid: claims.tid, le: claims.le, act: claims.act, aud: claims.aud, duties: claims.duties })))).toEqual(
      new Set([JSON.stringify({ tid: input.tenantId, le: input.legalEntityId, act: "planner", aud: "transport", duties: ["transport.tender.execute"] })]),
    );
  }, 60_000);

  it("keys the tender start on the first execution run so a reset replays the tender and a re-release gets a new one", async () => {
    const fake = new FakeTransport();
    let firstRunId = "";
    await run(fake, async (handle) => {
      firstRunId = (handle as WorkflowHandleWithFirstExecutionRunId).firstExecutionRunId;
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[0]);
      fake.respond(CARRIERS[0]!, true);
      await handle.signal(tenderResponseSignal);
    });
    expect(firstRunId).not.toBe("");
    expect(fake.startKey).toBe(`tender:${firstRunId}`);
  }, 60_000);

  it("does not lose a response that arrives while the tender is being read", async () => {
    const fake = new FakeTransport();
    let readHookDone = false;
    const result = await run(fake, async (handle) => {
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[0]);
      fake.duringNextRead = async () => {
        fake.respond(CARRIERS[0]!, true);
        await handle.signal(tenderResponseSignal);
        readHookDone = true;
      };
      await handle.signal(tenderResponseSignal);
      while (!readHookDone) await new Promise((resolve) => setTimeout(resolve, 10));
    });
    expect(result).toMatchObject({ status: "accepted", acceptedCarrierId: CARRIERS[0] });
    expect(fake.reads).toBe(2);
  }, 60_000);

  it("keeps a multi-day tender alive through a transport outage longer than the activity window", async () => {
    const fake = new FakeTransport();
    const result = await run(fake, async (handle) => {
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[0]);
      fake.readOutage = true;
      await handle.signal(tenderResponseSignal);
      await until(handle, () => fake.reads > 3);
      await pass(fake, 90);
      fake.readOutage = false;
      fake.respond(CARRIERS[0]!, true);
      await handle.signal(tenderResponseSignal);
    }, { timeoutMin: 240 });
    expect(result).toMatchObject({ status: "accepted", acceptedCarrierId: CARRIERS[0] });
  }, 60_000);

  it("adopts the open tender of its load instead of failing on TMS-0114", async () => {
    const fake = new FakeTransport();
    const result = await run(fake, async (handle) => {
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[0]);
      fake.respond(CARRIERS[0]!, true);
      await handle.signal(tenderResponseSignal);
    }, {}, (f) => f.openEarlier("a-planner-or-earlier-run", TIMEOUT_MIN));
    expect(result).toMatchObject({ status: "accepted", acceptedCarrierId: CARRIERS[0] });
    expect(fake.starts).toBe(1);
    expect(fake.lookups).toBe(1);
  }, 60_000);

  it("stalls instead of sweeping forever when the sweep never advances an overdue attempt", async () => {
    const fake = new FakeTransport();
    fake.frozenSweeps = true;
    const result = await run(fake, async (handle) => {
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[0]);
      await pass(fake, TIMEOUT_MIN + 1);
    });
    expect(result).toMatchObject({ status: "stalled", attempted: [CARRIERS[0]] });
    expect(fake.sweeps).toBe(5);
  }, 60_000);

  it("fails the workflow when transport refuses the tender terminally", async () => {
    const fake = new FakeTransport();
    await expect(run(fake, async () => {}, { loadId: "00000000-0000-0000-0000-0000000000d2" })).rejects.toBeInstanceOf(WorkflowFailedError);
    expect(fake.starts).toBe(0);
  }, 60_000);

  it("fails the workflow without a retry loop when the input is invalid", async () => {
    const fake = new FakeTransport();
    await expect(run(fake, async () => {}, { carriers: [] })).rejects.toBeInstanceOf(WorkflowFailedError);
    expect(fake.tokens).toHaveLength(0);
  }, 60_000);

  it("marks invalid input non-retryable", () => {
    const error = (() => {
      try {
        validateTenderInput({ ...input, carriers: [] });
      } catch (thrown) {
        return thrown;
      }
    })();
    expect(error).toBeInstanceOf(ApplicationFailure);
    expect((error as ApplicationFailure).nonRetryable).toBe(true);
  });

  it("fails the workflow when the releasing user's scope does not cover the load's legal entity", async () => {
    const fake = new FakeTransport();
    await expect(run(fake, async () => {}, { legalEntityScope: ["00000000-0000-0000-0000-0000000000a2"] })).rejects.toBeInstanceOf(WorkflowFailedError);
    expect(fake.tokens).toHaveLength(0);
  }, 60_000);

  it("refuses to start without a load, a requester or carriers", () => {
    expect(() => validateTenderInput({ ...input, requestedBy: "" })).toThrow(/required/);
    expect(() => validateTenderInput({ ...input, carriers: [] })).toThrow(/carrier/);
    expect(() => validateTenderInput({ ...input, carriers: [input.carriers[0]!, input.carriers[0]!] })).toThrow(/once/);
    expect(() => validateTenderInput(input)).not.toThrow();
  });
});
