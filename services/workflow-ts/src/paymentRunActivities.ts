import { ApplicationFailure, Context } from "@temporalio/activity";
import type { PaymentRunInput, PaymentRunRef, PaymentRunState } from "./paymentRunWorkflow.js";
import type { ServiceTokenSigner } from "./serviceToken.js";

export interface PaymentRunActivities {
  startPaymentRun(input: PaymentRunInput): Promise<PaymentRunState>;
  generatePaymentFile(ref: PaymentRunRef): Promise<PaymentRunState>;
  postPaymentJournal(ref: PaymentRunRef): Promise<PaymentRunState>;
  settlePaymentRun(ref: PaymentRunRef): Promise<PaymentRunState>;
  completePaymentRun(ref: PaymentRunRef): Promise<PaymentRunState>;
  failPaymentRun(ref: PaymentRunRef & { reason: string }): Promise<PaymentRunState>;
}

const REJECTED = "PaymentRunRejected";
const RETRYABLE_CLIENT_STATUSES = [408, 429];
export const MAX_REASON_BYTES = 500;

interface Identity {
  tenantId: string;
  legalEntityId: string;
  executedBy: string;
}

export function truncateToBytes(text: string, maxBytes: number): string {
  const bytes = new TextEncoder().encode(text);
  if (bytes.length <= maxBytes) return text;
  return new TextDecoder().decode(bytes.slice(0, maxBytes)).replace(/\uFFFD+$/, "");
}

export function createPaymentRunActivities(
  baseUrl: string,
  signer: ServiceTokenSigner,
  fetchImpl: typeof fetch = fetch,
): PaymentRunActivities {
  async function call(identity: Identity, path: string, body?: unknown): Promise<PaymentRunState> {
    const response = await fetchImpl(`${baseUrl}/v1/payables/payment-runs${path}`, {
      method: "POST",
      headers: {
        "X-Passage-Service-Token": signer.mintForPayables({
          tenantId: identity.tenantId,
          legalEntityId: identity.legalEntityId,
          actor: identity.executedBy,
        }),
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
    startPaymentRun: (input) => call(input, "", { legalEntityId: input.legalEntityId, proposalId: input.proposalId }),
    generatePaymentFile: step("generate-file"),
    postPaymentJournal: step("post-journal"),
    settlePaymentRun: step("settle"),
    completePaymentRun: step("complete"),
    failPaymentRun: (ref) => call(ref, `/${ref.runId}:fail`, { reason: truncateToBytes(ref.reason, MAX_REASON_BYTES) }),
  };
}

async function failure(path: string, response: Response): Promise<Error> {
  const problem = await problemBody(response);
  const message = `payables payment run ${path || "start"} failed with ${response.status}${problem.detail ? `: ${problem.detail}` : ""}`;
  const businessRule = problem.serviceCode?.startsWith("AP-") === true;
  const authFault = response.status === 401 || (response.status === 403 && !businessRule);
  const client = response.status >= 400 && response.status < 500 && !RETRYABLE_CLIENT_STATUSES.includes(response.status) && !authFault;
  return client ? ApplicationFailure.nonRetryable(message, REJECTED) : new Error(message);
}

async function problemBody(response: Response): Promise<{ detail?: string; serviceCode?: string }> {
  try {
    return (await response.json()) as { detail?: string; serviceCode?: string };
  } catch {
    return {};
  }
}
