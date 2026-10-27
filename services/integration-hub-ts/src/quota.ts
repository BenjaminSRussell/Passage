// Per-client token bucket for the public APIs (FR-35.8, BR-35.8, HUB-0113).
// Pure: the request middleware reads the clock, stores the returned state and
// answers 429 with Retry-After when `allowed` is false. Gateway policy (Envoy)
// enforces the same numbers at the edge; this module is the reference for them.

/** Parameter range for a client's rate per minute (§6.35.13). */
export const QUOTA_RATE_MIN = 10;
export const QUOTA_RATE_MAX = 100_000;

/** Parameter range for the burst size (§6.35.13). */
export const QUOTA_BURST_MIN = 1;
export const QUOTA_BURST_MAX = 100_000;

/** Defaults from FR-35.8: 600 requests per minute, burst 100. */
export const DEFAULT_QUOTA_PER_MIN = 600;
export const DEFAULT_BURST = 100;

/** BR-35.8: GraphQL requests cost one unit per 100 complexity points. */
export const GRAPHQL_POINTS_PER_UNIT = 100;

export interface BucketConfig {
  ratePerMinute: number;
  burst: number;
}

/** Tokens are fractional so that slow refill accumulates across requests. */
export interface BucketState {
  tokens: number;
  updatedAtMs: number;
}

export type TakeResult =
  | { allowed: true; state: BucketState; remaining: number }
  | { allowed: false; state: BucketState; retryAfterSeconds: number };

/** Throws RangeError when the rate or burst is outside the parameter ranges. */
export function validateBucket(cfg: BucketConfig): void {
  if (!Number.isInteger(cfg.ratePerMinute) || cfg.ratePerMinute < QUOTA_RATE_MIN || cfg.ratePerMinute > QUOTA_RATE_MAX) {
    throw new RangeError(`ratePerMinute must be an integer in ${QUOTA_RATE_MIN}..${QUOTA_RATE_MAX}, got ${cfg.ratePerMinute}`);
  }
  if (!Number.isInteger(cfg.burst) || cfg.burst < QUOTA_BURST_MIN || cfg.burst > QUOTA_BURST_MAX) {
    throw new RangeError(`burst must be an integer in ${QUOTA_BURST_MIN}..${QUOTA_BURST_MAX}, got ${cfg.burst}`);
  }
}

/** A new client starts with a full bucket. */
export function initialState(cfg: BucketConfig, nowMs: number): BucketState {
  validateBucket(cfg);
  return { tokens: cfg.burst, updatedAtMs: nowMs };
}

/** True when a single request of this cost can never be admitted by the bucket. */
export function exceedsBurst(cfg: BucketConfig, cost: number): boolean {
  return cost > cfg.burst;
}

/**
 * Takes `cost` tokens at time `nowMs`. Refill is continuous at ratePerMinute and
 * capped at burst. A clock that moves backwards refills nothing and does not
 * rewind the state's clock. Throws RangeError
 * for a non-positive cost; callers reject costs above burst first (see
 * exceedsBurst), because such a request could never succeed.
 */
export function take(cfg: BucketConfig, state: BucketState, nowMs: number, cost: number = 1): TakeResult {
  validateBucket(cfg);
  if (!Number.isInteger(cost) || cost < 1) {
    throw new RangeError(`cost must be a positive integer, got ${cost}`);
  }
  const perMs = cfg.ratePerMinute / 60_000;
  const elapsed = Math.max(0, nowMs - state.updatedAtMs);
  const tokens = Math.min(cfg.burst, state.tokens + elapsed * perMs);
  // The refill clock never moves backwards: a request stamped earlier than the
  // last one must not let the next request refill the same interval twice.
  const updatedAtMs = Math.max(state.updatedAtMs, nowMs);
  if (tokens >= cost) {
    const left = tokens - cost;
    return { allowed: true, state: { tokens: left, updatedAtMs }, remaining: Math.floor(left) };
  }
  const waitMs = (cost - tokens) / perMs;
  return {
    allowed: false,
    state: { tokens, updatedAtMs },
    retryAfterSeconds: Math.max(1, Math.ceil(waitMs / 1000)),
  };
}

/** BR-35.8: one unit per started 100 GraphQL complexity points, minimum one. */
export function graphqlCost(complexityPoints: number): number {
  if (!Number.isFinite(complexityPoints) || complexityPoints < 0) {
    throw new RangeError(`complexity points must be a non-negative number, got ${complexityPoints}`);
  }
  return Math.max(1, Math.ceil(complexityPoints / GRAPHQL_POINTS_PER_UNIT));
}
