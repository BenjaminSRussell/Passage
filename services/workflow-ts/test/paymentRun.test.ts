import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { ApplicationFailure } from "@temporalio/activity";
import { bundleWorkflowCode, Worker, type WorkerOptions, type WorkflowBundle } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import { createPaymentRunActivities, MAX_REASON_BYTES, PRE_JOURNAL_AUTH_BUDGET_MS, truncateToBytes, type PaymentRunActivities } from "../src/paymentRunActivities.js";
import { createServiceTokenSigner } from "../src/serviceToken.js";
import { startPaymentRunWorkflow } from "../src/startPaymentRun.js";
import { paymentRunWorkflow, paymentRunWorkflowId, validatePaymentRunInput, type PaymentRunInput } from "../src/workflows.js";
import { FakePayables } from "./fakePayables.js";

const input: PaymentRunInput = {
  tenantId: "t1",
  legalEntityId: "le1",
  proposalId: "p1",
  executedBy: "erin",
};

const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 5).toString("base64") });

const COMPLETED_ONCE = {
  runs: 1,
  files: 1,
  payments: 2,
  ledgerEntries: 1,
  settleCounts: [1, 1, 1],
  proposal: "EXECUTED",
  reservedLines: 3,
};

describe("payment run workflow", () => {
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

  function worker(taskQueue: string, activities: PaymentRunActivities, shutdownGraceTime?: WorkerOptions["shutdownGraceTime"]) {
    return Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowBundle,
      maxCachedWorkflows: 0,
      activities: { ...activities },
      ...(shutdownGraceTime ? { shutdownGraceTime } : {}),
    });
  }

  function setup() {
    sequence += 1;
    const fake = new FakePayables();
    return { fake, taskQueue: `payment-run-${sequence}`, activities: createPaymentRunActivities("http://payables", signer, fake.fetch) };
  }

  async function execute(taskQueue: string, activities: PaymentRunActivities) {
    const w = await worker(taskQueue, activities);
    return w.runUntil(async () => {
      const handle = await environment.client.workflow.start(paymentRunWorkflow, {
        workflowId: `${paymentRunWorkflowId(input.legalEntityId, input.proposalId)}:${taskQueue}`,
        taskQueue,
        args: [input],
      });
      return handle.result();
    });
  }

  it("runs reserve, file, journal, settle and complete once", async () => {
    const { fake, taskQueue, activities } = setup();
    const result = await execute(taskQueue, activities);
    expect(result).toEqual({ runId: expect.any(String), status: "COMPLETED", journalEntryId: "entry-1" });
    expect(fake.effects()).toMatchObject(COMPLETED_ONCE);
    expect(fake.effects().calls).toEqual({ start: 1, "generate-file": 1, "post-journal": 1, settle: 1, complete: 1, fail: 0 });
  }, 60_000);

  it.each(["start", "generate-file", "post-journal", "settle", "complete"] as const)(
    "a reply lost after %s committed its effect re-runs the step without a second effect",
    async (action) => {
      const { fake, taskQueue, activities } = setup();
      let lost = false;
      fake.after(action, async () => {
        if (lost) return;
        lost = true;
        throw new TypeError("fetch failed");
      });
      const result = await execute(taskQueue, activities);
      expect(result.status).toBe("COMPLETED");
      expect(fake.effects()).toMatchObject(COMPLETED_ONCE);
      expect(fake.effects().calls[action]).toBe(2);
    },
    60_000,
  );

  it.each(["generate-file", "post-journal", "settle"] as const)(
    "a worker killed mid-run after %s committed is replaced and the run finishes with exactly-once effects",
    async (action) => {
      const { fake, taskQueue, activities } = setup();
      let reached!: () => void;
      const inFlight = new Promise<void>((resolve) => (reached = resolve));
      let killed = false;
      fake.after(action, (signal) => {
        if (killed) return Promise.resolve();
        killed = true;
        reached();
        return new Promise<void>((_, reject) =>
          signal?.addEventListener("abort", () => reject(new Error("worker killed")), { once: true }),
        );
      });

      const first = await worker(taskQueue, activities, "100ms");
      const firstRun = first.run();
      const handle = await environment.client.workflow.start(paymentRunWorkflow, {
        workflowId: `${paymentRunWorkflowId(input.legalEntityId, input.proposalId)}:${taskQueue}`,
        taskQueue,
        args: [input],
      });
      await inFlight;
      expect(fake.effects().calls[action]).toBe(1);
      first.shutdown();
      await firstRun;

      const second = await worker(taskQueue, createPaymentRunActivities("http://payables", signer, fake.fetch));
      const result = await second.runUntil(handle.result());
      expect(result.status).toBe("COMPLETED");
      expect(fake.effects()).toMatchObject(COMPLETED_ONCE);
      expect(fake.effects().calls[action]).toBe(2);
    },
    90_000,
  );

  it("a terminal ledger rejection fails the run, releases the lines and never settles", async () => {
    const { fake, taskQueue, activities } = setup();
    fake.refuse("post-journal", 422);
    const result = await execute(taskQueue, activities);
    expect(result).toMatchObject({ status: "FAILED", failureReason: expect.stringContaining("refused post-journal") });
    expect(fake.effects()).toMatchObject({ files: 1, ledgerEntries: 0, settleCounts: [0, 0, 0], proposal: "FAILED", reservedLines: 0 });
    expect(fake.effects().calls).toMatchObject({ "post-journal": 1, settle: 0, fail: 1 });
  }, 60_000);

  it("a file that keeps failing is retried five times, then the run fails before any journal", async () => {
    const { fake, taskQueue, activities } = setup();
    fake.refuse("generate-file", 503);
    const result = await execute(taskQueue, activities);
    expect(result.status).toBe("FAILED");
    expect(fake.effects()).toMatchObject({ files: 0, ledgerEntries: 0, proposal: "FAILED", reservedLines: 0 });
    expect(fake.effects().calls).toMatchObject({ "generate-file": 5, "post-journal": 0, fail: 1 });
  }, 60_000);

  it("keeps retrying settlement after the journal posted instead of failing the run", async () => {
    const { fake, taskQueue, activities } = setup();
    fake.refuse("settle", 500, 8);
    const result = await execute(taskQueue, activities);
    expect(result.status).toBe("COMPLETED");
    expect(fake.effects()).toMatchObject(COMPLETED_ONCE);
    expect(fake.effects().calls).toMatchObject({ settle: 9, fail: 0 });
  }, 60_000);

  it("a start that finds the lines changed ends FAILED without touching the file or the ledger", async () => {
    const { fake, taskQueue, activities } = setup();
    fake.startsFailed = true;
    const result = await execute(taskQueue, activities);
    expect(result).toMatchObject({ status: "FAILED", failureReason: "open items changed" });
    expect(fake.effects().calls).toEqual({ start: 1, "generate-file": 0, "post-journal": 0, settle: 0, complete: 0, fail: 0 });
    expect(fake.effects()).toMatchObject({ proposal: "FAILED", reservedLines: 0 });
  }, 60_000);

  it("an authorization fault while settling is retried until it clears", async () => {
    const { fake, taskQueue, activities } = setup();
    fake.refuse("settle", 403, 3);
    const result = await execute(taskQueue, activities);
    expect(result.status).toBe("COMPLETED");
    expect(fake.effects().calls.settle).toBe(4);
    expect(fake.effects()).toMatchObject(COMPLETED_ONCE);
  }, 60_000);

  it("a settlement that never clears surfaces as a failed workflow after the retry budget", async () => {
    const { fake, taskQueue, activities } = setup();
    fake.refuse("settle", 401);
    await expect(execute(taskQueue, activities)).rejects.toThrow();
    expect(fake.effects()).toMatchObject({ ledgerEntries: 1, proposal: "EXECUTING", settleCounts: [0, 0, 0] });
  }, 90_000);

  it("a start the service refuses is terminal and fails the workflow", async () => {
    const { fake, taskQueue, activities } = setup();
    fake.refuse("start", 409);
    await expect(execute(taskQueue, activities)).rejects.toThrow();
    expect(fake.effects().calls.start).toBe(1);
  }, 60_000);

  it("every call carries a token for the starting user and the run's legal entity", async () => {
    const { fake, taskQueue, activities } = setup();
    await execute(taskQueue, activities);
    expect(fake.tokens.length).toBeGreaterThanOrEqual(5);
    for (const claims of fake.tokens) expect(claims).toMatchObject({ tid: "t1", le: "le1", act: "erin", duties: ["payment-run.execute"] });
    expect(new Set(fake.tokens.map((c) => c.jti)).size).toBe(fake.tokens.length);
  }, 60_000);

  it("starting the same proposal twice starts one workflow", async () => {
    sequence += 1;
    const proposalInput = { ...input, proposalId: `dup-${sequence}` };
    const first = await startPaymentRunWorkflow(environment.client, `payment-run-${sequence}`, proposalInput);
    const second = await startPaymentRunWorkflow(environment.client, `payment-run-${sequence}`, proposalInput);
    expect(first).toEqual({ workflowId: `payment-run:le1:dup-${sequence}`, started: true });
    expect(second).toEqual({ workflowId: first.workflowId, started: false });
  });
});

