import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { WorkflowFailedError } from "@temporalio/client";
import { bundleWorkflowCode, Worker, type WorkflowBundle } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import { createEcoActivities } from "../src/ecoActivities.js";
import { createServiceTokenSigner } from "../src/serviceToken.js";
import {
  ecoApprovalWorkflow,
  ecoApprovalWorkflowId,
  ecoApproveSignal,
  ecoApprovalStateQuery,
  ecoRejectSignal,
  validateEcoApprovalInput,
  type EcoApprovalInput,
} from "../src/workflows.js";
import { ECO_ID, FakeEngineering } from "./fakeEngineering.js";

const input: EcoApprovalInput = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  legalEntityId: "00000000-0000-0000-0000-0000000000a1",
  ecoId: ECO_ID,
  requester: "rita",
  approver: "alice",
  approvalTimeoutMs: 7 * 24 * 60 * 60 * 1000,
};

const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 5).toString("base64") });

describe("ECO approval workflow", () => {
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

  async function run(fake: FakeEngineering, drive: (handle: Awaited<ReturnType<typeof start>>) => Promise<void>, override: Partial<EcoApprovalInput> = {}) {
    sequence += 1;
    const taskQueue = `eco-approval-${sequence}`;
    const worker = await Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowBundle,
      maxCachedWorkflows: 0,
      activities: { ...createEcoActivities("http://engineering", signer, fake.fetch) },
    });
    return worker.runUntil(async () => {
      const handle = await start(taskQueue, { ...input, ...override });
      await drive(handle);
      return handle.result();
    });
  }

  function start(taskQueue: string, args: EcoApprovalInput) {
    return environment.client.workflow.start(ecoApprovalWorkflow, {
      workflowId: `${ecoApprovalWorkflowId(args.ecoId)}:${taskQueue}`,
      taskQueue,
      args: [args],
    });
  }

  it("applies the change on approval: old version expires, new version goes live, the approver is the actor", async () => {
    const fake = new FakeEngineering();
    const result = await run(fake, (handle) => handle.signal(ecoApproveSignal, "alice"));
    expect(result).toEqual({ ecoId: ECO_ID, status: "released", decidedBy: "alice" });
    expect(fake.eco.state).toBe("released");
    expect([fake.oldVersion, fake.newVersion]).toEqual(["expired", "active"]);
    expect(fake.calls).toEqual({ approve: 1, reject: 0, cancel: 0, release: 1 });
    expect(fake.tokens.map((claims) => [claims.act, claims.tid, claims.le, claims.duties])).toEqual([
      ["alice", input.tenantId, input.legalEntityId, ["engineering.eco.workflow"]],
      ["alice", input.tenantId, input.legalEntityId, ["engineering.eco.workflow"]],
    ]);
  }, 60_000);

  it("records the rejection and leaves the BOM versions alone", async () => {
    const fake = new FakeEngineering();
    const result = await run(fake, (handle) => handle.signal(ecoRejectSignal, "alice"));
    expect(result).toEqual({ ecoId: ECO_ID, status: "rejected", decidedBy: "alice" });
    expect(fake.eco.state).toBe("rejected");
    expect([fake.oldVersion, fake.newVersion]).toEqual(["active", "draft"]);
    expect(fake.calls).toEqual({ approve: 0, reject: 1, cancel: 0, release: 0 });
  }, 60_000);

  it("cancels the ECO as the requester when nobody decides before the timeout", async () => {
    const fake = new FakeEngineering();
    const result = await run(fake, async () => {});
    expect(result).toEqual({ ecoId: ECO_ID, status: "timed_out", decidedBy: "rita" });
    expect(fake.eco.state).toBe("cancelled");
    expect([fake.oldVersion, fake.newVersion]).toEqual(["active", "draft"]);
    expect(fake.calls).toEqual({ approve: 0, reject: 0, cancel: 1, release: 0 });
  }, 60_000);

  it("ignores decisions from anyone but the named approver, including the requester", async () => {
    const fake = new FakeEngineering();
    const result = await run(fake, async (handle) => {
      await handle.signal(ecoApproveSignal, "rita");
      await handle.signal(ecoApproveSignal, "mallory");
      await handle.signal(ecoRejectSignal, "rita");
    });
    expect(result.status).toBe("timed_out");
    expect(fake.calls.approve + fake.calls.reject + fake.calls.release).toBe(0);
  }, 60_000);

  it("keeps the first decision when a second arrives", async () => {
    const fake = new FakeEngineering();
    const result = await run(fake, async (handle) => {
      await handle.signal(ecoRejectSignal, "alice");
      await handle.signal(ecoApproveSignal, "alice");
    });
    expect(result.status).toBe("rejected");
    expect(fake.calls.approve).toBe(0);
  }, 60_000);

  it("exposes the awaiting state to a query", async () => {
    const fake = new FakeEngineering();
    let seen: unknown;
    await run(fake, async (handle) => {
      seen = await handle.query(ecoApprovalStateQuery);
      await handle.signal(ecoApproveSignal, "alice");
    });
    expect(seen).toEqual({ ecoId: ECO_ID, status: "awaiting_approval" });
  }, 60_000);

  it.each(["approve", "release"] as const)("a reply lost after %s committed re-runs the step without a second effect", async (action) => {
    const fake = new FakeEngineering();
    let lost = false;
    fake.after(action, async () => {
      if (lost) return;
      lost = true;
      throw new TypeError("fetch failed");
    });
    const result = await run(fake, (handle) => handle.signal(ecoApproveSignal, "alice"));
    expect(result.status).toBe("released");
    expect(fake.calls[action]).toBe(2);
    expect(fake.eco.state).toBe("released");
  }, 60_000);

  it("retries a server fault and still releases", async () => {
    const fake = new FakeEngineering();
    fake.refuse("approve", 503, "ENG-ECO-APPROVER-UNAVAILABLE", 2);
    const result = await run(fake, (handle) => handle.signal(ecoApproveSignal, "alice"));
    expect(result.status).toBe("released");
    expect(fake.calls.approve).toBe(3);
  }, 60_000);

  it("waits for a future release date and releases then", async () => {
    const fake = new FakeEngineering();
    fake.eco.releaseDate = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
    const result = await run(fake, (handle) => handle.signal(ecoApproveSignal, "alice"));
    expect(result.status).toBe("released");
    expect(fake.calls).toEqual({ approve: 1, reject: 0, cancel: 0, release: 2 });
    expect(fake.newVersion).toBe("active");
  }, 60_000);

  it("reports a release the engineering rules refuse, leaves the ECO approved and does not retry it", async () => {
    const fake = new FakeEngineering();
    fake.refuse("release", 422, "ENG-ECO-DRYRUN");
    const result = await run(fake, (handle) => handle.signal(ecoApproveSignal, "alice"));
    expect(result.status).toBe("release_failed");
    expect(result.failureReason).toContain("ENG-ECO-DRYRUN");
    expect(fake.eco.state).toBe("approved");
    expect(fake.calls.release).toBe(1);
  }, 60_000);

  it("fails the workflow when the ECO was already decided another way", async () => {
    const fake = new FakeEngineering();
    fake.eco.state = "cancelled";
    await expect(run(fake, (handle) => handle.signal(ecoApproveSignal, "alice"))).rejects.toBeInstanceOf(WorkflowFailedError);
    expect(fake.calls.approve).toBe(1);
    expect(fake.calls.release).toBe(0);
  }, 60_000);

  it("refuses to start without the people involved, or when the requester approves their own ECO", () => {
    expect(() => validateEcoApprovalInput({ ...input, approver: "" })).toThrow(/required/);
    expect(() => validateEcoApprovalInput({ ...input, approver: "rita" })).toThrow(/own ECO/);
    expect(() => validateEcoApprovalInput({ ...input, approvalTimeoutMs: 0 })).toThrow(/positive/);
    expect(() => validateEcoApprovalInput(input)).not.toThrow();
  });
});
