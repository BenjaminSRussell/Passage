import { ActivityFailure, metricMeter, proxyActivities } from "@temporalio/workflow";
import type { PaymentRunActivities } from "./paymentRunActivities.js";

export interface PaymentRunInput {
  tenantId: string;
  legalEntityId: string;
  proposalId: string;
  executedBy: string;
}

export interface PaymentRunRef {
  tenantId: string;
  legalEntityId: string;
  executedBy: string;
  runId: string;
}

export interface PaymentRunState {
  id: string;
  status: "RUNNING" | "COMPLETED" | "FAILED";
  failureReason?: string;
  journalEntryId?: string;
}

export interface PaymentRunResult {
  runId: string;
  status: "COMPLETED" | "FAILED";
  failureReason?: string;
  journalEntryId?: string;
}

export const REJECTED = "PaymentRunRejected";
const GENERATE_ATTEMPTS = 5;

const RETRY_BUDGET = "24 hours";

const durable = proxyActivities<PaymentRunActivities>({
  startToCloseTimeout: "1 minute",
  scheduleToCloseTimeout: RETRY_BUDGET,
  retry: { initialInterval: "1 second", maximumInterval: "1 minute", nonRetryableErrorTypes: [REJECTED] },
});

const bounded = proxyActivities<PaymentRunActivities>({
  startToCloseTimeout: "1 minute",
  retry: { initialInterval: "1 second", maximumInterval: "1 minute", maximumAttempts: GENERATE_ATTEMPTS, nonRetryableErrorTypes: [REJECTED] },
});

export function paymentRunWorkflowId(legalEntityId: string, proposalId: string): string {
  return `payment-run:${legalEntityId}:${proposalId}`;
}

export function validatePaymentRunInput(input: PaymentRunInput): void {
  if (!input.tenantId || !input.legalEntityId || !input.proposalId || !input.executedBy) {
    throw new TypeError("tenantId, legalEntityId, proposalId and executedBy are required");
  }
}

function summary(run: PaymentRunState): PaymentRunResult {
  return {
    runId: run.id,
    status: run.status === "COMPLETED" ? "COMPLETED" : "FAILED",
    ...(run.failureReason ? { failureReason: run.failureReason } : {}),
    ...(run.journalEntryId ? { journalEntryId: run.journalEntryId } : {}),
  };
}

export async function paymentRunWorkflow(input: PaymentRunInput): Promise<PaymentRunResult> {
  validatePaymentRunInput(input);

  const started = await durable.startPaymentRun(input);
  if (started.status !== "RUNNING") return summary(started);
  const ref: PaymentRunRef = { tenantId: input.tenantId, legalEntityId: input.legalEntityId, executedBy: input.executedBy, runId: started.id };

  const stuck = metricMeter.createCounter("passage_payment_run_stuck", "1", "Payment runs that could not finish after their journal was requested");
  try {
    await bounded.generatePaymentFile(ref);
    await durable.postPaymentJournal(ref);
  } catch (error) {
    if (!(error instanceof ActivityFailure)) throw error;
    try {
      return summary(await durable.failPaymentRun({ ...ref, reason: describe(error) }));
    } catch (failure) {
      if (failure instanceof ActivityFailure) stuck.add(1, { run_id: ref.runId });
      throw failure;
    }
  }
  try {
    await durable.settlePaymentRun(ref);
    return summary(await durable.completePaymentRun(ref));
  } catch (error) {
    if (error instanceof ActivityFailure) stuck.add(1, { run_id: ref.runId });
    throw error;
  }
}

function describe(error: ActivityFailure): string {
  return (error.cause?.message ?? error.message).slice(0, 500);
}
