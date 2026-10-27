import { test } from "node:test";
import assert from "node:assert/strict";

import {
  exceedsBurst,
  graphqlCost,
  initialState,
  take,
  type BucketConfig,
  type BucketState,
} from "../src/quota.ts";

const quota: BucketConfig = { ratePerMinute: 600, burst: 100 };
const t0 = 1_800_000_000_000;

test("AC-35.6: 700 requests in one instant against quota 600/min burst 100 get at least 100 HUB-0113 responses", () => {
  let state: BucketState = initialState(quota, t0);
  let allowed = 0;
  let denied = 0;
  for (let i = 0; i < 700; i += 1) {
    const r = take(quota, state, t0);
    state = r.state;
    if (r.allowed) {
      allowed += 1;
    } else {
      denied += 1;
      assert.ok(r.retryAfterSeconds >= 1, "Retry-After is at least one second");
    }
  }
  assert.equal(allowed, 100);
  assert.equal(denied, 600);
  assert.ok(denied >= 100);
});

test("refill is continuous at the configured rate and capped at burst", () => {
  let state = initialState(quota, t0);
  for (let i = 0; i < 100; i += 1) {
    assert.equal(take(quota, state, t0).allowed, true);
    state = take(quota, state, t0).state;
  }
  // Empty now. Six seconds at 600/min refills 60 tokens.
  const after = t0 + 6_000;
  let granted = 0;
  let s = state;
  for (;;) {
    const r = take(quota, s, after);
    s = r.state;
    if (!r.allowed) break;
    granted += 1;
  }
  assert.equal(granted, 60);

  // An idle hour never accumulates past burst.
  const idle = take(quota, initialState(quota, t0), t0 + 3_600_000);
  assert.equal(idle.allowed, true);
  assert.equal(idle.allowed && idle.remaining, 99);
});

test("a denied request reports the seconds until one token is available", () => {
  const drained: BucketState = { tokens: 0, updatedAtMs: t0 };
  const r = take({ ratePerMinute: 60, burst: 10 }, drained, t0);
  assert.equal(r.allowed, false);
  // One token at 60/min takes one second.
  assert.equal(!r.allowed && r.retryAfterSeconds, 1);
});

test("a clock that moves backwards refills nothing, now or on the next request", () => {
  const state: BucketState = { tokens: 0, updatedAtMs: t0 };
  const r = take(quota, state, t0 - 60_000);
  assert.equal(r.allowed, false);
  assert.equal(r.state.updatedAtMs, t0);
  // Back at t0 nothing has elapsed since the last refill, so still nothing to take.
  assert.equal(take(quota, r.state, t0).allowed, false);
});

test("parameter ranges are enforced", () => {
  const cases: Array<[string, () => unknown]> = [
    ["rate below 10", () => take({ ratePerMinute: 9, burst: 1 }, initialState({ ratePerMinute: 10, burst: 1 }, t0), t0)],
    ["rate above 100000", () => initialState({ ratePerMinute: 100_001, burst: 1 }, t0)],
    ["burst zero", () => initialState({ ratePerMinute: 600, burst: 0 }, t0)],
    ["rate fractional", () => initialState({ ratePerMinute: 600.5, burst: 1 }, t0)],
    ["cost zero", () => take(quota, initialState(quota, t0), t0, 0)],
    ["cost fractional", () => take(quota, initialState(quota, t0), t0, 1.5)],
  ];
  for (const [name, fn] of cases) {
    assert.throws(fn, RangeError, name);
  }
});

test("oversize costs are reported so callers can reject before taking tokens", () => {
  assert.equal(exceedsBurst(quota, 100), false);
  assert.equal(exceedsBurst(quota, 101), true);
});

test("BR-35.8: GraphQL cost is one unit per started 100 complexity points, minimum one", () => {
  const cases: Array<[number, number]> = [
    [0, 1],
    [1, 1],
    [100, 1],
    [101, 2],
    [1000, 10],
    [1001, 11],
  ];
  for (const [points, want] of cases) {
    assert.equal(graphqlCost(points), want, `points ${points}`);
  }
  assert.throws(() => graphqlCost(-1), RangeError);
  assert.throws(() => graphqlCost(Number.NaN), RangeError);
});

test("property: admitted tokens never exceed burst plus refill over the elapsed time", () => {
  // Deterministic linear congruential generator so failures reproduce.
  let seed = 20261021;
  const next = (): number => {
    seed = (seed * 1_103_515_245 + 12_345) % 2_147_483_648;
    return seed / 2_147_483_648;
  };
  for (let run = 0; run < 50; run += 1) {
    const cfg: BucketConfig = {
      ratePerMinute: 10 + Math.floor(next() * 1000),
      burst: 1 + Math.floor(next() * 50),
    };
    let state = initialState(cfg, t0);
    let now = t0;
    let admitted = 0;
    for (let step = 0; step < 400; step += 1) {
      now += Math.floor(next() * 2000);
      const cost = 1 + Math.floor(next() * Math.min(5, cfg.burst));
      const r = take(cfg, state, now, cost);
      state = r.state;
      if (r.allowed) {
        admitted += cost;
      }
      const ceiling = cfg.burst + (cfg.ratePerMinute / 60_000) * (now - t0);
      assert.ok(admitted <= ceiling + 1e-9, `run ${run} step ${step}: admitted ${admitted} > ceiling ${ceiling}`);
    }
  }
});
