// Flow graph model and publish-time validation (FR-32.2, BR-32.9). Pure.

import type { ScanClass } from "./gs1";

export const STEP_TYPES = ["scan", "input", "select", "confirm", "display", "branch", "call"] as const;
export type StepType = (typeof STEP_TYPES)[number];

export const SCAN_CLASSES: readonly ScanClass[] = ["LP", "GTIN", "LOCATION", "ANY"];

export interface FlowStep {
  id: string;
  type: StepType;
  prompt: string;
  /** Required for scan steps; ignored otherwise. */
  expectedScan?: ScanClass[];
  /** Outcome label to next step. A null target ends the flow. An empty map also ends it. */
  next: Record<string, string | null>;
}

export interface FlowDraft {
  code: string;
  entryStepId: string;
  steps: FlowStep[];
}

export interface GraphIssue {
  /** Stable message catalog code (MOB-0113 for graph failures). */
  code: "MOB-0113";
  stepId?: string;
  message: string;
}

const ID_FORMAT = /^[a-z][a-z0-9_]{0,39}$/;

/** Validates a draft before publish. Returns an empty list when the flow may be published. */
export function validateFlow(flow: FlowDraft): GraphIssue[] {
  const issues: GraphIssue[] = [];
  const add = (stepId: string | undefined, message: string) => issues.push({ code: "MOB-0113", stepId, message });

  if (flow.steps.length === 0) {
    add(undefined, "flow has no steps");
    return issues;
  }

  const byId = new Map<string, FlowStep>();
  for (const s of flow.steps) {
    if (!ID_FORMAT.test(s.id)) add(s.id, `step id ${s.id} must be lower snake case`);
    if (byId.has(s.id)) add(s.id, `step id ${s.id} is duplicated`);
    byId.set(s.id, s);
    if (!STEP_TYPES.includes(s.type)) add(s.id, `step ${s.id} has unknown type ${String(s.type)}`);
    if (s.type === "scan" && (!s.expectedScan || s.expectedScan.length === 0)) {
      add(s.id, `scan step ${s.id} must declare an expected scan class`);
    }
    if (s.type === "branch" && Object.keys(s.next).length < 2) {
      add(s.id, `branch step ${s.id} needs at least two outcomes`);
    }
  }

  if (!byId.has(flow.entryStepId)) {
    add(undefined, `entry step ${flow.entryStepId} does not exist`);
    return issues;
  }

  // Dangling targets.
  for (const s of flow.steps) {
    for (const [outcome, target] of Object.entries(s.next)) {
      if (target !== null && !byId.has(target)) add(s.id, `step ${s.id} outcome ${outcome} points to missing step ${target}`);
    }
  }

  // Reachability from the entry step.
  const reachable = new Set<string>();
  const stack = [flow.entryStepId];
  while (stack.length > 0) {
    const id = stack.pop() as string;
    if (reachable.has(id)) continue;
    reachable.add(id);
    for (const target of Object.values(byId.get(id)?.next ?? {})) {
      if (target !== null && byId.has(target)) stack.push(target);
    }
  }
  for (const s of flow.steps) {
    if (!reachable.has(s.id)) add(s.id, `step ${s.id} is unreachable from the entry step`);
  }

  // Termination: every reachable step must reach an end. Walk backwards from terminal steps.
  // A step is an exit when it has no outcomes or any outcome ends the flow.
  const terminal = (s: FlowStep) => Object.keys(s.next).length === 0 || Object.values(s.next).some((t) => t === null);
  const reverse = new Map<string, string[]>();
  for (const s of flow.steps) {
    for (const t of Object.values(s.next)) {
      if (t !== null && byId.has(t)) reverse.set(t, [...(reverse.get(t) ?? []), s.id]);
    }
  }
  const finishing = new Set<string>();
  const queue = flow.steps.filter(terminal).map((s) => s.id);
  while (queue.length > 0) {
    const id = queue.pop() as string;
    if (finishing.has(id)) continue;
    finishing.add(id);
    for (const prev of reverse.get(id) ?? []) queue.push(prev);
  }
  for (const id of reachable) {
    if (!finishing.has(id)) add(id, `step ${id} cannot reach the end of the flow`);
  }

  return issues;
}
