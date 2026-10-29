/**
 * Run state machine (BR-47.1). PLANNING -> ACTING -> (AWAITING_APPROVAL ->
 * COMMITTED | REJECTED) | COMMITTED (auto) | FAILED | KILLED.
 * COMMITTED, REJECTED, KILLED and FAILED are terminal. FAILED is terminal by
 * design: the PRD lists no way out of it and a retry starts a new run.
 */
export const RUN_STATUSES = [
  "PLANNING",
  "ACTING",
  "AWAITING_APPROVAL",
  "COMMITTED",
  "REJECTED",
  "FAILED",
  "KILLED",
] as const;

export type RunStatus = (typeof RUN_STATUSES)[number];

const TRANSITIONS: Readonly<Record<RunStatus, readonly RunStatus[]>> = {
  PLANNING: ["ACTING", "FAILED", "KILLED"],
  ACTING: ["AWAITING_APPROVAL", "COMMITTED", "FAILED", "KILLED"],
  // FAILED from AWAITING_APPROVAL is the one deviation from BR-47.1: a commit
  // call that fails after a human approved is a real failure, not a rejection.
  AWAITING_APPROVAL: ["COMMITTED", "REJECTED", "KILLED", "FAILED"],
  COMMITTED: [],
  REJECTED: [],
  FAILED: [],
  KILLED: [],
};

export function canTransition(from: RunStatus, to: RunStatus): boolean {
  return TRANSITIONS[from].includes(to);
}

export function isTerminal(status: RunStatus): boolean {
  return TRANSITIONS[status].length === 0;
}

/** Returns the next status or throws; the caller keeps the previous value. */
export function transition(from: RunStatus, to: RunStatus): RunStatus {
  if (!canTransition(from, to)) {
    throw new Error(`invalid run transition ${from} -> ${to}`);
  }
  return to;
}
