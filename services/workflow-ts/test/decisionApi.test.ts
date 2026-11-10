import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { WorkflowNotFoundError } from "@temporalio/client";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { createDecisionHandler } from "../src/decisionApi.js";

const LE = "00000000-0000-0000-0000-0000000000a1";
const OTHER_LE = "00000000-0000-0000-0000-0000000000a2";
const TENANT = "00000000-0000-0000-0000-000000000001";
const OTHER_TENANT = "00000000-0000-0000-0000-000000000002";
const ECO = "00000000-0000-0000-0000-0000000000e1";
const PLAN = "00000000-0000-0000-0000-0000000000b1";
const UNIT = "00000000-0000-0000-0000-0000000000c1";
const SCENARIO = "00000000-0000-0000-0000-0000000000b2";
const NCR = "00000000-0000-0000-0000-0000000000c1";

type Reading = Record<string, unknown> | "missing";

describe("decision routes", () => {
  let server: Server;
  let base = "";
  const readings = new Map<string, Reading>();
  let signals: { workflowId: string; name: string; args: unknown[] }[] = [];

  const client = {
    workflow: {
      getHandle: (workflowId: string) => ({
        query: async () => {
          const reading = readings.get(workflowId);
          if (!reading || reading === "missing") throw new WorkflowNotFoundError("not found", workflowId, undefined);
          return reading;
        },
        signal: async (definition: { name: string }, ...args: unknown[]) => void signals.push({ workflowId, name: definition.name, args }),
      }),
    },
  } as never;

  beforeAll(async () => {
    server = createServer(createDecisionHandler(client));
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  });
  afterAll(() => void server.close());
  beforeEach(() => {
    signals = [];
    readings.clear();
    readings.set(`eco-approval:${ECO}`, { tenantId: TENANT, legalEntityId: LE, requester: "rita", status: "awaiting_approval" });
    readings.set(`budget-plan:${PLAN}:1`, { tenantId: TENANT, legalEntityId: LE, requester: "pat", status: "awaiting_review" });
    readings.set(`capa:${NCR}`, { tenantId: TENANT, legalEntityId: LE, status: "awaiting_effectiveness", currentStep: 8, implementer: "engineer" });
  });

  function post(path: string, body: unknown, headers: Record<string, string> = {}) {
    return fetch(`${base}${path}`, {
      method: "POST",
      headers: { "X-Passage-Tenant": TENANT, "X-Passage-User": "alice", "X-Passage-Legal-Entity": `${OTHER_LE},${LE.toUpperCase()}`, "X-Passage-Duties": "engineering.eco.approve,quality.capa.maintain,budget.plan.decide", ...headers },
      body: JSON.stringify(body),
    });
  }

  it("signals an ECO approval with the actor from the request principal, never from the body", async () => {
    const response = await post(`/v1/workflows/eco-approvals/${ECO}:approve`, { actor: "mallory", decision: "rejected" });
    expect(response.status).toBe(202);
    expect(signals).toEqual([{ workflowId: `eco-approval:${ECO}`, name: "eco-decision", args: [{ decision: "approved", actor: "alice", scope: [OTHER_LE, LE] }] }]);
  });

  it("passes a rejection reason", async () => {
    await post(`/v1/workflows/eco-approvals/${ECO}:reject`, { reason: " too loose " });
    expect(signals[0]?.args).toEqual([{ decision: "rejected", actor: "alice", scope: [OTHER_LE, LE], reason: "too loose" }]);
  });

  it.each([
    ["no tenant", { "X-Passage-Tenant": "" }, 401],
    ["no user", { "X-Passage-User": "" }, 401],
    ["a service token", { "X-Passage-Service-Token": "x.y" }, 401],
    ["no approve duty", { "X-Passage-Duties": "quality.capa.maintain" }, 403],
    ["the creator", { "X-Passage-User": "rita" }, 403],
    ["another tenant", { "X-Passage-Tenant": OTHER_TENANT }, 404],
    ["a legal entity outside the user's scope", { "X-Passage-Legal-Entity": OTHER_LE }, 403],
    ["no legal-entity claim", { "X-Passage-Legal-Entity": "" }, 403],
  ])("refuses an ECO decision from %s and signals nothing", async (_name, headers, status) => {
    const response = await post(`/v1/workflows/eco-approvals/${ECO}:approve`, {}, headers);
    expect(response.status).toBe(status);
    expect(signals).toEqual([]);
  });

  it("refuses an ECO decision when no workflow runs or it already ended", async () => {
    readings.set(`eco-approval:${ECO}`, "missing");
    expect((await post(`/v1/workflows/eco-approvals/${ECO}:approve`, {})).status).toBe(404);
    readings.set(`eco-approval:${ECO}`, { tenantId: TENANT, legalEntityId: LE, requester: "rita", status: "timed_out" });
    expect((await post(`/v1/workflows/eco-approvals/${ECO}:approve`, {})).status).toBe(409);
    expect(signals).toEqual([]);
  });

  it("signals a budget plan stage sign-off with the actor from the request principal, never from the body", async () => {
    const response = await post(`/v1/workflows/budget-plans/${PLAN}/stages/approve:sign-off`, { submission: 1, orgUnitId: UNIT, scenarioId: SCENARIO, actor: "mallory" });
    expect(response.status).toBe(202);
    expect(signals).toEqual([
      { workflowId: `budget-plan:${PLAN}:1`, name: "budget-plan-decision", args: [{ decision: "sign-off", stage: "approve", orgUnitId: UNIT, scenarioId: SCENARIO, actor: "alice", scope: [OTHER_LE, LE] }] },
    ]);
  });

  it("signals a budget plan rejection with a trimmed reason", async () => {
    await post(`/v1/workflows/budget-plans/${PLAN}:reject`, { submission: 1, reason: " too high " });
    expect(signals[0]?.args).toEqual([{ decision: "reject", reason: "too high", actor: "alice", scope: [OTHER_LE, LE] }]);
  });

  it.each([
    ["no decide duty", `/v1/workflows/budget-plans/${PLAN}/stages/review:sign-off`, { submission: 1, orgUnitId: UNIT }, { "X-Passage-Duties": "engineering.eco.approve" }, 403],
    ["the submitter", `/v1/workflows/budget-plans/${PLAN}/stages/review:sign-off`, { submission: 1, orgUnitId: UNIT }, { "X-Passage-User": "pat" }, 403],
    ["another tenant", `/v1/workflows/budget-plans/${PLAN}/stages/review:sign-off`, { submission: 1, orgUnitId: UNIT }, { "X-Passage-Tenant": OTHER_TENANT }, 404],
    ["a legal entity outside the user's scope", `/v1/workflows/budget-plans/${PLAN}/stages/review:sign-off`, { submission: 1, orgUnitId: UNIT }, { "X-Passage-Legal-Entity": OTHER_LE }, 403],
    ["no submission", `/v1/workflows/budget-plans/${PLAN}/stages/review:sign-off`, { orgUnitId: UNIT }, {}, 400],
    ["a bad org unit", `/v1/workflows/budget-plans/${PLAN}/stages/review:sign-off`, { submission: 1, orgUnitId: "x" }, {}, 400],
    ["an approval without a scenario", `/v1/workflows/budget-plans/${PLAN}/stages/approve:sign-off`, { submission: 1, orgUnitId: UNIT }, {}, 400],
    ["a review naming a scenario", `/v1/workflows/budget-plans/${PLAN}/stages/review:sign-off`, { submission: 1, orgUnitId: UNIT, scenarioId: SCENARIO }, {}, 400],
    ["a rejection without a reason", `/v1/workflows/budget-plans/${PLAN}:reject`, { submission: 1 }, {}, 400],
    ["a submission with no workflow", `/v1/workflows/budget-plans/${PLAN}:reject`, { submission: 2, reason: "x" }, {}, 404],
  ])("refuses a budget plan decision from %s and signals nothing", async (_name, path, body, headers, status) => {
    const response = await post(path, body, headers);
    expect(response.status).toBe(status);
    expect(signals).toEqual([]);
  });

  it("refuses a budget plan decision once the submission ended", async () => {
    readings.set(`budget-plan:${PLAN}:1`, { tenantId: TENANT, legalEntityId: LE, requester: "pat", status: "approved" });
    expect((await post(`/v1/workflows/budget-plans/${PLAN}:reject`, { submission: 1, reason: "x" })).status).toBe(409);
    expect(signals).toEqual([]);
  });

  it("signals a CAPA step for the current step only", async () => {
    readings.set(`capa:${NCR}`, { tenantId: TENANT, legalEntityId: LE, status: "in_correction", currentStep: 2 });
    expect((await post(`/v1/workflows/capa/${NCR}:step-done`, { step: 3, notes: "x" })).status).toBe(409);
    expect((await post(`/v1/workflows/capa/${NCR}:step-done`, { step: 2, notes: "  " })).status).toBe(400);
    expect((await post(`/v1/workflows/capa/${NCR}:step-done`, { step: 2, notes: "root cause", actor: "mallory" })).status).toBe(202);
    expect(signals).toEqual([{ workflowId: `capa:${NCR}`, name: "capa-step-done", args: [{ step: 2, notes: "root cause", actor: "alice", scope: [OTHER_LE, LE] }] }]);
  });

  it("applies the same legal-entity scope to CAPA decisions", async () => {
    const outside = { "X-Passage-Legal-Entity": OTHER_LE };
    expect((await post(`/v1/workflows/capa/${NCR}:effectiveness`, { result: "EFFECTIVE", note: "x" }, outside)).status).toBe(403);
    readings.set(`capa:${NCR}`, { tenantId: TENANT, legalEntityId: LE, status: "in_correction", currentStep: 1 });
    expect((await post(`/v1/workflows/capa/${NCR}:step-done`, { step: 1, notes: "x" }, outside)).status).toBe(403);
    expect(signals).toEqual([]);
  });

  it("needs the CAPA duty", async () => {
    expect((await post(`/v1/workflows/capa/${NCR}:step-done`, { step: 1, notes: "x" }, { "X-Passage-Duties": "engineering.eco.approve" })).status).toBe(403);
    expect(signals).toEqual([]);
  });

  it("signals an effectiveness check from someone other than the D7 implementer, once it is open", async () => {
    const path = `/v1/workflows/capa/${NCR}:effectiveness`;
    const body = { result: "EFFECTIVE", note: "no recurrence" };
    expect((await post(path, body, { "X-Passage-User": "engineer" })).status).toBe(403);
    expect((await post(path, { result: "MAYBE", note: "x" })).status).toBe(400);
    readings.set(`capa:${NCR}`, { tenantId: TENANT, legalEntityId: LE, status: "effectiveness_pending", currentStep: 8, implementer: "engineer" });
    expect((await post(path, body)).status).toBe(409);
    expect(signals).toEqual([]);
    readings.set(`capa:${NCR}`, { tenantId: TENANT, legalEntityId: LE, status: "awaiting_effectiveness", currentStep: 8, implementer: "engineer" });
    expect((await post(path, body)).status).toBe(202);
    expect(signals).toEqual([{ workflowId: `capa:${NCR}`, name: "capa-effectiveness", args: [{ result: "EFFECTIVE", note: "no recurrence", actor: "alice", scope: [OTHER_LE, LE] }] }]);
  });

  const CHANGE = "00000000-0000-0000-0000-0000000000b1";
  const approver = { "X-Passage-Duties": "party.approve" };
  const pendingChange = { requestId: CHANGE, entityType: "credit_limit", tenantId: TENANT, legalEntityId: LE, requester: "rita", status: "pending", levelId: "finance", approver: "alice", history: [] };

  it("signals a party change decision with the actor and scope of the authenticated approver, never from the body", async () => {
    readings.set(`approval:${CHANGE}`, pendingChange);
    expect((await post(`/v1/workflows/party-changes/${CHANGE}:approve`, { actor: "mallory", scope: [OTHER_LE] }, approver)).status).toBe(202);
    expect((await post(`/v1/workflows/party-changes/${CHANGE}:reject`, {}, approver)).status).toBe(202);
    expect(signals).toEqual([
      { workflowId: `approval:${CHANGE}`, name: "approval-decision", args: [{ action: "approve", actor: "alice", scope: [OTHER_LE, LE] }] },
      { workflowId: `approval:${CHANGE}`, name: "approval-decision", args: [{ action: "reject", actor: "alice", scope: [OTHER_LE, LE] }] },
    ]);
  });

  it("delegates a party change level to another user", async () => {
    readings.set(`approval:${CHANGE}`, pendingChange);
    const path = `/v1/workflows/party-changes/${CHANGE}:delegate`;
    expect((await post(path, {}, approver)).status).toBe(400);
    expect((await post(path, { delegate: "alice" }, approver)).status).toBe(400);
    expect((await post(path, { delegate: "rita" }, approver)).status).toBe(400);
    expect(signals).toEqual([]);
    expect((await post(path, { delegate: " dana " }, approver)).status).toBe(202);
    expect(signals).toEqual([{ workflowId: `approval:${CHANGE}`, name: "approval-decision", args: [{ action: "delegate", actor: "alice", scope: [OTHER_LE, LE], delegate: "dana" }] }]);
  });

  it.each([
    ["no approve duty", {}, { "X-Passage-Duties": "engineering.eco.approve" }, 403],
    ["a service token", {}, { ...approver, "X-Passage-Service-Token": "x.y" }, 401],
    ["forged user headers without a tenant", {}, { ...approver, "X-Passage-Tenant": "tenant-1" }, 401],
    ["the requester", {}, { ...approver, "X-Passage-User": "rita" }, 403],
    ["someone who is not the current approver", {}, { ...approver, "X-Passage-User": "bob" }, 403],
    ["another tenant", {}, { ...approver, "X-Passage-Tenant": OTHER_TENANT }, 404],
    ["a legal entity outside the approver's scope", {}, { ...approver, "X-Passage-Legal-Entity": OTHER_LE }, 403],
    ["a decided approval", { status: "approved" }, approver, 409],
    ["an approval that is not a party change", { tenantId: undefined, legalEntityId: undefined }, approver, 404],
  ])("refuses a party change decision from %s and signals nothing", async (_name, override, headers, status) => {
    readings.set(`approval:${CHANGE}`, { ...pendingChange, ...override });
    expect((await post(`/v1/workflows/party-changes/${CHANGE}:approve`, {}, headers)).status).toBe(status);
    expect(signals).toEqual([]);
  });

  it("answers 404 when no party change approval runs", async () => {
    expect((await post(`/v1/workflows/party-changes/${CHANGE}:approve`, {}, approver)).status).toBe(404);
    expect(signals).toEqual([]);
  });

  const MERGE = "00000000-0000-0000-0000-0000000000d1";
  const mergeWorkflow = `approval:party-merge:${MERGE}`;
  const mergeApprover = { "X-Passage-Duties": "party.merge.approve,tenant.administer", "X-Passage-Legal-Entity": "" };
  const pendingMerge = { requestId: `party-merge:${MERGE}`, entityType: "party_merge", tenantId: TENANT, mergeRequestId: MERGE, requester: "rita", status: "pending", levelId: "data-steward", approver: "alice", history: [] };

  it("signals a merge decision for the authenticated tenant-wide approver with no legal-entity scope", async () => {
    readings.set(mergeWorkflow, pendingMerge);
    expect((await post(`/v1/workflows/party-merges/${MERGE}:approve`, { actor: "mallory", scope: [LE] }, mergeApprover)).status).toBe(202);
    expect((await post(`/v1/workflows/party-merges/${MERGE}:reject`, {}, mergeApprover)).status).toBe(202);
    expect((await post(`/v1/workflows/party-merges/${MERGE}:delegate`, { delegate: "rita" }, mergeApprover)).status).toBe(400);
    expect((await post(`/v1/workflows/party-merges/${MERGE}:delegate`, { delegate: "dana" }, mergeApprover)).status).toBe(202);
    expect(signals).toEqual([
      { workflowId: mergeWorkflow, name: "approval-decision", args: [{ action: "approve", actor: "alice", scope: [] }] },
      { workflowId: mergeWorkflow, name: "approval-decision", args: [{ action: "reject", actor: "alice", scope: [] }] },
      { workflowId: mergeWorkflow, name: "approval-decision", args: [{ action: "delegate", actor: "alice", scope: [], delegate: "dana" }] },
    ]);
  });

  it.each([
    ["the requester (segregation of duties)", {}, { ...mergeApprover, "X-Passage-User": "rita" }, 403, "PTY-MERGE-SELF-APPROVAL"],
    ["a payment-run approver (segregation of duties)", {}, { ...mergeApprover, "X-Passage-Duties": "party.merge.approve,tenant.administer,payment-proposal.approve" }, 403, "PTY-MERGE-SOD"],
    ["a payment-run executor (segregation of duties)", {}, { ...mergeApprover, "X-Passage-Duties": "party.merge.approve,tenant.administer,payment-run.execute" }, 403, "PTY-MERGE-SOD"],
    ["a party change approver", {}, { ...mergeApprover, "X-Passage-Duties": "party.approve,tenant.administer" }, 403, "AUTH-0003"],
    ["the merge requester duty", {}, { ...mergeApprover, "X-Passage-Duties": "party.merge,tenant.administer" }, 403, "AUTH-0003"],
    ["an approver who is not a tenant administrator", {}, { ...mergeApprover, "X-Passage-Duties": "party.merge.approve" }, 403, "AUTH-0003"],
    ["an approver with a legal-entity claim", {}, { ...mergeApprover, "X-Passage-Legal-Entity": LE }, 403, "AUTH-0003"],
    ["someone who is not the current approver", {}, { ...mergeApprover, "X-Passage-User": "bob" }, 403, "WF-0403"],
    ["a service token", {}, { ...mergeApprover, "X-Passage-Service-Token": "x.y" }, 401, "AUTH-0001"],
    ["another tenant", {}, { ...mergeApprover, "X-Passage-Tenant": OTHER_TENANT }, 404, "WF-0404"],
    ["a decided approval (replayed decision)", { status: "approved" }, mergeApprover, 409, "WF-0409"],
    ["an approval that is not a merge", { mergeRequestId: undefined }, mergeApprover, 404, "WF-0404"],
  ])("refuses a merge decision from %s and signals nothing", async (_name, override, headers, status, code) => {
    readings.set(mergeWorkflow, { ...pendingMerge, ...override });
    const response = await post(`/v1/workflows/party-merges/${MERGE}:approve`, {}, headers);
    expect(response.status).toBe(status);
    expect(((await response.json()) as { code: string }).code).toBe(code);
    expect(signals).toEqual([]);
  });

  it("keeps merge and party change approvals apart", async () => {
    readings.set(mergeWorkflow, pendingMerge);
    readings.set(`approval:${MERGE}`, { ...pendingMerge, requestId: MERGE });
    expect((await post(`/v1/workflows/party-changes/${MERGE}:approve`, {}, approver)).status).toBe(404);
    readings.set(`approval:party-merge:${CHANGE}`, pendingChange);
    expect((await post(`/v1/workflows/party-merges/${CHANGE}:approve`, {}, mergeApprover)).status).toBe(404);
    readings.delete(mergeWorkflow);
    expect((await post(`/v1/workflows/party-merges/${MERGE}:approve`, {}, mergeApprover)).status).toBe(404);
    expect(signals).toEqual([]);
  });

  it("answers health probes, 404 for unknown routes and 405 for other methods", async () => {
    expect((await fetch(`${base}/healthz`)).status).toBe(200);
    expect((await fetch(`${base}/v1/unknown`)).status).toBe(404);
    expect((await fetch(`${base}/v1/workflows/eco-approvals/${ECO}:approve`)).status).toBe(405);
  });
});
