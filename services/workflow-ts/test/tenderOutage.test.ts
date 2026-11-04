// The seven-day outage bound of tenderWorkflow, in its own file with its own test server. The test lengthens the wait
// between failed one-hour windows to a day, so the week costs eight windows (a few hundred activity attempts) instead
// of about a hundred and fifty; the production defaults are pinned below.
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { bundleWorkflowCode, Worker, type WorkflowBundle } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import { createTenderActivities } from "../src/tenderActivities.js";
import { createServiceTokenSigner } from "../src/serviceToken.js";
import {
  MAX_OUTAGE_MS,
  OUTAGE_RETRY_MS,
  tenderResponseSignal,
  tenderStateQuery,
  tenderWorkflow,
  tenderWorkflowId,
  validateTenderInput,
  type TenderInput,
} from "../src/workflows.js";
import { CARRIERS, FakeTransport, LOAD_ID } from "./fakeTransport.js";

const MINUTE_MS = 60_000;
const DAY_MS = 24 * 60 * MINUTE_MS;
const LE = "00000000-0000-0000-0000-0000000000a1";

const input: TenderInput = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  legalEntityId: LE,
  legalEntityScope: [LE],
  loadId: LOAD_ID,
  carriers: CARRIERS.map((carrierId) => ({ carrierId })),
  timeoutMin: 1440,
  requestedBy: "planner",
};

describe("tender outage defaults", () => {
  it("waits five minutes between failed windows and gives up after seven days unless told otherwise", () => {
    expect(OUTAGE_RETRY_MS).toBe(5 * MINUTE_MS);
    expect(MAX_OUTAGE_MS).toBe(7 * DAY_MS);
    expect(() => validateTenderInput({ ...input, outageRetryMs: 1_000 })).toThrow(/at least one minute/);
    expect(() => validateTenderInput({ ...input, outageRetryMs: DAY_MS })).not.toThrow();
  });
});

describe("tender workflow during a transport outage", () => {
  let environment: TestWorkflowEnvironment;
  let workflowBundle: WorkflowBundle;

  beforeAll(async () => {
    environment = await TestWorkflowEnvironment.createTimeSkipping();
    workflowBundle = await bundleWorkflowCode({ workflowsPath: fileURLToPath(new URL("../src/workflows.ts", import.meta.url)) });
  }, 300_000);

  afterAll(async () => {
    await environment?.teardown();
  }, 60_000);

  async function eventually(ready: () => Promise<boolean>): Promise<void> {
    const deadline = Date.now() + 120_000;
    while (!(await ready())) {
      if (Date.now() > deadline) throw new Error("workflow never reached the expected state");
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }

  it("keeps waiting while transport is unreachable and ends stalled only after seven days", async () => {
    const fake = new FakeTransport();
    fake.skewMs = (await environment.currentTimeMs()) - Date.now();
    const worker = await Worker.create({
      connection: environment.nativeConnection,
      taskQueue: "tender-outage",
      workflowBundle,
      activities: createTenderActivities("http://transport", createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 9).toString("base64") }), fake.fetch),
    });
    let outageStartedAt = 0;
    const result = await worker.runUntil(async () => {
      const handle = await environment.client.workflow.start(tenderWorkflow, { workflowId: tenderWorkflowId(input.tenantId, LOAD_ID), taskQueue: "tender-outage", args: [{ ...input, outageRetryMs: DAY_MS }] });
      await eventually(async () => (await handle.query(tenderStateQuery)).pendingCarrierId === CARRIERS[0]);
      fake.readOutage = true;
      outageStartedAt = await environment.currentTimeMs();
      await handle.signal(tenderResponseSignal);
      await eventually(async () => fake.reads > 3);
      // No manual time skips: awaiting the result lets the test server skip time on its own, as in the other outage tests.
      return handle.result();
    });
    expect(result).toMatchObject({ status: "stalled", attempted: [CARRIERS[0]] });
    // Main's earlier bound (six one-hour windows) would have stalled it within hours; it must last the full week, and
    // with a day between windows it ends at the first window after day seven.
    const outageMs = (await environment.currentTimeMs()) - outageStartedAt;
    expect(outageMs).toBeGreaterThanOrEqual(7 * DAY_MS);
    expect(outageMs).toBeLessThan(8 * DAY_MS);
  }, 300_000);

  function startWorker(fake: FakeTransport, taskQueue: string): Promise<Worker> {
    return Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowBundle,
      activities: createTenderActivities("http://transport", createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 9).toString("base64") }), fake.fetch),
    });
  }

  it("rides out a start that fails for more than one activity window and then opens one tender", async () => {
    const fake = new FakeTransport();
    // One one-hour window holds about 65 attempts, so 100 failures need the workflow's own outage wait and a second window.
    fake.startOutages = 100;
    fake.duringNextRead = async () => fake.respond(CARRIERS[0]!, true);
    const worker = await startWorker(fake, "tender-start-outage");
    const startedAt = await environment.currentTimeMs();
    const result = await worker.runUntil(
      environment.client.workflow.execute(tenderWorkflow, { workflowId: `${tenderWorkflowId(input.tenantId, LOAD_ID)}:start`, taskQueue: "tender-start-outage", args: [{ ...input, outageRetryMs: DAY_MS }] }),
    );
    expect(result).toMatchObject({ status: "accepted", acceptedCarrierId: CARRIERS[0] });
    expect(fake.starts).toBe(1);
    expect(fake.startCalls).toBe(101);
    expect((await environment.currentTimeMs()) - startedAt).toBeGreaterThanOrEqual(DAY_MS);
  }, 300_000);

  it("ends stalled, without a tender, when transport is unreachable for seven days from the start", async () => {
    const fake = new FakeTransport();
    fake.startOutages = Number.POSITIVE_INFINITY;
    const worker = await startWorker(fake, "tender-start-down");
    const startedAt = await environment.currentTimeMs();
    const result = await worker.runUntil(
      environment.client.workflow.execute(tenderWorkflow, { workflowId: `${tenderWorkflowId(input.tenantId, LOAD_ID)}:down`, taskQueue: "tender-start-down", args: [{ ...input, outageRetryMs: DAY_MS }] }),
    );
    expect(result).toMatchObject({ status: "stalled", attempted: [] });
    expect(result.tenderId).toBeUndefined();
    expect(fake.starts).toBe(0);
    expect((await environment.currentTimeMs()) - startedAt).toBeGreaterThanOrEqual(7 * DAY_MS);
  }, 300_000);
});
