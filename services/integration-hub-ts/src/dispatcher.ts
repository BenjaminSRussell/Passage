// Webhook dispatcher (FR-35.9, BR-35.7, BR-35.9, AC-35.3, AC-35.4): delivers a
// subscription's events over HTTPS with the signed CloudEvents body, applies the
// dispatch state machine after every attempt, keeps per-aggregate order,
// dead-letters after the retry schedule, and re-enqueues archived events for a
// replay. Delivery is at-least-once: an event leaves the pending set only after
// the receiver answered 2xx and that outcome was recorded, so a crash or a lost
// response means the event is sent again, never dropped.
//
// The store is an interface; MemoryDeliveryStore backs the tests and a single
// process, PgDeliveryStore (src/pgStore.ts) the running hub.

import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";

import { nextStep, planReplay, restStep, type Step, replayHeaders, REPLAY_MAX_EVENTS, type ReplayCandidate } from "./dispatch.ts";
import { validateWebhookUrl, type GuardOptions, type PinnedTarget } from "./ssrf.ts";
import { deliverableHeads, sign } from "./webhook.ts";

/** An event as archived by the hub: replay metadata plus the CloudEvents type and data. */
export interface HubEvent extends ReplayCandidate {
  type: string;
  data: unknown;
  /** The CloudEvents `legalentityid` extension, when the event carries one. */
  legalEntityId?: string;
  /** The CloudEvents `actor` extension: who caused the event. Not stored; used for echo suppression at enqueue. */
  actor?: string;
}

export interface Subscription {
  id: string;
  url: string;
  secret: string;
}

export interface PendingDelivery {
  event: HubEvent;
  retriesDone: number;
  nextRetryAt: number;
  replay: boolean;
}

export type DeadLetterCode = "HUB-0115" | "HUB-0117" | "HUB-0118";

/** A dead-lettered event: the retry schedule is exhausted, or the receiver refused it for good. */
export interface DeadLetter {
  event: HubEvent;
  attempts: number;
  lastStatus: number | null;
  /**
   * HUB-0115 retries exhausted; HUB-0117 a rest-push receiver or render refused the request for good (4xx);
   * HUB-0118 a rest-push the receiver accepted whose legacy key conflicts with the one already recorded for the
   * event (MIG-XREF-EVENT-CONFLICT): kept for the record, never resent or replayed.
   */
  code: DeadLetterCode;
}

/**
 * What one rest-push attempt came to, classified by src/restPush.ts. `status` is the receiver's (or the
 * render service's) HTTP status, null when nothing answered.
 */
export type RestOutcome = (
  | { kind: "delivered"; status: number }
  | { kind: "retry"; status: number | null; retryAfterSeconds?: number }
  | { kind: "dead"; status: number | null; code?: "HUB-0117" | "HUB-0118" }
  | { kind: "pause"; status: number }
) & {
  /** The receiver's (or the render service's) answer, truncated and with credentials removed; kept on the delivery. */
  excerpt?: string;
  /** The legacy key read from the response at responseKeyPath. */
  legacyKey?: string;
  /** The legacy keys of the elements of the pushed collection, by position (lineKeyPath). */
  lineKeys?: { index: number; legacyKey: string }[];
  /** The lookup table versions the render read, kept with the push for audit. */
  lookups?: { name: string; table: string; version: number }[];
};

/** Renders and pushes one event; resolves with the outcome, never throws for a receiver problem. */
export type Deliver = (delivery: PendingDelivery, nowSeconds: number) => Promise<RestOutcome>;


export interface DeliveryStore {
  pending(subscriptionId: string): PendingDelivery[];
  put(subscriptionId: string, delivery: PendingDelivery): void;
  /** Removes a delivered or dead-lettered event from the pending set. */
  remove(subscriptionId: string, eventId: string): void;
  deadLetter(subscriptionId: string, letter: DeadLetter): void;
  deadLetters(subscriptionId: string): DeadLetter[];
  isPaused(subscriptionId: string): boolean;
  setPaused(subscriptionId: string, paused: boolean): void;
}

export class MemoryDeliveryStore implements DeliveryStore {
  #pending = new Map<string, Map<string, PendingDelivery>>();
  #dead = new Map<string, DeadLetter[]>();
  #paused = new Set<string>();

