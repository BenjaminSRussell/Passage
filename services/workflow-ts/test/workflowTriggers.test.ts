import { describe, expect, it, vi } from "vitest";
import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy, type Client } from "@temporalio/client";
import {
  CAPA_TRIGGER_ACTOR,
  ECO_APPROVAL_TIMEOUT_MS,
  ecoSubmittedSource,
  ecoSubmittedType,
  handleEcoSubmitted,
  handleNonconformanceCreated,
  nonconformanceCreatedSource,
  nonconformanceCreatedType,
  type EcoSubmittedEvent,
  type NonconformanceCreatedEvent,
} from "../src/workflowTriggers.js";
import { capaWorkflow, ecoApprovalWorkflow } from "../src/workflows.js";

const TENANT = "00000000-0000-0000-0000-000000000001";
const ENTITY = "00000000-0000-0000-0000-0000000000a1";
const ECO = "00000000-0000-0000-0000-0000000000e1";
const NCR = "00000000-0000-0000-0000-0000000000c1";

const eco: EcoSubmittedEvent = { type: ecoSubmittedType, source: ecoSubmittedSource, tenantid: TENANT, actor: "creator", data: { ecoId: ECO, legalEntityId: ENTITY } };
const ncr: NonconformanceCreatedEvent = { type: nonconformanceCreatedType, source: nonconformanceCreatedSource, tenantid: TENANT, data: { ncr_id: NCR, legal_entity_id: ENTITY } };

const clientWith = (start: ReturnType<typeof vi.fn>) => ({ client: { workflow: { start } } as unknown as Pick<Client, "workflow">, start });
const alreadyStarted = () => new WorkflowExecutionAlreadyStartedError("dup", "w", "w");

describe("ECO approval trigger", () => {
  it("starts eco-approval:<ecoId> for the creator, scoped to the event's legal entity", async () => {
    const { client, start } = clientWith(vi.fn().mockResolvedValue({}));
    expect(await handleEcoSubmitted(client, "q", eco)).toEqual({ workflowId: `eco-approval:${ECO}`, started: true });
    expect(start).toHaveBeenCalledWith(
      ecoApprovalWorkflow,
      expect.objectContaining({
        workflowId: `eco-approval:${ECO}`,
        taskQueue: "q",
        workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE,
        args: [{ tenantId: TENANT, legalEntityId: ENTITY, ecoId: ECO, requester: "creator", approvalTimeoutMs: ECO_APPROVAL_TIMEOUT_MS }],
      }),
    );
  });

  it("skips with a plain-language reason when the event names no legal entity", async () => {
    for (const legalEntityId of [undefined, null, ""]) {
      const { client, start } = clientWith(vi.fn());
      const result = await handleEcoSubmitted(client, "q", { ...eco, data: { ecoId: ECO, legalEntityId } });
      expect(result).toMatchObject({ workflowId: `eco-approval:${ECO}`, started: false, skipped: expect.stringContaining("no legal entity") });
      expect(start).not.toHaveBeenCalled();
    }
  });

  it("refuses a foreign type or source and malformed fields with a TypeError, starting nothing", async () => {
    const { client, start } = clientWith(vi.fn());
    const bad: EcoSubmittedEvent[] = [
      { ...eco, type: "dev.depot.engineering.eco.approved.v1" },
      { ...eco, source: "/depot/tms/transport" },
      { ...eco, data: { ecoId: "", legalEntityId: ENTITY } },
      { ...eco, data: { ecoId: 7 as unknown as string, legalEntityId: ENTITY } },
      { ...eco, data: { ecoId: ECO, legalEntityId: 5 as unknown as string } },
      { ...eco, actor: "" },
      { ...eco, actor: undefined },
      { ...eco, tenantid: "" },
    ];
    for (const event of bad) await expect(handleEcoSubmitted(client, "q", event)).rejects.toBeInstanceOf(TypeError);
    expect(start).not.toHaveBeenCalled();
  });

  it("reports a redelivered event as not started and rethrows when Temporal is unavailable", async () => {
    expect(await handleEcoSubmitted(clientWith(vi.fn().mockRejectedValue(alreadyStarted())).client, "q", eco)).toEqual({ workflowId: `eco-approval:${ECO}`, started: false });
    await expect(handleEcoSubmitted(clientWith(vi.fn().mockRejectedValue(new Error("unavailable"))).client, "q", eco)).rejects.toThrow("unavailable");
  });
});

describe("CAPA trigger", () => {
  it("starts capa:<ncrId> as the trigger actor, waiting for the NCR to be approved", async () => {
    const { client, start } = clientWith(vi.fn().mockResolvedValue({}));
    expect(await handleNonconformanceCreated(client, "q", ncr)).toEqual({ workflowId: `capa:${NCR}`, started: true });
    expect(start).toHaveBeenCalledWith(
      capaWorkflow,
      expect.objectContaining({
        workflowId: `capa:${NCR}`,
        taskQueue: "q",
        workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE,
        args: [expect.objectContaining({ tenantId: TENANT, legalEntityId: ENTITY, ncrId: NCR, startedBy: CAPA_TRIGGER_ACTOR, awaitApproval: true, maxReopens: 1 })],
      }),
    );
  });

  it("skips with a reason when the event names no legal entity", async () => {
    for (const legal_entity_id of [undefined, null, ""]) {
      const { client, start } = clientWith(vi.fn());
      const result = await handleNonconformanceCreated(client, "q", { ...ncr, data: { ncr_id: NCR, legal_entity_id } });
      expect(result).toMatchObject({ started: false, skipped: expect.stringContaining("no legal entity") });
      expect(start).not.toHaveBeenCalled();
    }
  });

  it("refuses a foreign type or source and a missing or mistyped ncr_id with a TypeError", async () => {
    const { client, start } = clientWith(vi.fn());
    const bad: NonconformanceCreatedEvent[] = [
      { ...ncr, type: "dev.depot.quality.capa.closed.v1" },
      { ...ncr, source: "/depot/engineering/engineering" },
      { ...ncr, data: { ncr_id: "", legal_entity_id: ENTITY } },
      { ...ncr, data: { ncr_id: { id: NCR } as unknown as string, legal_entity_id: ENTITY } },
    ];
    for (const event of bad) await expect(handleNonconformanceCreated(client, "q", event)).rejects.toBeInstanceOf(TypeError);
    expect(start).not.toHaveBeenCalled();
  });

  it("reports a redelivered event as not started", async () => {
    expect(await handleNonconformanceCreated(clientWith(vi.fn().mockRejectedValue(alreadyStarted())).client, "q", ncr)).toEqual({ workflowId: `capa:${NCR}`, started: false });
  });
});
