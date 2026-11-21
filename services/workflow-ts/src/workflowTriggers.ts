import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy } from "@temporalio/client";
import type { Client } from "@temporalio/client";
import { ecoApprovalWorkflow, ecoApprovalWorkflowId, validateEcoApprovalInput, type EcoApprovalInput } from "./ecoApprovalWorkflow.js";
import { capaWorkflow, capaWorkflowId, validateCapaInput, type CapaInput } from "./capaWorkflow.js";

export const ecoSubmittedType = "dev.depot.engineering.eco.submitted.v1";
export const ecoSubmittedSource = "/depot/engineering/engineering";
export const nonconformanceCreatedType = "dev.depot.quality.nonconformance.created.v1";
export const nonconformanceCreatedSource = "quality";

const DAY_MS = 24 * 60 * 60 * 1000;
export const ECO_APPROVAL_TIMEOUT_MS = 14 * DAY_MS;
export const CAPA_TRIGGER_ACTOR = "system:capa-trigger";

export interface EcoSubmittedEvent {
  type: string;
  source: string;
  tenantid: string;
  actor?: string;
  data: { ecoId: string; legalEntityId?: string | null };
}

export interface NonconformanceCreatedEvent {
  type: string;
  source: string;
  tenantid: string;
  data: { ncr_id: string; legal_entity_id?: string | null };
}

export interface TriggerResult {
  workflowId: string;
  started: boolean;
  skipped?: string;
}

function checkEnvelope(event: { type: string; source: string }, type: string, source: string): void {
  if (event.type !== type) throw new TypeError(`expected ${type}, got ${event.type}`);
  if (event.source !== source) throw new TypeError(`${type} is accepted only from ${source}, got ${event.source}`);
}

function requireText(value: unknown, name: string): string {
  if (typeof value !== "string" || value === "") throw new TypeError(`${name} must be a non-empty string`);
  return value;
}

function optionalLegalEntity(value: unknown, name: string): string {
  if (value === undefined || value === null || value === "") return "";
  return requireText(value, name);
}

export function toEcoApprovalInput(event: EcoSubmittedEvent): EcoApprovalInput {
  checkEnvelope(event, ecoSubmittedType, ecoSubmittedSource);
  return {
    tenantId: requireText(event.tenantid, "tenantid"),
    legalEntityId: optionalLegalEntity(event.data.legalEntityId, "legalEntityId"),
    ecoId: requireText(event.data.ecoId, "ecoId"),
    requester: requireText(event.actor, "actor"),
    approvalTimeoutMs: ECO_APPROVAL_TIMEOUT_MS,
  };
}

export function toCapaInput(event: NonconformanceCreatedEvent): CapaInput {
  checkEnvelope(event, nonconformanceCreatedType, nonconformanceCreatedSource);
  return {
    tenantId: requireText(event.tenantid, "tenantid"),
    legalEntityId: optionalLegalEntity(event.data.legal_entity_id, "legal_entity_id"),
    ncrId: requireText(event.data.ncr_id, "ncr_id"),
    startedBy: CAPA_TRIGGER_ACTOR,
    stepTimeoutMs: 14 * DAY_MS,
    effectivenessTimeoutMs: 14 * DAY_MS,
    maxReopens: 1,
    awaitApproval: true,
  };
}

async function startOnce(start: () => Promise<unknown>, workflowId: string): Promise<TriggerResult> {
  try {
    await start();
    return { workflowId, started: true };
  } catch (error) {
    if (error instanceof WorkflowExecutionAlreadyStartedError) return { workflowId, started: false };
    throw error;
  }
}

const skippedWithoutEntity = (what: string): string =>
  `No workflow was started: the ${what} event names no legal entity, which happens when it was raised by a tenant-wide or multi-entity user.`;

export async function handleEcoSubmitted(client: Pick<Client, "workflow">, taskQueue: string, event: EcoSubmittedEvent): Promise<TriggerResult> {
  const input = toEcoApprovalInput(event);
  const workflowId = ecoApprovalWorkflowId(input.ecoId);
  if (!input.legalEntityId) return { workflowId, started: false, skipped: skippedWithoutEntity("ECO submitted") };
  validateEcoApprovalInput(input);
  return startOnce(
    () => client.workflow.start(ecoApprovalWorkflow, { workflowId, taskQueue, args: [input], workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE }),
    workflowId,
  );
}

export async function handleNonconformanceCreated(client: Pick<Client, "workflow">, taskQueue: string, event: NonconformanceCreatedEvent): Promise<TriggerResult> {
  const input = toCapaInput(event);
  const workflowId = capaWorkflowId(input.ncrId);
  if (!input.legalEntityId) return { workflowId, started: false, skipped: skippedWithoutEntity("nonconformance created") };
  validateCapaInput(input);
  return startOnce(
    () => client.workflow.start(capaWorkflow, { workflowId, taskQueue, args: [input], workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE }),
    workflowId,
  );
}
