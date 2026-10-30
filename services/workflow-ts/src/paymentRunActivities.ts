import { ApplicationFailure, Context } from "@temporalio/activity";
import type { PaymentRunInput, PaymentRunRef, PaymentRunState } from "./paymentRunWorkflow.js";

export interface PaymentRunActivities {
  startPaymentRun(input: PaymentRunInput): Promise<PaymentRunState>;
  generatePaymentFile(ref: PaymentRunRef): Promise<PaymentRunState>;
  postPaymentJournal(ref: PaymentRunRef): Promise<PaymentRunState>;
  settlePaymentRun(ref: PaymentRunRef): Promise<PaymentRunState>;
  completePaymentRun(ref: PaymentRunRef): Promise<PaymentRunState>;
  failPaymentRun(ref: PaymentRunRef & { reason: string }): Promise<PaymentRunState>;
}

interface Identity {
  tenantId: string;
  legalEntityId: string;
  executedBy: string;
}

const REJECTED = "PaymentRunRejected";
const RETRYABLE_CLIENT_STATUSES = [408, 429];

export function createPaymentRunActivities(baseUrl: string, fetchImpl: typeof fetch = fetch): PaymentRunActivities {
  // TODO: #226 the hardcoded execute and ledger duties and the header actor are interim until service identity lands.
  async function call(identity: Identity, path: string, body?: unknown): Promise<PaymentRunState> {
    const response = await fetchImpl(`${baseUrl}/v1/payables/payment-runs${path}`, {
      method: "POST",
      headers: {
        "X-Passage-Tenant": identity.tenantId,
        "X-Passage-Legal-Entity": identity.legalEntityId,
        "X-Passage-User": identity.executedBy,
        "X-Passage-Duties": "payment-run.execute,ledger.journal.post,ledger.journal.read",
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      signal: Context.current().cancellationSignal,
    });
    if (!response.ok) throw await failure(path, response);
    return (await response.json()) as PaymentRunState;
  }

  const step = (action: string) => (ref: PaymentRunRef) => call(ref, `/${ref.runId}:${action}`);

  return {
    startPaymentRun: (input) =>
      call(input, "", { ...input.request, legalEntityId: input.legalEntityId, proposalId: input.proposalId }),
    generatePaymentFile: step("generate-file"),
    postPaymentJournal: step("post-journal"),
    settlePaymentRun: step("settle"),
    completePaymentRun: step("complete"),
    failPaymentRun: (ref) => call(ref, `/${ref.runId}:fail`, { reason: ref.reason }),
  };
}

async function failure(path: string, response: Response): Promise<Error> {
  const detail = await problemDetail(response);
  const message = `payables payment run ${path || "start"} failed with ${response.status}${detail ? `: ${detail}` : ""}`;
  const client = response.status >= 400 && response.status < 500 && !RETRYABLE_CLIENT_STATUSES.includes(response.status);
  return client ? ApplicationFailure.nonRetryable(message, REJECTED) : new Error(message);
}

async function problemDetail(response: Response): Promise<string | undefined> {
  try {
    return ((await response.json()) as { detail?: string }).detail;
  } catch {
    return undefined;
  }
}
