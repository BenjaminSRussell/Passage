import { describe, expect, it } from "vitest";
import { createRateLimiter, rateLimitHeaders } from "../src/ratelimit.js";

function clock(start = 0) {
  let now = start;
  return { nowMs: () => now, advance: (ms: number) => (now += ms) };
}

describe("createRateLimiter (per-pod fixed window)", () => {
  it("allows up to the limit then refuses with Retry-After until the window resets", () => {
    const c = clock(0);
    const rl = createRateLimiter({ limit: 2, windowMs: 60_000, nowMs: c.nowMs });
    expect(rl.hit("u")).toMatchObject({ allowed: true, remaining: 1, limit: 2 });
    expect(rl.hit("u")).toMatchObject({ allowed: true, remaining: 0 });
    const refused = rl.hit("u");
    expect(refused).toMatchObject({ allowed: false, remaining: 0, retryAfterSeconds: 60, resetSeconds: 60 });
    c.advance(30_000);
    expect(rl.hit("u")).toMatchObject({ allowed: false, resetSeconds: 30, retryAfterSeconds: 30 });
  });

  it("opens a fresh window after the reset", () => {
    const c = clock(0);
    const rl = createRateLimiter({ limit: 1, windowMs: 60_000, nowMs: c.nowMs });
    rl.hit("u");
    expect(rl.hit("u").allowed).toBe(false);
    c.advance(60_000);
    expect(rl.hit("u")).toMatchObject({ allowed: true, remaining: 0 });
  });

  it("counts each key separately", () => {
    const rl = createRateLimiter({ limit: 1, nowMs: () => 0 });
    expect(rl.hit("tenant-a:user-1").allowed).toBe(true);
    expect(rl.hit("tenant-a:user-2").allowed).toBe(true);
    expect(rl.hit("tenant-a:user-1").allowed).toBe(false);
  });

  it("rejects an invalid limit or window at construction", () => {
    expect(() => createRateLimiter({ limit: 0, nowMs: () => 0 })).toThrow();
    expect(() => createRateLimiter({ windowMs: 10, nowMs: () => 0 })).toThrow();
  });

  it("property: never allows more than the limit within one window", () => {
    for (let limit = 1; limit <= 6; limit++) {
      const c = clock(0);
      const rl = createRateLimiter({ limit, windowMs: 60_000, nowMs: c.nowMs });
      let allowed = 0;
      for (let i = 0; i < 50; i++) {
        if (rl.hit("k").allowed) allowed += 1;
        c.advance(7);
      }
      // 50 hits spaced 7 ms apart span 343 ms, well inside one window.
      expect(allowed).toBe(limit);
    }
  });
});

describe("rateLimitHeaders", () => {
  it("emits RateLimit-* on allowed responses and Retry-After only when refused", () => {
    expect(rateLimitHeaders({ allowed: true, limit: 10, remaining: 9, resetSeconds: 42 })).toEqual({
      "ratelimit-limit": "10",
      "ratelimit-remaining": "9",
      "ratelimit-reset": "42",
    });
    expect(rateLimitHeaders({ allowed: false, limit: 10, remaining: 0, resetSeconds: 5, retryAfterSeconds: 5 })["retry-after"]).toBe("5");
  });
});
