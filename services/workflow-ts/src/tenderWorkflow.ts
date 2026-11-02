import { condition, defineQuery, defineSignal, metricMeter, proxyActivities, setHandler, sleep, workflowInfo } from "@temporalio/workflow";
import type { TenderActivities } from "./tenderActivities.js";

export interface TenderCarrier {
  carrierId: string;
  rateQuoted?: string;
}

export interface TenderInput {
  tenantId: string;
  legalEntityId: string;
  loadId: string;
  carriers: TenderCarrier[];
  currency?: string;
  timeoutMin?: number;
  requestedBy: string;
}

export interface TenderRef {
  tenantId: string;
  legalEntityId: string;
  actor: string;
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

const activities = proxyActivities<TenderActivities>({
  startToCloseTimeout: "1 minute",
  scheduleToCloseTimeout: "1 hour",
  retry: { initialInterval: "1 second", maximumInterval: "1 minute", nonRetryableErrorTypes: ["ServiceCallRejected"] },
});

export function tenderWorkflowId(loadId: string): string {
  return `tender:${loadId}`;
}

export function validateTenderInput(input: TenderInput): void {
  if (!input.tenantId || !input.legalEntityId || !input.loadId || !input.requestedBy) {
    throw new TypeError("tenantId, legalEntityId, loadId and requestedBy are required");
  }
  if (input.carriers.length === 0) throw new TypeError("at least one carrier is required");
  if (new Set(input.carriers.map((carrier) => carrier.carrierId)).size !== input.carriers.length) throw new TypeError("list each carrier once, in rank order");
}

export async function tenderWorkflow(input: TenderInput): Promise<TenderState> {
  validateTenderInput(input);

  const state: TenderState = { loadId: input.loadId, status: "tendering", attempted: [] };
  let nudged = false;
  setHandler(tenderStateQuery, () => ({ ...state, attempted: [...state.attempted] }));
  setHandler(tenderResponseSignal, () => {
    nudged = true;
  });

  const ref: TenderRef = { tenantId: input.tenantId, legalEntityId: input.legalEntityId, actor: input.requestedBy };
  const follow = (tender: TenderView): TenderView => {
    state.tenderId = tender.id;
    state.pendingCarrierId = tender.pendingCarrierId;
    state.attempted = tender.attempts.map((attempt) => attempt.carrierId);
    state.acceptedCarrierId = tender.acceptedCarrierId;
    return tender;
  };
  const counter = (name: string, description: string) => metricMeter.createCounter(name, "1", description).add(1, { load_id: input.loadId });

  let tender = follow(
    await activities.startTender({ ...ref, loadId: input.loadId, carriers: input.carriers, currency: input.currency, timeoutMin: input.timeoutMin, idempotencyKey: `tender:${workflowInfo().runId}` }),
  );

  let stuckSweeps = 0;
  while (tender.status === "PENDING") {
    if (tender.overdue) {
      const attempts = tender.attempts.length;
      await activities.sweepExpiredTenders(ref);
      tender = follow(await activities.getTender({ ...ref, tenderId: tender.id }));
      if (tender.status !== "PENDING") break;
      stuckSweeps = tender.attempts.length === attempts ? stuckSweeps + 1 : 0;
      if (stuckSweeps >= MAX_STUCK_SWEEPS) {
        counter("passage_tender_stalled", "Tenders whose overdue attempt the expiry sweep could not advance");
        return Object.assign(state, { status: "stalled" as const });
      }
      if (tender.overdue) await sleep(SWEEP_RETRY_MS);
      continue;
    }
    nudged = false;
    const untilDeadline = tender.pendingExpiresAt ? Math.max(0, Date.parse(tender.pendingExpiresAt) - Date.now()) : 0;
    await condition(() => nudged, untilDeadline + SWEEP_GRACE_MS);
    tender = follow(await activities.getTender({ ...ref, tenderId: tender.id }));
  }

  if (tender.status === "ACCEPTED") return Object.assign(state, { status: "accepted" as const });
  counter("passage_tender_escalated", "Tenders every carrier declined or let expire, left to a planner");
  return Object.assign(state, { status: "escalated" as const });
}