describe("validatePaymentRunInput", () => {
  it.each(["tenantId", "legalEntityId", "proposalId", "executedBy"] as const)("requires %s", (field) => {
    expect(() => validatePaymentRunInput({ ...input, [field]: "" })).toThrow(/required/);
  });

  it("accepts a complete input", () => {
    expect(() => validatePaymentRunInput(input)).not.toThrow();
  });
});

describe("payment run http activities", () => {
  const ref = { tenantId: "t1", legalEntityId: "le1", executedBy: "erin", runId: "r1" };

  function activitiesWith(response: Response) {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(response);
    return { fetchImpl, activities: createPaymentRunActivities("http://payables", signer, fetchImpl) };
  }

  async function inActivity<T>(fn: () => Promise<T>): Promise<T> {
    const { MockActivityEnvironment } = await import("@temporalio/testing");
    return new MockActivityEnvironment().run(fn);
  }

  it("posts the start as workflow-ts with a service token and nothing a caller could redirect", async () => {
    const { fetchImpl, activities } = activitiesWith(new Response(JSON.stringify({ id: "r1", status: "RUNNING" }), { status: 201 }));
    const state = await inActivity(() => activities.startPaymentRun(input));
    expect(state).toEqual({ id: "r1", status: "RUNNING" });
    const [url, init] = fetchImpl.mock.calls[0]!;
    expect(url).toBe("http://payables/v1/payables/payment-runs");
    const headers = init?.headers as Record<string, string>;
    expect(Object.keys(headers).sort()).toEqual(["Content-Type", "X-Passage-Service-Token"]);
    const claims = JSON.parse(Buffer.from(headers["X-Passage-Service-Token"]!.split(".")[0]!, "base64url").toString());
    expect(claims).toMatchObject({ iss: "workflow-ts", aud: "payables", tid: "t1", le: "le1", act: "erin", duties: ["payment-run.execute"] });
    expect(JSON.parse(init?.body as string)).toEqual({ legalEntityId: "le1", proposalId: "p1" });
  });

  it("mints a fresh token for every call so a retry is not a replay", async () => {
    const { fetchImpl, activities } = activitiesWith(new Response(JSON.stringify({ id: "r1", status: "RUNNING" }), { status: 200 }));
    await inActivity(() => activities.settlePaymentRun(ref));
    fetchImpl.mockResolvedValue(new Response(JSON.stringify({ id: "r1", status: "RUNNING" }), { status: 200 }));
    await inActivity(() => activities.settlePaymentRun(ref));
    const jtis = fetchImpl.mock.calls.map(([, init]) => (init?.headers as Record<string, string>)["X-Passage-Service-Token"]!.split(".")[0]!);
    expect(new Set(jtis.map((b) => JSON.parse(Buffer.from(b, "base64url").toString()).jti)).size).toBe(2);
  });

  it.each([
    ["generatePaymentFile", "generate-file"],
    ["postPaymentJournal", "post-journal"],
    ["settlePaymentRun", "settle"],
    ["completePaymentRun", "complete"],
  ] as const)("%s posts to the %s action of the run", async (method, action) => {
    const { fetchImpl, activities } = activitiesWith(new Response(JSON.stringify({ id: "r1", status: "RUNNING" }), { status: 200 }));
    await inActivity(() => activities[method](ref));
    expect(fetchImpl.mock.calls[0]![0]).toBe(`http://payables/v1/payables/payment-runs/r1:${action}`);
  });

  it("truncates the failure reason to the 500 bytes payables accepts, never splitting a character", async () => {
    const { fetchImpl, activities } = activitiesWith(new Response(JSON.stringify({ id: "r1", status: "FAILED" }), { status: 200 }));
    const reason = "é".repeat(400);
    expect(reason.length).toBe(400);
    await inActivity(() => activities.failPaymentRun({ ...ref, reason }));
    const sent = (JSON.parse(fetchImpl.mock.calls[0]![1]?.body as string) as { reason: string }).reason;
    expect(new TextEncoder().encode(sent).length).toBeLessThanOrEqual(MAX_REASON_BYTES);
    expect(sent).toBe("é".repeat(250));
    expect(truncateToBytes("short", 500)).toBe("short");
    expect(truncateToBytes("a€".repeat(300), 500)).not.toContain("\uFFFD");
  });

  it("sends the failure reason", async () => {
    const { fetchImpl, activities } = activitiesWith(new Response(JSON.stringify({ id: "r1", status: "FAILED" }), { status: 200 }));
    await inActivity(() => activities.failPaymentRun({ ...ref, reason: "bad bank details" }));
    expect(JSON.parse(fetchImpl.mock.calls[0]![1]?.body as string)).toEqual({ reason: "bad bank details" });
  });

  it("keeps 401 and an authorization 403 retryable but a business 403 terminal", async () => {
    for (const [status, body, retryable] of [
      [401, { serviceCode: "AUTH-0001" }, true],
      [403, { serviceCode: "AUTH-0003" }, true],
      [403, {}, true],
      [403, { serviceCode: "AP-PRP-003", detail: "same person" }, false],
      [403, { serviceCode: "AP-RUN-016", detail: "a payment run needs an identified user" }, false],
    ] as const) {
      const { activities } = activitiesWith(new Response(JSON.stringify(body), { status }));
      const error = await inActivity(() => activities.settlePaymentRun(ref)).catch((e: unknown) => e);
      expect(error instanceof ApplicationFailure && error.nonRetryable).toBe(!retryable);
    }
  });

  describe("auth retry budget", () => {
    const authFaults = [[401, {}], [403, { serviceCode: "AUTH-0003" }]] as const;
    const scheduledAgo = (ms: number) => ({ scheduledTimestampMs: Date.now() - ms });

    async function outcome(call: (a: PaymentRunActivities) => Promise<unknown>, status: number, body: object, ageMs: number) {
      const { MockActivityEnvironment } = await import("@temporalio/testing");
      const { activities } = activitiesWith(new Response(JSON.stringify(body), { status }));
      const error = await new MockActivityEnvironment(scheduledAgo(ageMs)).run(() => call(activities)).catch((e: unknown) => e);
      return error instanceof ApplicationFailure && error.nonRetryable ? "terminal" : "retryable";
    }

    const preJournal = [
      ["start", (a: PaymentRunActivities) => a.startPaymentRun(input)],
      ["generate-file", (a: PaymentRunActivities) => a.generatePaymentFile(ref)],
      ["fail", (a: PaymentRunActivities) => a.failPaymentRun({ ...ref, reason: "x" })],
    ] as const;
    const overBudget = PRE_JOURNAL_AUTH_BUDGET_MS + 1000;

    it.each(preJournal)("%s keeps retrying an auth fault inside the budget and gives up after it", async (_name, call) => {
      for (const [status, body] of authFaults) {
        expect(await outcome(call, status, body, 60_000)).toBe("retryable");
        expect(await outcome(call, status, body, overBudget)).toBe("terminal");
      }
    });

    it.each(preJournal)("%s keeps retrying a server fault past the auth budget", async (_name, call) => {
      expect(await outcome(call, 503, {}, overBudget)).toBe("retryable");
    });

    it.each([
      ["post-journal", (a: PaymentRunActivities) => a.postPaymentJournal(ref)],
      ["settle", (a: PaymentRunActivities) => a.settlePaymentRun(ref)],
      ["complete", (a: PaymentRunActivities) => a.completePaymentRun(ref)],
    ] as const)("%s retries forward on an auth fault however long it has lasted", async (_name, call) => {
      for (const [status, body] of authFaults) expect(await outcome(call, status, body, overBudget * 100)).toBe("retryable");
    });
  });

  it.each([400, 404, 409, 422])("treats %i as terminal", async (status) => {
    const { activities } = activitiesWith(new Response(JSON.stringify({ detail: "no" }), { status }));
    const error = await inActivity(() => activities.settlePaymentRun(ref)).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApplicationFailure);
    expect((error as ApplicationFailure).nonRetryable).toBe(true);
    expect((error as ApplicationFailure).type).toBe("PaymentRunRejected");
    expect((error as ApplicationFailure).message).toContain("no");
  });

  it.each([408, 429, 500, 502, 503])("keeps %i retryable", async (status) => {
    const { activities } = activitiesWith(new Response(null, { status }));
    const error = await inActivity(() => activities.postPaymentJournal(ref)).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(Error);
    expect(error).not.toBeInstanceOf(ApplicationFailure);
  });
});

describe("payables contract", () => {
  const spec = readFileSync(fileURLToPath(new URL("../../payables/api/openapi.yaml", import.meta.url)), "utf8");

  it.each([
    "/v1/payables/payment-runs",
    "/v1/payables/payment-runs/{id}:generate-file",
    "/v1/payables/payment-runs/{id}:post-journal",
    "/v1/payables/payment-runs/{id}:settle",
    "/v1/payables/payment-runs/{id}:complete",
    "/v1/payables/payment-runs/{id}:fail",
  ])("payables documents %s", (path) => {
    expect(spec).toContain(`\n  ${path}:\n    post:`);
  });
});
