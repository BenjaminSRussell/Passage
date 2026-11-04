// Webhook delivery policy for the integration hub (FR-35.9, BR-35.7). Everything
// here is pure: no I/O, no clock reads. The dispatcher supplies the time.

import { createHmac, timingSafeEqual } from "node:crypto";

/** Retry delays in seconds after attempts 1..8: 10 s, 1 m, 5 m, 30 m, 2 h, 6 h, 12 h, 24 h. */
export const RETRY_SCHEDULE_SECONDS: readonly number[] = [10, 60, 300, 1800, 7200, 21600, 43200, 86400];

/** A receiver must verify the signature within this many seconds of `t`. */
export const SIGNATURE_TOLERANCE_SECONDS = 300;

export type DeliveryDecision = { kind: "delivered" } | { kind: "pause" } | { kind: "retry" };

/**
 * Signs `t.body` with HMAC-SHA256 and returns the Passage-Signature header value. During a secret
 * rotation's grace period pass the previous secret too: the header then carries one v1 per secret
 * (`t=..,v1=<new>,v1=<old>`) and a receiver holding either secret verifies it.
 */
export function sign(secret: string, unixSeconds: number, body: string, previousSecret?: string): string {
  const secrets = previousSecret === undefined ? [secret] : [secret, previousSecret];
  return `t=${unixSeconds},${secrets.map((s) => `v1=${digest(s, unixSeconds, body).toString("hex")}`).join(",")}`;
}

/** Verifies a Passage-Signature header at `nowSeconds`. Stale or malformed headers fail. */
export function verify(header: string, secret: string, body: string, nowSeconds: number): boolean {
  const parsed = parseHeader(header);
  if (parsed === null) {
    return false;
  }
  if (Math.abs(nowSeconds - parsed.t) > SIGNATURE_TOLERANCE_SECONDS) {
    return false;
  }
  const expected = digest(secret, parsed.t, body);
  // Any v1 may match (a rotation's grace period signs with both secrets); every one is compared, none short-circuits.
  let ok = false;
  for (const v1 of parsed.v1) {
    const given = Buffer.from(v1, "hex");
    if (given.length === expected.length && timingSafeEqual(given, expected)) {
      ok = true;
    }
  }
  return ok;
}

/**
 * Delay before retry number `retry` (1-based), or null once the schedule is
 * exhausted and the event must be dead-lettered (HUB-0115).
 */
export function retryDelaySeconds(retry: number): number | null {
  if (!Number.isInteger(retry) || retry < 1 || retry > RETRY_SCHEDULE_SECONDS.length) {
    return null;
  }
  return RETRY_SCHEDULE_SECONDS[retry - 1];
}

/** Maps an HTTP status from the receiver to the next step. 2xx delivers, 410 pauses, everything else retries. */
export function decide(status: number): DeliveryDecision {
  if (status >= 200 && status <= 299) {
    return { kind: "delivered" };
  }
  if (status === 410) {
    return { kind: "pause" };
  }
  return { kind: "retry" };
}

export interface PendingEvent {
  eventId: string;
  aggregateId: string;
  sequence: number;
}

/**
 * The events that may be delivered now under per-aggregate ordering (BR-35.7):
 * for each aggregate only its lowest-sequence pending event is eligible, so a
 * failing event blocks later events of the same aggregate and nothing else.
 */
export function deliverableHeads(pending: readonly PendingEvent[]): PendingEvent[] {
  const heads = new Map<string, PendingEvent>();
  for (const ev of pending) {
    const current = heads.get(ev.aggregateId);
    if (current === undefined || ev.sequence < current.sequence) {
      heads.set(ev.aggregateId, ev);
    }
  }
  return [...heads.values()].sort((a, b) => a.sequence - b.sequence);
}

function digest(secret: string, unixSeconds: number, body: string): Buffer {
  return createHmac("sha256", secret).update(`${unixSeconds}.${body}`).digest();
}

function parseHeader(header: string): { t: number; v1: string[] } | null {
  let t: number | null = null;
  const v1: string[] = [];
  for (const part of header.split(",")) {
    const eq = part.indexOf("=");
    if (eq < 0) {
      continue;
    }
    const key = part.slice(0, eq).trim();
    const value = part.slice(eq + 1).trim();
    if (key === "t" && /^\d+$/.test(value)) {
      t = Number(value);
    } else if (key === "v1") {
      v1.push(value);
    }
  }
  return t === null || v1.length === 0 ? null : { t, v1 };
}
