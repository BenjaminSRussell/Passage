import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy, WorkflowNotFoundError } from "@temporalio/client";
import type { Client } from "@temporalio/client";
import { tenderResponseSignal, tenderWorkflow, tenderWorkflowId, validateTenderInput, type TenderInput } from "./tenderWorkflow.js";

export const loadReleasedType = "dev.passage.tms.load.released.v1";
export const tenderOutcomeTypes = ["dev.passage.tms.tender.accepted.v1", "dev.passage.tms.tender.declined.v1"] as const;

export interface LoadReleasedEvent {
  type: string;
  tenantid: string;
  data: {
    loadId: string;
    legalEntityId: string;
    releasedBy: string;
    carriers: { carrierId: string; rateQuoted?: string }[];
    currency?: string;
    timeoutMin?: number;
  };
}

export interface TenderOutcomeEvent {
  type: string;
  tenantid: string;
  data: { loadId: string };
}

export function toTenderInput(event: LoadReleasedEvent): TenderInput {
  if (event.type !== loadReleasedType) throw new TypeError(`expected ${loadReleasedType}, got ${event.type}`);
  const { loadId, legalEntityId, releasedBy, carriers, currency, timeoutMin } = event.data;
  const input: TenderInput = {
    tenantId: event.tenantid,
    legalEntityId,
    loadId,
    carriers: (carriers ?? []).map((carrier) => ({ carrierId: carrier.carrierId, ...(carrier.rateQuoted ? { rateQuoted: carrier.rateQuoted } : {}) })),
    ...(currency ? { currency } : {}),
    ...(timeoutMin ? { timeoutMin } : {}),
    requestedBy: releasedBy,
  };
  validateTenderInput(input);
  return input;
}

export async function handleLoadReleased(
  client: Pick<Client, "workflow">,
  taskQueue: string,
  event: LoadReleasedEvent,
): Promise<{ workflowId: string; started: boolean }> {
  const input = toTenderInput(event);
  const workflowId = tenderWorkflowId(input.loadId);
  try {
    await client.workflow.start(tenderWorkflow, {
      workflowId,
      taskQueue,
      args: [input],
      workflowIdReusePolicy: WorkflowIdReusePolicy.ALLOW_DUPLICATE,
    });
    return { workflowId, started: true };
  } catch (error) {
    if (error instanceof WorkflowExecutionAlreadyStartedError) return { workflowId, started: false };
    throw error;
  }
}

export async function handleTenderOutcome(client: Pick<Client, "workflow">, event: TenderOutcomeEvent): Promise<{ workflowId: string; signalled: boolean }> {
  if (!(tenderOutcomeTypes as readonly string[]).includes(event.type)) throw new TypeError(`expected one of ${tenderOutcomeTypes.join(", ")}, got ${event.type}`);
  if (!event.data.loadId) throw new TypeError("tender outcome event is missing the load id");
  const workflowId = tenderWorkflowId(event.data.loadId);
  try {
    await client.workflow.getHandle(workflowId).signal(tenderResponseSignal);
    return { workflowId, signalled: true };
  } catch (error) {
    if (error instanceof WorkflowNotFoundError) return { workflowId, signalled: false };
    throw error;
  }
}
