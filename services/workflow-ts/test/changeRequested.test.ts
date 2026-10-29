import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { Worker } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import { createActivities, createPartyHttpClient, type PartyClient, type PartyDecision } from "../src/activities.js";
import { changeRequestedType, handleChangeRequested, toApprovalInput, type ChangeRequestedEvent } from "../src/changeRequested.js";
import { approvalStateQuery, approvalWorkflow, approveSignal, rejectSignal, type ApprovalLevel } from "../src/workflows.js";

const levels: ApprovalLevel[] = [
  { id: "finance", approver: "alice", escalationApprover: "cfo", slaMs: 3_600_000 },
];

function event(changeId: string): ChangeRequestedEvent {
  return {
    type: changeRequestedType,
    tenantid: "tenant-1",
    data: { change_id: changeId, legal_entity_id: "le-1", field: "credit_limit", requested_by: "requester" },
  };
}

describe("change.requested consumer", () => {
  let environment: TestWorkflowEnvironment;
  const decisions: PartyDecision[] = [];
  const party: PartyClient = { decide: async (decision) => void decisions.push(decision) };

  beforeAll(async () => {
    environment = await TestWorkflowEnvironment.createTimeSkipping();
  });

  afterAll(async () => {
    await environment?.teardown();
  });

  async function run(taskQueue: string, body: () => Promise<void>): Promise<void> {
    decisions.length = 0;
    const worker = await Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowsPath: fileURLToPath(new URL("../src/workflows.ts", import.meta.url)),
      activities: createActivities(party),
    });
    await worker.runUntil(body);
  }

  it("maps the event to an approval input carrying tenant, legal entity and requester", () => {
    expect(toApprovalInput(event("c-1"), levels)).toEqual({
      requestId: "c-1",
      entityType: "credit_limit",
      levels,
      requester: "requester",
      partyChange: { changeId: "c-1", tenantId: "tenant-1", legalEntityId: "le-1" },
    });
  });

  it("rejects events of another type, field, or missing tenant", () => {
    expect(() => toApprovalInput({ ...event("c"), type: "x.y.v1" }, levels)).toThrow(TypeError);
    expect(() => toApprovalInput({ ...event("c"), data: { ...event("c").data, field: "name" } }, levels)).toThrow(TypeError);
    expect(() => toApprovalInput({ ...event("c"), tenantid: "" }, levels)).toThrow(TypeError);
  });

  it("starts one workflow per change and ignores duplicate events", async () => {
    await run("dup-queue", async () => {
      const first = await handleChangeRequested(environment.client, "dup-queue", levels, event("c-dup"));
      const second = await handleChangeRequested(environment.client, "dup-queue", levels, event("c-dup"));
      expect(first).toEqual({ workflowId: "approval:c-dup", started: true });
      expect(second).toEqual({ workflowId: "approval:c-dup", started: false });
      await environment.client.workflow.getHandle("approval:c-dup").signal(approveSignal, "alice");
      await environment.client.workflow.getHandle("approval:c-dup").result();
      const third = await handleChangeRequested(environment.client, "dup-queue", levels, event("c-dup"));
      expect(third.started).toBe(false);
      expect(decisions).toHaveLength(1);
    });
  }, 30_000);

  it("approves the party change with the deciding user and change ids", async () => {
    await run("approve-queue", async () => {
      await handleChangeRequested(environment.client, "approve-queue", levels, event("c-ok"));
      const handle = environment.client.workflow.getHandle("approval:c-ok");
      await handle.signal(approveSignal, "alice");
      const result = await handle.result();
      expect(result.status).toBe("approved");
      expect(decisions).toEqual([
        { changeId: "c-ok", tenantId: "tenant-1", legalEntityId: "le-1", decision: "approved", decidedBy: "alice" },
      ]);
    });
  }, 30_000);

  it("rejects the party change on rejection", async () => {
    await run("reject-queue", async () => {
      await handleChangeRequested(environment.client, "reject-queue", levels, event("c-no"));
      const handle = environment.client.workflow.getHandle("approval:c-no");
      await handle.signal(rejectSignal, "alice");
      expect((await handle.result()).status).toBe("rejected");
      expect(decisions).toEqual([
        { changeId: "c-no", tenantId: "tenant-1", legalEntityId: "le-1", decision: "rejected", decidedBy: "alice" },
      ]);
    });
  }, 30_000);

  it("refuses a decision by the requester", async () => {
    const selfLevels: ApprovalLevel[] = [
      { id: "finance", approver: "requester", escalationApprover: "cfo", slaMs: 3_600_000 },
    ];
    await run("self-queue", async () => {
      await handleChangeRequested(environment.client, "self-queue", selfLevels, event("c-self"));
      const handle = environment.client.workflow.getHandle<typeof approvalWorkflow>("approval:c-self");
      await handle.signal(approveSignal, "requester");
      const state = await handle.query(approvalStateQuery);
      expect(state.status).toBe("pending");
      await environment.sleep(3_600_001);
      await handle.signal(approveSignal, "cfo");
      const result = await handle.result();
      expect(result.history.map((entry) => entry.action)).toEqual(["escalated", "approved"]);
      expect(decisions.map((d) => d.decidedBy)).toEqual(["cfo"]);
    });
  }, 30_000);
});

describe("party http client", () => {
  const decision: PartyDecision = {
    changeId: "c-1", tenantId: "t", legalEntityId: "le", decision: "approved", decidedBy: "alice",
  };

  it("posts approve and reject to the pending-change endpoints with scope headers", async () => {
    const calls: { url: string; headers: Record<string, string> }[] = [];
    const fetchStub = (async (url: string, init: { headers: Record<string, string> }) => {
      calls.push({ url, headers: init.headers });
      return new Response("{}", { status: 200 });
    }) as unknown as typeof fetch;
    const client = createPartyHttpClient("http://party", fetchStub);
    await client.decide(decision);
    await client.decide({ ...decision, decision: "rejected" });
    expect(calls.map((c) => c.url)).toEqual([
      "http://party/v1/party/pending-changes/c-1:approve",
      "http://party/v1/party/pending-changes/c-1:reject",
    ]);
    expect(calls[0]?.headers).toMatchObject({
      "X-Passage-Tenant": "t", "X-Passage-Legal-Entity": "le", "X-Passage-User": "alice",
    });
  });

  it("fails non-retryably on 4xx and retryably on 5xx", async () => {
    const respond = (status: number) =>
      createPartyHttpClient("http://party", (async () => new Response("", { status })) as unknown as typeof fetch);
    await expect(respond(403).decide(decision)).rejects.toMatchObject({ nonRetryable: true });
    await expect(respond(503).decide(decision)).rejects.not.toMatchObject({ nonRetryable: true });
  });
});
