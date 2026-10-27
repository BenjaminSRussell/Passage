// Flow runner (FR-32.2, FR-32.4, BR-32.2, §6.32.6 interaction rules). Pure: a device, or the
// designer's simulator, walks a validated flow one step at a time. Scans go through routeScan.
// Every evaluated step yields a StepTiming for the telemetry log (FR-32.11); a scan ignored
// because the focused step is not a scan step yields none (AC-32.7). Clocks are injected.

import { validateFlow, type FlowDraft, type FlowStep, type GraphIssue } from "./flowGraph";
import { routeScan, type ScanOptions, type ScanSource } from "./scanRouting";
import type { StepTiming } from "./telemetry";

export interface RunnerState {
  flowCode: string;
  /** Step awaiting input, or null once the flow has finished. */
  stepId: string | null;
  /** Captured value per step id: the scanned value, the typed value or the branch outcome. */
  values: Record<string, string>;
  /** GS1 fields per scan step id, when the scan was a GS1 element string (BR-32.1). */
  fields: Record<string, Record<string, string>>;
  /** Step ids already completed, oldest first. goBack pops the last one. */
  history: string[];
  /** Epoch ms when the current step became active. */
  stepStartedAt: number;
}

export interface RunnerContext {
  deviceId: string;
  userId: string;
}

export type StepInput =
  | { kind: "scan"; raw: string; source: ScanSource }
  | { kind: "value"; value: string }
  | { kind: "confirm" };

export type RunnerResult =
  | { ok: true; state: RunnerState; timing: StepTiming; finished: boolean }
  /** ignored: a scan arrived on a step that does not take scans. Nothing changed; log it. */
  | { ok: false; ignored: true; state: RunnerState; message: string }
  | { ok: false; ignored: false; state: RunnerState; message: string; code?: string; timing?: StepTiming };

export type StartResult = { ok: true; state: RunnerState } | { ok: false; issues: GraphIssue[] };

/** A flow that cannot run: graph failure, or a step or outcome missing from the flow. */
export const CODE_FLOW_INVALID = "MOB-0113";

/** Starts a flow at its entry step. A flow that fails publish validation does not start. */
export function startFlow(flow: FlowDraft, now: number): StartResult {
  const issues = validateFlow(flow);
  if (issues.length > 0) return { ok: false, issues };
  return { ok: true, state: { flowCode: flow.code, stepId: flow.entryStepId, values: {}, fields: {}, history: [], stepStartedAt: now } };
}

const hasKey = (o: Record<string, unknown>, k: string): boolean => Object.prototype.hasOwnProperty.call(o, k);

/** Steps that advance on a confirmation alone. */
const CONFIRMABLE = new Set<FlowStep["type"]>(["confirm", "display", "call"]);

/**
 * Submits input to the active step. In order:
 * 1. A finished flow takes no input.
 * 2. A scan on a step that is not a scan step is ignored, with no timing (AC-32.7).
 * 3. A scan on a scan step must pass routeScan (BR-32.2); a rejection does not advance.
 * 4. confirm, display and call steps advance on a confirmation; input and select need a
 *    non-empty value; a branch needs one of its outcomes.
 * 5. The next step is the branch outcome's target, else the "default" target, else the only one.
 */
export function submitStep(
  flow: FlowDraft,
  state: RunnerState,
  input: StepInput,
  ctx: RunnerContext,
  now: number,
  opts: ScanOptions = {},
): RunnerResult {
  if (state.stepId === null) return { ok: false, ignored: false, state, message: "The flow has already finished." };
  const step = flow.steps.find((s) => s.id === state.stepId);
  if (!step) {
    return { ok: false, ignored: false, state, message: `Step ${state.stepId} is not in flow ${flow.code}.`, code: CODE_FLOW_INVALID };
  }

  const timing = (failed: boolean): StepTiming => ({
    flowCode: flow.code,
    stepId: step.id,
    userId: ctx.userId,
    deviceId: ctx.deviceId,
    durationMs: Math.max(0, now - state.stepStartedAt),
    failed,
    at: now,
  });
  const reject = (message: string, code?: string): RunnerResult => ({
    ok: false,
    ignored: false,
    state,
    message,
    ...(code ? { code } : {}),
    timing: timing(true),
  });

  switch (input.kind) {
    case "scan": {
      const decision = routeScan(input.raw, step, opts);
      if (decision.kind === "ignored") return { ok: false, ignored: true, state, message: decision.reason };
      if (decision.kind === "rejected") return reject(decision.message, decision.code);
      const gs1 = decision.classified.gs1?.fields;
      return advance(state, step, decision.classified.value, gs1, undefined, timing(false));
    }
    case "confirm":
      if (!CONFIRMABLE.has(step.type)) return reject(`Step ${step.id} needs ${step.type === "scan" ? "a scan" : "a value"}.`);
      return advance(state, step, null, undefined, undefined, timing(false));
    case "value": {
      const value = input.value.trim();
      if (step.type === "input" || step.type === "select") {
        if (value === "") return reject(`Step ${step.id} needs a value.`);
        return advance(state, step, value, undefined, undefined, timing(false));
      }
      if (step.type === "branch") {
        if (!hasKey(step.next, value)) return reject(`Outcome "${value}" is not defined on step ${step.id}.`, CODE_FLOW_INVALID);
        return advance(state, step, value, undefined, value, timing(false));
      }
      return reject(`Step ${step.id} takes ${step.type === "scan" ? "a scan" : "a confirmation"}, not a typed value.`);
    }
  }
}

/** Moves past the step, recording its value (when not null) and its GS1 fields. */
function advance(
  state: RunnerState,
  step: FlowStep,
  value: string | null,
  gs1: Record<string, string> | undefined,
  outcome: string | undefined,
  timing: StepTiming,
): RunnerResult {
  const next = step.next;
  const keys = Object.keys(next);
  let target: string | null;
  if (outcome !== undefined) target = next[outcome] ?? null;
  else if (keys.length === 0) target = null;
  else if (hasKey(next, "default")) target = next["default"] ?? null;
  else if (keys.length === 1) target = next[keys[0]] ?? null;
  else {
    return {
      ok: false,
      ignored: false,
      state,
      message: `Step ${step.id} has several outcomes but no default.`,
      code: CODE_FLOW_INVALID,
      timing: { ...timing, failed: true },
    };
  }
  const values = value === null ? state.values : { ...state.values, [step.id]: value };
  const fields = { ...state.fields };
  if (gs1) fields[step.id] = { ...gs1 };
  else delete fields[step.id];
  const nextState: RunnerState = {
    flowCode: state.flowCode,
    stepId: target,
    values,
    fields,
    history: [...state.history, step.id],
    stepStartedAt: timing.at,
  };
  return { ok: true, state: nextState, timing, finished: target === null };
}

/** Hardware back returns one step (§6.32.6). At the first step nothing changes. */
export function goBack(state: RunnerState, now: number): RunnerState {
  if (state.history.length === 0) return state;
  return {
    ...state,
    stepId: state.history[state.history.length - 1],
    history: state.history.slice(0, -1),
    stepStartedAt: now,
  };
}
