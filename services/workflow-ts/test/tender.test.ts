import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { WorkflowFailedError, type WorkflowHandle } from "@temporalio/client";
import { bundleWorkflowCode, Worker, type WorkflowBundle } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import { createTenderActivities } from "../src/tenderActivities.js";
import { tenderResponseSignal, tenderStateQuery, tenderWorkflow, tenderWorkflowId, validateTenderInput, type TenderInput, type TenderState } from "../src/workflows.js";
import { CARRIERS, FakeTransport, LOAD_ID } from "./fakeTransport.js";

const MINUTE_MS = 60_000;
const TIMEOUT_MIN = 30;

const input: TenderInput = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  legalEntityId: "00000000-0000-0000-0000-0000000000a1",
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

  async function run(fake: FakeTransport, drive: (handle: Handle) => Promise<void>, override: Partial<TenderInput> = {}) {
    sequence += 1;
    const taskQueue = `tender-${sequence}`;
    fake.skewMs = (await environment.currentTimeMs()) - Date.now();
    const worker = await Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowBundle,
      maxCachedWorkflows: 0,
      activities: createTenderActivities("http://transport", fake.fetch),
    });
    return worker.runUntil(async () => {
      const handle = await environment.client.workflow.start(tenderWorkflow, { workflowId: `${tenderWorkflowId(LOAD_ID)}:${taskQueue}`, taskQueue, args: [{ ...input, ...override }] });
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

  it("calls transport as the requesting user with the tender duty in the load's legal entity", async () => {
    const fake = new FakeTransport();
    await run(fake, async (handle) => {
      await until(handle, (state) => state.pendingCarrierId === CARRIERS[0]);
      fake.respond(CARRIERS[0]!, true);
      await handle.signal(tenderResponseSignal);
    });
    expect(new Set(fake.identities.map((identity) => JSON.stringify(identity)))).toEqual(
      new Set([JSON.stringify({ tenant: input.tenantId, legalEntity: input.legalEntityId, user: "planner" })]),
    );
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

  it("refuses to start without a load, a requester or carriers", () => {
    expect(() => validateTenderInput({ ...input, requestedBy: "" })).toThrow(/required/);
    expect(() => validateTenderInput({ ...input, carriers: [] })).toThrow(/carrier/);
    expect(() => validateTenderInput({ ...input, carriers: [input.carriers[0]!, input.carriers[0]!] })).toThrow(/once/);
    expect(() => validateTenderInput(input)).not.toThrow();
  });
});
