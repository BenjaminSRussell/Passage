import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy } from "@temporalio/client";
import type { Client } from "@temporalio/client";
import { paymentRunWorkflow, paymentRunWorkflowId, validatePaymentRunInput, type PaymentRunInput } from "./paymentRunWorkflow.js";

export async function startPaymentRunWorkflow(
  client: Pick<Client, "workflow">,
  taskQueue: string,
  input: PaymentRunInput,
): Promise<{ workflowId: string; started: boolean }> {
  validatePaymentRunInput(input);
  const workflowId = paymentRunWorkflowId(input.legalEntityId, input.proposalId);
  try {
    await client.workflow.start(paymentRunWorkflow, {
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
