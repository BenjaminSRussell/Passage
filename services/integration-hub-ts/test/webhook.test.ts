import { test } from "vitest";
import assert from "node:assert/strict";

import {
  decide,
  deliverableHeads,
  retryDelaySeconds,
  sign,
  verify,
  RETRY_SCHEDULE_SECONDS,
} from "../src/webhook.ts";

const secret = "whsec_test_only";
const body = '{"type":"dev.passage.edi.message.received.v1"}';
const now = 1_800_000_000;

test("a signature made now verifies and a wrong secret or body does not", () => {
  const header = sign(secret, now, body);
  assert.equal(verify(header, secret, body, now), true);
  assert.equal(verify(header, "other", body, now), false);
  assert.equal(verify(header, secret, body + " ", now), false);
});

test("a signature outside the 5 minute tolerance is rejected", () => {
  const header = sign(secret, now, body);
  assert.equal(verify(header, secret, body, now + 300), true);
  assert.equal(verify(header, secret, body, now + 301), false);
  assert.equal(verify(header, secret, body, now - 301), false);
});

test("malformed signature headers are rejected", () => {
  assert.equal(verify("", secret, body, now), false);
  assert.equal(verify("t=abc,v1=00", secret, body, now), false);
  assert.equal(verify(`t=${now}`, secret, body, now), false);
  assert.equal(verify(`t=${now},v1=zz`, secret, body, now), false);
});

test("retry schedule follows BR-35.7 and dead-letters after eight retries", () => {
  assert.deepEqual(RETRY_SCHEDULE_SECONDS, [10, 60, 300, 1800, 7200, 21600, 43200, 86400]);
  assert.equal(retryDelaySeconds(1), 10);
  assert.equal(retryDelaySeconds(8), 86400);
  assert.equal(retryDelaySeconds(9), null);
  assert.equal(retryDelaySeconds(0), null);
  assert.equal(retryDelaySeconds(1.5), null);
});

test("2xx delivers, 410 pauses the subscription, other statuses retry", () => {
  assert.deepEqual(decide(200), { kind: "delivered" });
  assert.deepEqual(decide(204), { kind: "delivered" });
  assert.deepEqual(decide(410), { kind: "pause" });
  assert.deepEqual(decide(500), { kind: "retry" });
  assert.deepEqual(decide(429), { kind: "retry" });
});

test("per-aggregate ordering: only the lowest pending sequence per aggregate is eligible", () => {
  const heads = deliverableHeads([
    { eventId: "e3", aggregateId: "A", sequence: 3 },
    { eventId: "e1", aggregateId: "A", sequence: 1 },
    { eventId: "e2", aggregateId: "B", sequence: 2 },
    { eventId: "e4", aggregateId: "B", sequence: 4 },
  ]);
  assert.deepEqual(
    heads.map((h) => h.eventId),
    ["e1", "e2"],
  );
});

test("a failing head blocks only its own aggregate", () => {
  const pending = [
    { eventId: "a1", aggregateId: "A", sequence: 1 },
    { eventId: "a2", aggregateId: "A", sequence: 2 },
    { eventId: "b1", aggregateId: "B", sequence: 1 },
  ];
  // Event a1 keeps failing, so the head list still offers a1 and never a2.
  const eligible = deliverableHeads(pending).map((h) => h.eventId);
  assert.equal(eligible.includes("a2"), false);
  assert.equal(eligible.includes("b1"), true);
});