  pending(subscriptionId: string): PendingDelivery[] {
    return [...(this.#pending.get(subscriptionId)?.values() ?? [])].map((d) => ({ ...d }));
  }
  put(subscriptionId: string, delivery: PendingDelivery): void {
    const map = this.#pending.get(subscriptionId) ?? new Map<string, PendingDelivery>();
    map.set(delivery.event.eventId, { ...delivery });
    this.#pending.set(subscriptionId, map);
  }
  remove(subscriptionId: string, eventId: string): void {
    this.#pending.get(subscriptionId)?.delete(eventId);
  }
  deadLetter(subscriptionId: string, letter: DeadLetter): void {
    this.#dead.set(subscriptionId, [...(this.#dead.get(subscriptionId) ?? []), letter]);
  }
  deadLetters(subscriptionId: string): DeadLetter[] {
    return [...(this.#dead.get(subscriptionId) ?? [])];
  }
  isPaused(subscriptionId: string): boolean {
    return this.#paused.has(subscriptionId);
  }
  setPaused(subscriptionId: string, paused: boolean): void {
    if (paused) {
      this.#paused.add(subscriptionId);
    } else {
      this.#paused.delete(subscriptionId);
    }
  }
}

/** Sends one request and returns the receiver's status, or null when no response arrived. */
export type Transport = (url: string, headers: Record<string, string>, body: string) => Promise<number | null>;

export interface HttpTransportOptions extends GuardOptions {
  timeoutMs?: number;
  validate?: (url: string) => Promise<PinnedTarget>;
}

/**
 * Validates the URL on every attempt, connects to the validated address only
 * (DNS cannot change it between check and connect; SNI and Host keep the
 * hostname) and never follows a redirect: a 3xx status is returned as it is and
 * the state machine retries it as a failed delivery. A rejected URL, a network
 * error or a timeout is "no response".
 */
export function httpTransport(options: HttpTransportOptions = {}): Transport {
  const { timeoutMs = 10_000, validate = (url: string) => validateWebhookUrl(url, options) } = options;
  return async (url, headers, body) => {
    const signal = AbortSignal.timeout(timeoutMs);
    let target: PinnedTarget;
    try {
      target = await Promise.race([
        validate(url),
        new Promise<never>((_, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true })),
      ]);
    } catch {
      return null;
    }
    const send = target.url.protocol === "https:" ? httpsRequest : httpRequest;
    return new Promise<number | null>((resolve) => {
      const req = send(
        target.url,
        {
          method: "POST",
          headers: { ...headers, "content-length": String(Buffer.byteLength(body)) },
          signal,
          lookup: (_host, lookupOptions, callback) => {
            if (lookupOptions.all) {
              (callback as unknown as (e: null, a: { address: string; family: number }[]) => void)(null, [
                { address: target.address, family: target.family },
              ]);
            } else {
              callback(null, target.address, target.family);
            }
          },
        },
        (res) => {
          resolve(res.statusCode ?? null);
          res.destroy();
        },
      );
      req.on("error", () => resolve(null));
      req.end(body);
    });
  };
}

export interface DispatcherOptions {
  subscription: Subscription;
  store: DeliveryStore;
  /** Webhook sender. Exactly one of transport and deliver is set. */
  transport?: Transport;
  /** Rest-push sender (render, push, record the legacy key). */
  deliver?: Deliver;
  /** Unix seconds. */
  clock: () => number;
  source?: string;
}

export interface TickResult {
  attempted: number;
  delivered: number;
  retried: number;
  deadLettered: number;
  paused: boolean;
}

export class Dispatcher {
  readonly #o: DispatcherOptions;

  constructor(options: DispatcherOptions) {
    this.#o = options;
  }

  /** Adds an event to the subscription's pending deliveries, due now. An event already pending is kept as it is. */
  enqueue(event: HubEvent, replay = false): void {
    const { store, subscription, clock } = this.#o;
    if (store.pending(subscription.id).some((d) => d.event.eventId === event.eventId)) {
      return;
    }
    store.put(subscription.id, { event, retriesDone: 0, nextRetryAt: clock(), replay });
  }

  /**
   * Replays the archive from `fromSeconds` (BR-35.9): every page of planReplay
   * is enqueued as a replayed delivery, so receivers see ce-passagereplay: true.
   * Returns the number of events enqueued.
   */
  replay(archive: readonly HubEvent[], fromSeconds: number, cap: number = REPLAY_MAX_EVENTS): number {
    const byId = new Map(archive.map((e) => [e.eventId, e]));
    let plan = planReplay(archive, fromSeconds, cap);
    let count = 0;
    for (;;) {
      for (const candidate of plan.events) {
        this.enqueue(byId.get(candidate.eventId) as HubEvent, true);
        count += 1;
      }
      if (plan.next === null) {
        return count;
      }
      plan = planReplay(archive, plan.next.fromSeconds, cap, plan.next.skip);
    }
  }

  /** Clears a 410 pause (HUB-0114) once the receiver is back. */
  resume(): void {
    this.#o.store.setPaused(this.#o.subscription.id, false);
  }

  /** The earliest time an eligible head is due, or null when nothing is pending or the subscription is paused. */
  nextDueAt(): number | null {
    const { store, subscription } = this.#o;
    if (store.isPaused(subscription.id)) {
      return null;
    }
    const pending = store.pending(subscription.id);
    const heads = new Set(deliverableHeads(pending.map((d) => d.event)).map((e) => e.eventId));
    const due = pending.filter((d) => heads.has(d.event.eventId)).map((d) => d.nextRetryAt);
    return due.length === 0 ? null : Math.min(...due);
  }

  /**
   * One dispatch pass: each aggregate's lowest pending sequence is attempted when
   * due; later events of that aggregate wait for it (BR-35.7).
   */
  async tick(): Promise<TickResult> {
    const { store, subscription, transport, deliver, clock } = this.#o;
    const result: TickResult = { attempted: 0, delivered: 0, retried: 0, deadLettered: 0, paused: false };
    if (store.isPaused(subscription.id)) {
      result.paused = true;
      return result;
    }
    const pending = store.pending(subscription.id);
    const byId = new Map(pending.map((d) => [d.event.eventId, d]));
    for (const head of deliverableHeads(pending.map((d) => d.event))) {
      const delivery = byId.get(head.eventId) as PendingDelivery;
      const now = clock();
      if (delivery.nextRetryAt > now) {
        continue;
      }
      let status: number | null;
      let step: Step;
      if (deliver !== undefined) {
        const outcome = await deliver(delivery, now);
        status = outcome.status;
        step = restStep(delivery.retriesDone, outcome, clock());
      } else {
        const { body, headers } = this.#request(delivery, now);
        status = await (transport as Transport)(subscription.url, headers, body);
        step = nextStep(delivery.retriesDone, status, clock());
      }
      result.attempted += 1;
      switch (step.kind) {
        case "delivered":
          store.remove(subscription.id, head.eventId);
          result.delivered += 1;
          break;
        case "retry":
          store.put(subscription.id, { ...delivery, retriesDone: delivery.retriesDone + 1, nextRetryAt: step.nextRetryAt });
          result.retried += 1;
          break;
        case "dead_letter":
          store.deadLetter(subscription.id, { event: delivery.event, attempts: step.attempts, lastStatus: status, code: step.code ?? "HUB-0115" });
          store.remove(subscription.id, head.eventId);
          result.deadLettered += 1;
          break;
        case "paused":
          store.setPaused(subscription.id, true);
          result.paused = true;
          return result;
      }
    }
    return result;
  }

  #request(delivery: PendingDelivery, now: number): { body: string; headers: Record<string, string> } {
    const e = delivery.event;
    const body = JSON.stringify({
      specversion: "1.0",
      id: e.eventId,
      source: this.#o.source ?? "/passage/integration-hub",
      type: e.type,
      time: new Date(e.occurredAt * 1000).toISOString(),
      subject: e.aggregateId,
      passagesequence: e.sequence,
      data: e.data,
    });
    // At-least-once: a retry, a resend after a crash and a replay all carry the
    // same Idempotency-Key (the CloudEvent id), so a receiver applies the event once.
    const headers: Record<string, string> = {
      "content-type": "application/cloudevents+json",
      "idempotency-key": e.eventId,
      "passage-signature": sign(this.#o.subscription.secret, now, body),
      ...(delivery.replay ? replayHeaders() : {}),
    };
    return { body, headers };
  }
}
