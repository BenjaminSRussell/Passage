import { test } from "node:test";
import assert from "node:assert/strict";

import {
  deliveryOrder,
  nextStep,
  planReplay,
  replayHeaders,
  REPLAY_MAX_EVENTS,
  type ReplayCandidate,
} from "../src/dispatch.ts";

const now = 1_800_000_000;

test("AC-35.3: a receiver that fails three times then succeeds gets exactly one delivery after three retries", () => {
  let retries = 0;
  const statuses = [503, 500, 502, 200];
  const seen: string[] = [];
  const delays: number[] = [];
  for (const status of statuses) {
    const step = nextStep(retries, status, now);
    seen.push(step.kind);
    if (step.kind === "retry") {
      delays.push(step.nextRetryAt - now);
      retries += 1;
    } else {
      assert.equal(step.kind, "delivered");
      assert.equal(step.attempts, 4);
    }
  }
  assert.deepEqual(seen, ["retry", "retry", "retry", "delivered"]);
  assert.deepEqual(delays, [10, 60, 300]);
});

test("a 2xx on the first attempt delivers at attempt 1", () => {
  assert.deepEqual(nextStep(0, 204, now), { kind: "delivered", attempts: 1 });
});

test("410 Gone pauses the subscription without further retries (HUB-0114)", () => {
  assert.deepEqual(nextStep(0, 410, now), { kind: "paused", attempts: 1 });
  assert.deepEqual(nextStep(5, 410, now), { kind: "paused", attempts: 6 });
});

test("no HTTP response is retried, never treated as delivered", () => {
  const step = nextStep(2, null, now);
  assert.equal(step.kind, "retry");
  assert.equal(step.kind === "retry" && step.nextRetryAt - now, 300);
});

test("the eighth retry is the last: after it the event is dead-lettered (HUB-0115)", () => {
  const last = nextStep(7, 500, now);
  assert.equal(last.kind, "retry");
  assert.equal(last.kind === "retry" && last.nextRetryAt - now, 86400);
  assert.deepEqual(nextStep(8, 500, now), { kind: "dead_letter", attempts: 9 });
});

test("a negative or fractional retry count is refused", () => {
  assert.throws(() => nextStep(-1, 500, now), RangeError);
  assert.throws(() => nextStep(1.5, 500, now), RangeError);
});

const events: ReplayCandidate[] = [
  { eventId: "e1", occurredAt: 100, aggregateId: "A", sequence: 1 },
  { eventId: "e2", occurredAt: 200, aggregateId: "A", sequence: 2 },
  { eventId: "e3", occurredAt: 200, aggregateId: "B", sequence: 1 },
  { eventId: "e4", occurredAt: 300, aggregateId: "A", sequence: 3 },
  { eventId: "e0", occurredAt: 50, aggregateId: "C", sequence: 1 },
];

test("replay from T includes events at or after T in time order, then per-aggregate sequence", () => {
  const plan = planReplay(events, 100);
  assert.deepEqual(
    plan.events.map((e) => e.eventId),
    ["e1", "e2", "e3", "e4"],
  );
  assert.equal(plan.truncated, false);
  assert.equal(plan.next, null);
});

test("an aggregate whose producer clock ran behind is still replayed in sequence order", () => {
  // A's sequence 2 is stamped before its sequence 1; B sits between them in time.
  const skewed: ReplayCandidate[] = [
    { eventId: "a2", occurredAt: 100, aggregateId: "A", sequence: 2 },
    { eventId: "b1", occurredAt: 150, aggregateId: "B", sequence: 1 },
    { eventId: "a1", occurredAt: 200, aggregateId: "A", sequence: 1 },
  ];
  assert.deepEqual(
    deliveryOrder(skewed).map((e) => e.eventId),
    ["a1", "b1", "a2"],
  );
});

test("delivery order is the same whatever order the archive returns events in", () => {
  const forward = deliveryOrder(events).map((e) => e.eventId);
  assert.deepEqual(deliveryOrder([...events].reverse()).map((e) => e.eventId), forward);
});

test("paging a capped replay delivers every event exactly once, even when more than the cap share a timestamp", () => {
  const sameSecond: ReplayCandidate[] = Array.from({ length: 7 }, (_, i) => ({
    eventId: `s${i}`,
    occurredAt: 500,
    aggregateId: `agg-${i % 3}`,
    sequence: Math.floor(i / 3) + 1,
  }));
  const all = [...events, ...sameSecond];
  const seen: string[] = [];
  let plan = planReplay(all, 0, 3);
  let pages = 1;
  seen.push(...plan.events.map((e) => e.eventId));
  while (plan.next !== null) {
    plan = planReplay(all, plan.next.fromSeconds, 3, plan.next.skip);
    seen.push(...plan.events.map((e) => e.eventId));
    pages += 1;
    assert.ok(pages <= 10, "replay paging must terminate");
  }
  assert.equal(pages, 4);
  assert.deepEqual([...seen].sort(), all.map((e) => e.eventId).sort());
  assert.deepEqual(seen, deliveryOrder(all).map((e) => e.eventId));
});

test("a capped replay reports truncation and the cursor to continue from", () => {
  const plan = planReplay(events, 0, 2);
  assert.deepEqual(plan.events.map((e) => e.eventId), ["e0", "e1"]);
  assert.equal(plan.truncated, true);
  assert.deepEqual(plan.next, { fromSeconds: 0, skip: 2 });
});

test("the replay cap must be an integer between 1 and 10,000,000", () => {
  assert.equal(REPLAY_MAX_EVENTS, 1_000_000);
  assert.throws(() => planReplay(events, 0, 0), RangeError);
  assert.throws(() => planReplay(events, 0, 10_000_001), RangeError);
  assert.throws(() => planReplay(events, 0, 2.5), RangeError);
  assert.throws(() => planReplay(events, 0, 2, -1), RangeError);
});

test("replayed deliveries carry ce-passagereplay: true", () => {
  assert.deepEqual(replayHeaders(), { "ce-passagereplay": "true" });
});
