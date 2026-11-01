import { ActivityFailure, condition, defineQuery, defineSignal, metricMeter, proxyActivities, setHandler, sleep } from "@temporalio/workflow";
import type { CapaActivities } from "./capaActivities.js";

export type EffectivenessResult = "EFFECTIVE" | "NOT_EFFECTIVE";

export interface CapaInput {
  tenantId: string;
  legalEntityId: string;
  ncrId: string;
  startedBy: string;
  effectivenessCheckDays?: number;
  stepTimeoutMs: number;
  effectivenessTimeoutMs: number;
  maxReopens: number;
}

export interface CapaRef {
  tenantId: string;
  legalEntityId: string;
  ncrId: string;
  actor: string;
}

export interface CapaNcr {
  id: string;
  status: string;
  version: number;
  correction?: {
    status: "OPEN" | "EFFECTIVENESS_PENDING" | "CLOSED";
    currentStep: number;
    effectivenessDueAt?: string;
    reopened: number;
    steps: { step: number; completedBy?: string }[];
  };
}

export interface CapaState {
  ncrId: string;
  tenantId: string;
  status: "in_correction" | "effectiveness_pending" | "awaiting_effectiveness" | "closed" | "stalled" | "escalated";
  currentStep: number;
  reopened: number;
  implementer?: string;
  refusedChecks: number;
  stalledAt?: "step" | "effectiveness";
}

interface StepDone {
  step: number;
  notes: string;
  actor: string;
}

interface EffectivenessChecked {
  result: EffectivenessResult;
  note: string;
  actor: string;
}

export const capaStepDoneSignal = defineSignal<[done: StepDone]>("capa-step-done");
export const capaEffectivenessSignal = defineSignal<[checked: EffectivenessChecked]>("capa-effectiveness");
export const capaStateQuery = defineQuery<CapaState>("capa-state");

const LAST_STEP = 7;

const activities = proxyActivities<CapaActivities>({
  startToCloseTimeout: "1 minute",
  scheduleToCloseTimeout: "1 hour",
  retry: { initialInterval: "1 second", maximumInterval: "1 minute", nonRetryableErrorTypes: ["ServiceCallRejected"] },
});

export function capaWorkflowId(ncrId: string): string {
  return `capa:${ncrId}`;
}

export function validateCapaInput(input: CapaInput): void {
  if (!input.tenantId || !input.legalEntityId || !input.ncrId || !input.startedBy) {
    throw new TypeError("tenantId, legalEntityId, ncrId and startedBy are required");
  }
  if (!(input.stepTimeoutMs > 0) || !(input.effectivenessTimeoutMs > 0)) throw new TypeError("stepTimeoutMs and effectivenessTimeoutMs must be positive");
  if (!Number.isInteger(input.maxReopens) || input.maxReopens < 0) throw new TypeError("maxReopens must be a whole number of at least 0");
}

export async function capaWorkflow(input: CapaInput): Promise<CapaState> {
  validateCapaInput(input);

  const state: CapaState = { ncrId: input.ncrId, tenantId: input.tenantId, status: "in_correction", currentStep: 1, reopened: 0, refusedChecks: 0 };
  const stepsDone: StepDone[] = [];
  const checks: EffectivenessChecked[] = [];
  setHandler(capaStateQuery, () => ({ ...state }));
  setHandler(capaStepDoneSignal, (done) => void stepsDone.push(done));
  setHandler(capaEffectivenessSignal, (checked) => {
    if (state.status === "awaiting_effectiveness") checks.push(checked);
  });

  const as = (actor: string): CapaRef => ({ tenantId: input.tenantId, legalEntityId: input.legalEntityId, ncrId: input.ncrId, actor });
  const stalled = (at: "step" | "effectiveness"): CapaState => {
    metricMeter.createCounter("passage_capa_stalled", "1", "CAPAs whose next step or effectiveness check never arrived").add(1, { ncr_id: input.ncrId, at });
    return Object.assign(state, { status: "stalled" as const, stalledAt: at });
  };
  const follow = (ncr: CapaNcr): void => {
    state.currentStep = ncr.correction?.currentStep ?? state.currentStep;
    state.reopened = ncr.correction?.reopened ?? state.reopened;
    state.implementer = ncr.correction?.steps.find((s) => s.step === LAST_STEP)?.completedBy;
  };

  let ncr = await activities.startCorrection({ ...as(input.startedBy), effectivenessCheckDays: input.effectivenessCheckDays });
  follow(ncr);

  for (;;) {
    while (ncr.correction?.status === "OPEN") {
      const wanted = ncr.correction.currentStep;
      const arrived = await condition(() => stepsDone.some((done) => done.step === wanted), input.stepTimeoutMs);
      if (!arrived) return stalled("step");
      const done = stepsDone.splice(stepsDone.findIndex((candidate) => candidate.step === wanted), 1)[0]!;
      ncr = await activities.completeStep({ ...as(done.actor), step: done.step, notes: done.notes });
      follow(ncr);
    }

    const correction = ncr.correction;
    if (correction?.status !== "EFFECTIVENESS_PENDING" || !correction.effectivenessDueAt) break;
    state.status = "effectiveness_pending";
    checks.length = 0;
    await sleep(Math.max(0, Date.parse(correction.effectivenessDueAt) - Date.now()));
    state.status = "awaiting_effectiveness";

    let checked = false;
    while (!checked) {
      if (!(await condition(() => checks.length > 0, input.effectivenessTimeoutMs))) return stalled("effectiveness");
      const candidate = checks.shift()!;
      try {
        ncr = await activities.checkEffectiveness({ ...as(candidate.actor), result: candidate.result, note: candidate.note });
        checked = true;
      } catch (error) {
        if (!(error instanceof ActivityFailure)) throw error;
        state.refusedChecks += 1;
      }
    }
    state.status = "in_correction";
    checks.length = 0;
    follow(ncr);

    if (ncr.correction?.status === "CLOSED") break;
    if (state.reopened > input.maxReopens) return Object.assign(state, { status: "escalated" as const });
  }

  return Object.assign(state, { status: ncr.status === "CLOSED" ? ("closed" as const) : state.status });
}
