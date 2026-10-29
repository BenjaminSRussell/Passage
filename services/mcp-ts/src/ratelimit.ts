/**
 * Per-pod fixed-window rate limiter for the gateway (API rules: RateLimit-*
 * headers and Retry-After). Counters are in process memory: each pod enforces
 * its own limit. A shared Valkey store is the open ADR question, so this is the
 * first increment and the limit per pod is an upper bound per user.
 */

/** Default per-principal budget per window. An assumption until the ADR is settled. */
export const DEFAULT_RATE_LIMIT = 600;
export const DEFAULT_WINDOW_MS = 60_000;

export interface RateDecision {
  allowed: boolean;
  limit: number;
  remaining: number;
  /** Whole seconds until the current window resets. */
  resetSeconds: number;
  /** Set only when the request is refused. */
  retryAfterSeconds?: number;
}

export interface RateLimiterOptions {
  limit?: number;
  windowMs?: number;
  nowMs: () => number;
}

export interface RateLimiter {
  hit(key: string): RateDecision;
}

interface Window {
  startMs: number;
  count: number;
}

export function createRateLimiter(opts: RateLimiterOptions): RateLimiter {
  const limit = opts.limit ?? DEFAULT_RATE_LIMIT;
  const windowMs = opts.windowMs ?? DEFAULT_WINDOW_MS;
  if (!Number.isInteger(limit) || limit < 1) throw new Error("rate limit must be a positive integer");
  if (!Number.isInteger(windowMs) || windowMs < 1000) throw new Error("rate window must be at least 1 s");
  const windows = new Map<string, Window>();

  return {
    hit(key: string): RateDecision {
      const now = opts.nowMs();
      let w = windows.get(key);
      if (!w || now - w.startMs >= windowMs) {
        w = { startMs: now, count: 0 };
        windows.set(key, w);
        // Drop expired windows so the map cannot grow without bound.
        if (windows.size > 10_000) pruneExpired(windows, now, windowMs);
      }
      const resetSeconds = Math.max(1, Math.ceil((w.startMs + windowMs - now) / 1000));
      if (w.count >= limit) {
        return { allowed: false, limit, remaining: 0, resetSeconds, retryAfterSeconds: resetSeconds };
      }
      w.count += 1;
      return { allowed: true, limit, remaining: limit - w.count, resetSeconds };
    },
  };
}

function pruneExpired(windows: Map<string, Window>, now: number, windowMs: number): void {
  for (const [key, w] of windows) {
    if (now - w.startMs >= windowMs) windows.delete(key);
  }
}

/** The RateLimit-* and Retry-After headers for a decision. */
export function rateLimitHeaders(d: RateDecision): Record<string, string> {
  const headers: Record<string, string> = {
    "ratelimit-limit": String(d.limit),
    "ratelimit-remaining": String(d.remaining),
    "ratelimit-reset": String(d.resetSeconds),
  };
  if (d.retryAfterSeconds !== undefined) headers["retry-after"] = String(d.retryAfterSeconds);
  return headers;
}
