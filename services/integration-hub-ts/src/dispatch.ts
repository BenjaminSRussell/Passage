// Dispatch state machine and replay planning for webhook subscriptions (FR-35.9,
// BR-35.7, BR-35.9). Pure: the dispatcher reads the clock, performs the HTTP
// call and the store writes, and asks this module what happens next.

import { decide, retryDelaySeconds } from "./webhook.ts";

/** Replay cap per request (BR-35.9, parameter replay_max_events default). */
export const REPLAY_MAX_EVENTS = 1_000_000;

/** Upper bound accepted for a configured replay cap (parameter range 1..10,000,000). */
export const REPLAY_CAP_LIMIT = 10_000_000;

/** The outcome of one delivery attempt and what the dispatcher records next. */
export type Step =
  | { kind: "delivered"; attempts: number }
  | { kind: "paused"; attempts: number }
  | { kind: "retry"; attempts: number; nextRetryAt: number }
  | { kind: "dead_letter"; attempts: number; code?: "HUB-0115" | "HUB-0117" };

/**
 * Decides the next step after an attempt. `retriesDone` counts retries already
 * made before this attempt (0 for the first attempt). `status` is the receiver's
 * HTTP status, or null when no response arrived (connection refused, timeout);
 * a null status is always retried. The schedule has 8 retries (BR-35.7); after
 * the last one the event is dead-lettered (HUB-0115).
 */
export function nextStep(retriesDone: number, status: number | null, nowSeconds: number): Step {
  if (!Number.isInteger(retriesDone) || retriesDone < 0) {
    throw new RangeError(`retriesDone must be a non-negative integer, got ${retriesDone}`);
  }
  const attempts = retriesDone + 1;
  if (status !== null) {
    const decision = decide(status);
    if (decision.kind === "delivered") {
      return { kind: "delivered", attempts };
    }
    if (decision.kind === "pause") {
      return { kind: "paused", attempts };
    }
  }
  const delay = retryDelaySeconds(retriesDone + 1);
  if (delay === null) {
    return { kind: "dead_letter", attempts };
  }
  return { kind: "retry", attempts, nextRetryAt: nowSeconds + delay };
}

/** An event that can be replayed: its time, aggregate and per-aggregate sequence. */
export interface ReplayCandidate {
  eventId: string;
  occurredAt: number;
  aggregateId: string;
  sequence: number;
}

/** Where the next page of a capped replay starts: the same `fromSeconds` and this many events skipped. */
export interface ReplayCursor {
  fromSeconds: number;
  skip: number;
}

/**
 * One page of a bounded replay. `events` are in delivery order. When
 * `truncated` is true, `next` continues the replay with no event repeated or
 * lost, even when more than `cap` events share one timestamp.
 */
export interface ReplayPlan {
  events: ReplayCandidate[];
  truncated: boolean;
  next: ReplayCursor | null;
}

function compareText(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/**
 * Delivery order for a replay: by time, but each aggregate's events take the
 * time slots of that aggregate in sequence order. A producer clock that runs
 * behind can stamp sequence 2 earlier than sequence 1; the aggregate is still
 * replayed 1 then 2, so a consumer rebuilds the same state (BR-35.7, AC-35.4).
 * The order is total (event id breaks ties), so paging by position is stable.
 */
export function deliveryOrder(events: readonly ReplayCandidate[]): ReplayCandidate[] {
  const bySequence = (a: ReplayCandidate, b: ReplayCandidate) => a.sequence - b.sequence || compareText(a.eventId, b.eventId);
  const byTime = [...events].sort(
    (a, b) => a.occurredAt - b.occurredAt || compareText(a.aggregateId, b.aggregateId) || bySequence(a, b),
  );
  const slots = new Map<string, number[]>();
  const members = new Map<string, ReplayCandidate[]>();
  byTime.forEach((event, index) => {
    slots.set(event.aggregateId, [...(slots.get(event.aggregateId) ?? []), index]);
    members.set(event.aggregateId, [...(members.get(event.aggregateId) ?? []), event]);
  });
  const ordered: ReplayCandidate[] = new Array(byTime.length);
  for (const [aggregateId, list] of members) {
    const positions = slots.get(aggregateId) ?? [];
    list.sort(bySequence).forEach((event, k) => {
      ordered[positions[k]] = event;
    });
  }
  return ordered;
}

/**
 * Plans one page of a replay from `fromSeconds` (inclusive), skipping the first
 * `skip` events of the delivery order and taking at most `cap`. Throws
 * RangeError when the cap is outside 1..REPLAY_CAP_LIMIT or skip is not a
 * non-negative integer.
 */
export function planReplay(
  events: readonly ReplayCandidate[],
  fromSeconds: number,
  cap: number = REPLAY_MAX_EVENTS,
  skip: number = 0,
): ReplayPlan {
  if (!Number.isInteger(cap) || cap < 1 || cap > REPLAY_CAP_LIMIT) {
    throw new RangeError(`replay cap must be an integer in 1..${REPLAY_CAP_LIMIT}, got ${cap}`);
  }
  if (!Number.isInteger(skip) || skip < 0) {
    throw new RangeError(`replay skip must be a non-negative integer, got ${skip}`);
  }
  const remaining = deliveryOrder(events.filter((e) => e.occurredAt >= fromSeconds)).slice(skip);
  const truncated = remaining.length > cap;
  return {
    events: remaining.slice(0, cap),
    truncated,
    next: truncated ? { fromSeconds, skip: skip + cap } : null,
  };
}

/** Headers that mark a delivery as a replay so consumers can apply it idempotently (BR-35.9). */
export function replayHeaders(): Record<string, string> {
  return { "ce-passagereplay": "true" };
}

/** A Retry-After longer than the last schedule step is not honoured beyond it. */
const MAX_RETRY_AFTER_SECONDS = 86_400;

/**
 * The next step after a rest-push attempt. `dead` dead-letters at once (HUB-0117: a 4xx the receiver will
 * answer the same way every time), `pause` is a 410, `retry` follows the 8-step schedule, or the receiver's
 * Retry-After when it sent one (429 and 503), and dead-letters (HUB-0115) when the schedule is spent.
 */
export function restStep(
  retriesDone: number,
  outcome: { kind: "delivered" | "retry" | "dead" | "pause"; retryAfterSeconds?: number },
  nowSeconds: number,
): Step {
  const attempts = retriesDone + 1;
  switch (outcome.kind) {
    case "delivered":
      return { kind: "delivered", attempts };
    case "pause":
      return { kind: "paused", attempts };
    case "dead":
      return { kind: "dead_letter", attempts, code: "HUB-0117" };
    case "retry": {
      const scheduled = retryDelaySeconds(retriesDone + 1);
      if (scheduled === null) {
        return { kind: "dead_letter", attempts, code: "HUB-0115" };
      }
      const asked = outcome.retryAfterSeconds;
      const delay = asked !== undefined && Number.isFinite(asked) && asked >= 1 ? Math.min(Math.ceil(asked), MAX_RETRY_AFTER_SECONDS) : scheduled;
      return { kind: "retry", attempts, nextRetryAt: nowSeconds + delay };
    }
  }
}
