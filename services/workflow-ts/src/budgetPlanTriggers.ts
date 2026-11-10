import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy } from "@temporalio/client";
import type { Client } from "@temporalio/client";
import { budgetPlanWorkflow, budgetPlanWorkflowId, validateBudgetPlanInput, type BudgetPlanInput } from "./budgetPlanWorkflow.js";

export const planSubmittedType = "dev.passage.budget.plan.submitted.v1";
/** The only producer of plan.submitted: budgeting's outbox. An event from any other source starts nothing. */
export const planSubmittedSource = "budgeting";

export interface PlanSubmittedEvent {
  type: string;
  source: string;
  tenantid: string;
  data: {
    plan_id: string;
    legal_entity_id: string;
    submission: number;
    submitted_by: string;
  };
}

export function toBudgetPlanInput(event: PlanSubmittedEvent): BudgetPlanInput {
  if (event.type !== planSubmittedType) throw new TypeError(`expected ${planSubmittedType}, got ${event.type}`);
  if (event.source !== planSubmittedSource) throw new TypeError(`${planSubmittedType} is accepted only from ${planSubmittedSource}, got ${event.source}`);
  const { plan_id, legal_entity_id, submission, submitted_by } = event.data;
  const input: BudgetPlanInput = {
    tenantId: event.tenantid,
    legalEntityId: legal_entity_id,
    planId: plan_id,
    submission,
    requester: submitted_by,
  };
  validateBudgetPlanInput(input);
  return input;
}

/** Starts the approval workflow of one submission; a redelivered event finds it running or finished and starts no second one. */
export async function handlePlanSubmitted(
  client: Pick<Client, "workflow">,
  taskQueue: string,
  event: PlanSubmittedEvent,
): Promise<{ workflowId: string; started: boolean }> {
  const input = toBudgetPlanInput(event);
  const workflowId = budgetPlanWorkflowId(input.planId, input.submission);
  try {
    await client.workflow.start(budgetPlanWorkflow, {
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
