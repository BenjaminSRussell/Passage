import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy } from "@temporalio/client";
import type { Client } from "@temporalio/client";
import { approvalWorkflow, approvalWorkflowId, type ApprovalInput, type ApprovalLevel } from "./workflows.js";

export const changeRequestedType = "dev.passage.party.change.requested.v1";

export interface ChangeRequestedEvent {
  type: string;
  tenantid: string;
  data: {
    change_id: string;
    legal_entity_id: string;
    field: string;
    requested_by: string;
  };
}

export function toApprovalInput(event: ChangeRequestedEvent, levels: ApprovalLevel[]): ApprovalInput {
  if (event.type !== changeRequestedType) {
    throw new TypeError(`expected ${changeRequestedType}, got ${event.type}`);
  }
  const { change_id, legal_entity_id, field, requested_by } = event.data;
  if (field !== "credit_limit") throw new TypeError(`unsupported pending change field ${field}`);
  if (!event.tenantid || !change_id || !legal_entity_id || !requested_by) {
    throw new TypeError("change.requested event is missing tenant, change, legal entity or requester");
  }
  return {
    requestId: change_id,
    entityType: "credit_limit",
    levels,
    requester: requested_by,
    partyChange: { changeId: change_id, tenantId: event.tenantid, legalEntityId: legal_entity_id },
  };
}

export async function handleChangeRequested(
  client: Pick<Client, "workflow">,
  taskQueue: string,
  levels: ApprovalLevel[],
  event: ChangeRequestedEvent,
): Promise<{ workflowId: string; started: boolean }> {
  const input = toApprovalInput(event, levels);
  const workflowId = approvalWorkflowId(input.requestId);
  try {
    await client.workflow.start(approvalWorkflow, {
      workflowId,
      taskQueue,
      args: [input],
      workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE,
    });
    return { workflowId, started: true };
  } catch (error) {
    if (error instanceof WorkflowExecutionAlreadyStartedError) return { workflowId, started: false };
    throw error;
  }
}
