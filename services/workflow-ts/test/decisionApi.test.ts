import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { WorkflowNotFoundError } from "@temporalio/client";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { createDecisionHandler } from "../src/decisionApi.js";

const TENANT = "00000000-0000-0000-0000-000000000001";
const OTHER_TENANT = "00000000-0000-0000-0000-000000000002";
const ECO = "00000000-0000-0000-0000-0000000000e1";
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
    readings.set(`eco-approval:${ECO}`, { tenantId: TENANT, requester: "rita", status: "awaiting_approval" });
    readings.set(`capa:${NCR}`, { tenantId: TENANT, status: "awaiting_effectiveness", currentStep: 8, implementer: "engineer" });
  });

  function post(path: string, body: unknown, headers: Record<string, string> = {}) {
    return fetch(`${base}${path}`, {
      method: "POST",
      headers: { "X-Passage-Tenant": TENANT, "X-Passage-User": "alice", "X-Passage-Duties": "engineering.eco.approve,QM_CAPA_MAINTAIN", ...headers },
      body: JSON.stringify(body),
    });
  }

  it("signals an ECO approval with the actor from the request principal, never from the body", async () => {
    const response = await post(`/v1/workflows/eco-approvals/${ECO}:approve`, { actor: "mallory", decision: "rejected" });
    expect(response.status).toBe(202);
    expect(signals).toEqual([{ workflowId: `eco-approval:${ECO}`, name: "eco-decision", args: [{ decision: "approved", actor: "alice" }] }]);
  });

  it("passes a rejection reason", async () => {
    await post(`/v1/workflows/eco-approvals/${ECO}:reject`, { reason: " too loose " });
    expect(signals[0]?.args).toEqual([{ decision: "rejected", actor: "alice", reason: "too loose" }]);
  });

  it.each([
    ["no tenant", { "X-Passage-Tenant": "" }, 401],
    ["no user", { "X-Passage-User": "" }, 401],
    ["a service token", { "X-Passage-Service-Token": "x.y" }, 401],
    ["no approve duty", { "X-Passage-Duties": "QM_CAPA_MAINTAIN" }, 403],
    ["the creator", { "X-Passage-User": "rita" }, 403],
    ["another tenant", { "X-Passage-Tenant": OTHER_TENANT }, 404],
  ])("refuses an ECO decision from %s and signals nothing", async (_name, headers, status) => {
    const response = await post(`/v1/workflows/eco-approvals/${ECO}:approve`, {}, headers);
    expect(response.status).toBe(status);
    expect(signals).toEqual([]);
  });

  it("refuses an ECO decision when no workflow runs or it already ended", async () => {
    readings.set(`eco-approval:${ECO}`, "missing");
    expect((await post(`/v1/workflows/eco-approvals/${ECO}:approve`, {})).status).toBe(404);
    readings.set(`eco-approval:${ECO}`, { tenantId: TENANT, requester: "rita", status: "timed_out" });
    expect((await post(`/v1/workflows/eco-approvals/${ECO}:approve`, {})).status).toBe(409);
    expect(signals).toEqual([]);
  });

  it("signals a CAPA step for the current step only", async () => {
    readings.set(`capa:${NCR}`, { tenantId: TENANT, status: "in_correction", currentStep: 2 });
    expect((await post(`/v1/workflows/capa/${NCR}:step-done`, { step: 3, notes: "x" })).status).toBe(409);
    expect((await post(`/v1/workflows/capa/${NCR}:step-done`, { step: 2, notes: "  " })).status).toBe(400);
    expect((await post(`/v1/workflows/capa/${NCR}:step-done`, { step: 2, notes: "root cause", actor: "mallory" })).status).toBe(202);
    expect(signals).toEqual([{ workflowId: `capa:${NCR}`, name: "capa-step-done", args: [{ step: 2, notes: "root cause", actor: "alice" }] }]);
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
    readings.set(`capa:${NCR}`, { tenantId: TENANT, status: "effectiveness_pending", currentStep: 8, implementer: "engineer" });
    expect((await post(path, body)).status).toBe(409);
    expect(signals).toEqual([]);
    readings.set(`capa:${NCR}`, { tenantId: TENANT, status: "awaiting_effectiveness", currentStep: 8, implementer: "engineer" });
    expect((await post(path, body)).status).toBe(202);
    expect(signals).toEqual([{ workflowId: `capa:${NCR}`, name: "capa-effectiveness", args: [{ result: "EFFECTIVE", note: "no recurrence", actor: "alice" }] }]);
  });

  it("answers health probes, 404 for unknown routes and 405 for other methods", async () => {
    expect((await fetch(`${base}/healthz`)).status).toBe(200);
    expect((await fetch(`${base}/v1/unknown`)).status).toBe(404);
    expect((await fetch(`${base}/v1/workflows/eco-approvals/${ECO}:approve`)).status).toBe(405);
  });
});
