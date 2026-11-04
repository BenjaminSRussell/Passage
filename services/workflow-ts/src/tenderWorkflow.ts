import { ActivityFailure, ApplicationFailure, condition, defineQuery, defineSignal, metricMeter, proxyActivities, setHandler, sleep, workflowInfo } from "@temporalio/workflow";
import type { TenderActivities } from "./tenderActivities.js";

export interface TenderCarrier {
  carrierId: string;
  rateQuoted?: string;
}

export interface TenderInput {
  tenantId: string;
  legalEntityId: string;
  loadId: string;
  legalEntityScope: string[];
  carriers: TenderCarrier[];
  currency?: string;
  timeoutMin?: number;
  requestedBy: string;
  /**
   * Wait after a read or sweep that ran out of its activity window, before trying again (default OUTAGE_RETRY_MS).
   * Not mapped from any event; tests lengthen it so a week-long outage costs a few windows instead of a few hundred.
   */
  outageRetryMs?: number;
}

export interface TenderRef {
  tenantId: string;
  legalEntityId: string;
  actor: string;
  scope: string[];
}

export interface TenderView {
  id: string;
  loadId: string;
  status: "PENDING" | "ACCEPTED" | "TENDER_FAILED";
  attempts: { rank: number; carrierId: string; response: string }[];
  acceptedCarrierId?: string;
  plannerTask: boolean;
  pendingCarrierId?: string;
  pendingExpiresAt?: string;
  overdue: boolean;
}

export interface TenderState {
  loadId: string;
  tenderId?: string;
  status: "tendering" | "accepted" | "escalated" | "stalled";
  pendingCarrierId?: string;
  attempted: string[];
  acceptedCarrierId?: string;
}

export const tenderResponseSignal = defineSignal("tender-response");
export const tenderStateQuery = defineQuery<TenderState>("tender-state");

const SWEEP_GRACE_MS = 5_000;
const SWEEP_RETRY_MS = 60_000;
const MAX_STUCK_SWEEPS = 5;
// A read or sweep that exhausts its one-hour activity window is retried five minutes later; the workflow gives up only after
// transport has been unreachable for seven days of workflow time, so a long outage does not end a multi-day tender.
export const MAX_OUTAGE_MS = 7 * 24 * 60 * 60_000;
export const OUTAGE_RETRY_MS = 5 * 60_000;
const INVALID_INPUT = "InvalidTenderInput";

const activities = proxyActivities<TenderActivities>({
  startToCloseTimeout: "1 minute",
  scheduleToCloseTimeout: "1 hour",
  retry: { initialInterval: "1 second", maximumInterval: "1 minute", nonRetryableErrorTypes: ["ServiceCallRejected"] },
});

export function tenderWorkflowId(tenantId: string, loadId: string): string {
  return `tender:${tenantId}:${loadId}`;
}

export function validateTenderInput(input: TenderInput): void {
  const invalid = (message: string): never => {
    throw ApplicationFailure.nonRetryable(message, INVALID_INPUT);
  };
  if (!input.tenantId || !input.legalEntityId || !input.loadId || !input.requestedBy) invalid("tenantId, legalEntityId, loadId and requestedBy are required");
  if (!(input.legalEntityScope ?? []).includes(input.legalEntityId)) invalid("legalEntityScope must include the load's legal entity");
  if (input.carriers.length === 0) invalid("at least one carrier is required");
  if (input.outageRetryMs !== undefined && !(input.outageRetryMs >= 60_000)) invalid("outageRetryMs must be at least one minute");
  if (new Set(input.carriers.map((carrier) => carrier.carrierId)).size !== input.carriers.length) invalid("list each carrier once, in rank order");
}

function outage(error: unknown): boolean {
  return error instanceof ActivityFailure && error.retryState === "TIMEOUT";
}

export async function tenderWorkflow(input: TenderInput): Promise<TenderState> {
  validateTenderInput(input);

  const state: TenderState = { loadId: input.loadId, status: "tendering", attempted: [] };
  let nudged = false;
  setHandler(tenderStateQuery, () => ({ ...state, attempted: [...state.attempted] }));
  setHandler(tenderResponseSignal, () => {
    nudged = true;
  });

  const ref: TenderRef = { tenantId: input.tenantId, legalEntityId: input.legalEntityId, actor: input.requestedBy, scope: input.legalEntityScope };
  const follow = (tender: TenderView): TenderView => {
    state.tenderId = tender.id;
    state.pendingCarrierId = tender.pendingCarrierId;
    state.attempted = tender.attempts.map((attempt) => attempt.carrierId);
    state.acceptedCarrierId = tender.acceptedCarrierId;
    return tender;
  };
  const counter = (name: string, description: string, attributes: Record<string, string> = {}) => metricMeter.createCounter(name, "1", description).add(1, attributes);

  const stall = (reason: "sweep_stuck" | "transport_unreachable"): TenderState => {
    counter("passage_tender_stalled", "Tenders the workflow gave up on: the expiry sweep did not advance them or transport stayed unreachable", { reason });
    return Object.assign(state, { status: "stalled" as const });
  };

  let tender = follow(
    await activities.startTender({ ...ref, loadId: input.loadId, carriers: input.carriers, currency: input.currency, timeoutMin: input.timeoutMin, idempotencyKey: `tender:${workflowInfo().firstExecutionRunId}` }),
  );

  let stuckSweeps = 0;
  let outageSince: number | undefined;
  while (tender.status === "PENDING") {
    try {
      if (tender.overdue) {
        const attempts = tender.attempts.length;
        nudged = false;
        await activities.sweepExpiredTenders(ref);
        tender = follow(await activities.getTender({ ...ref, tenderId: tender.id }));
        outageSince = undefined;
        if (tender.status !== "PENDING") break;
        stuckSweeps = tender.attempts.length === attempts ? stuckSweeps + 1 : 0;
        if (stuckSweeps >= MAX_STUCK_SWEEPS) {
          return stall("sweep_stuck");
        }
        if (tender.overdue) await sleep(SWEEP_RETRY_MS);
        continue;
      }
      const untilDeadline = tender.pendingExpiresAt ? Math.max(0, Date.parse(tender.pendingExpiresAt) - Date.now()) : 0;
      await condition(() => nudged, untilDeadline + SWEEP_GRACE_MS);
      nudged = false;
      tender = follow(await activities.getTender({ ...ref, tenderId: tender.id }));
      outageSince = undefined;
    } catch (error) {
      if (!outage(error)) throw error;
      outageSince ??= Date.now();
      if (Date.now() - outageSince >= MAX_OUTAGE_MS) {
        return stall("transport_unreachable");
      }
      await sleep(input.outageRetryMs ?? OUTAGE_RETRY_MS);
    }
  }

  if (tender.status === "ACCEPTED") return Object.assign(state, { status: "accepted" as const });
  counter("passage_tender_escalated", "Tenders every carrier declined or let expire, left to a planner");
  return Object.assign(state, { status: "escalated" as const });
}
