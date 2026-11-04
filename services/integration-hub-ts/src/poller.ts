// The next_retry_at poller (FR-35.9, BR-35.7): finds subscriptions with a delivery
// due, and drives the existing Dispatcher over each one's Postgres delivery state.
// The retry schedule, ordering, dead-lettering and the 410 pause are the
// Dispatcher's (nextStep); this file only loads, runs and records.
//
// Several replicas may poll: a subscription is worked by the replica holding its
// lease, which expires so a crashed replica's subscriptions are picked up again.
// A replica sends nothing once its lease is about to lapse, so two replicas never
// send for one subscription at the same time.

import { Dispatcher, type Deliver, type TickResult, type Transport } from "./dispatcher.ts";
import { Metrics } from "./metrics.ts";
import type { HubStore, PgDeliveryStore } from "./pgStore.ts";
import type { RestPusher, RestTarget } from "./restPush.ts";

export interface PollerOptions {
  store: HubStore;
  transport: Transport;
  /** Sends rest-push subscriptions; without it a rest-push subscription is not worked (and is reported as a poller error). */
  rest?: RestPusher;
  /** Unix seconds. */
  clock?: () => number;
  /** How long a replica owns a subscription it is working on. */
  leaseSeconds?: number;
  metrics?: Metrics;
  source?: string;
  onError?: (err: unknown) => void;
}

const MAX_TICKS_PER_PASS = 100;
/** No send starts later than this before the lease ends: the transport's timeout plus slack. */
const LEASE_MARGIN_SECONDS = 15;

/** The lease is about to lapse; the run stops and the rest waits for the next pass. */
export class LeaseEndingError extends Error {
  constructor() {
    super("the poller lease is about to lapse");
    this.name = "LeaseEndingError";
  }
}

/**
 * Wraps the transport so the outcome of every earlier send is stored before the
 * next one goes out, and each send itself is recorded as an attempt. Nothing is
 * written for a send until it returns, so a crash in between leaves the event
 * pending and it is sent again.
 */
function recording(store: PgDeliveryStore, inner: Transport, clock: () => number, leaseUntil: number): Transport {
  return async (url, headers, body) => {
    await store.flush();
    if (clock() + LEASE_MARGIN_SECONDS > leaseUntil) {
      throw new LeaseEndingError();
    }
    const status = await inner(url, headers, body);
    const id = (JSON.parse(body) as { id: string }).id;
    store.recordAttempt(id, status);
    return status;
  };
}

/** The rest-push counterpart of `recording`: flush, lease check, send, then queue the attempt with the receiver's answer. */
function recordingRest(store: PgDeliveryStore, pusher: RestPusher, target: RestTarget, clock: () => number, leaseUntil: number): Deliver {
  return async (delivery) => {
    await store.flush();
    if (clock() + LEASE_MARGIN_SECONDS > leaseUntil) {
      throw new LeaseEndingError();
    }
    const outcome = await pusher.attempt(target, delivery);
    store.recordAttempt(delivery.event.eventId, outcome.status, {
      ...(outcome.excerpt === undefined ? {} : { excerpt: outcome.excerpt }),
      ...(outcome.legacyKey === undefined ? {} : { legacyKey: outcome.legacyKey }),
      ...(outcome.lineKeys === undefined ? {} : { lineKeys: outcome.lineKeys }),
      ...(outcome.lookups === undefined ? {} : { lookups: outcome.lookups }),
    });
    return outcome;
  };
}

export class Poller {
  readonly #o: PollerOptions;
  #timer: NodeJS.Timeout | null = null;
  #running: Promise<void> = Promise.resolve();

  constructor(options: PollerOptions) {
    this.#o = options;
  }

  #now(): number {
    return (this.#o.clock ?? (() => Date.now() / 1000))();
  }

  /** One pass over every subscription with a delivery due. Returns the subscriptions worked. */
  async pollOnce(): Promise<number> {
    const { store } = this.#o;
    let worked = 0;
    for (const due of await store.dueSubscriptions(this.#now())) {
      try {
        if (await this.runSubscription(due.tenantId, due.subscriptionId)) {
          worked += 1;
        }
      } catch (err) {
        this.#o.metrics?.inc("hub_poller_errors_total", "Subscription runs that failed");
        this.#o.onError?.(err);
      }
    }
    return worked;
  }

  /** Works one subscription until nothing more is due. False when another replica holds it. */
  async runSubscription(tenantId: string, subscriptionId: string): Promise<boolean> {
    const { store, metrics } = this.#o;
    const leaseUntil = await store.claim(tenantId, subscriptionId, this.#now(), this.#o.leaseSeconds ?? 120);
    if (leaseUntil === null) {
      return false;
    }
    try {
      const loaded = await store.loadDeliveryStore(tenantId, subscriptionId);
      if (loaded === null) {
        return false;
      }
      const { store: deliveries, target } = loaded;
      let sender: { transport: Transport } | { deliver: Deliver };
      let secret = "";
      let previousSecret: string | null = null;
      if (target.kind === "rest-push") {
        if (this.#o.rest === undefined) {
          throw new Error(`subscription ${target.id} is rest-push but the hub has no migration client configured`);
        }
        sender = { deliver: recordingRest(deliveries, this.#o.rest, store.openRest(tenantId, target), () => this.#now(), leaseUntil) };
      } else {
        secret = store.openSecret(tenantId, target);
        previousSecret = store.openPreviousSecret(tenantId, target, this.#now());
        sender = { transport: recording(deliveries, this.#o.transport, () => this.#now(), leaseUntil) };
      }
      const dispatcher = new Dispatcher({
        subscription: { id: target.id, url: target.url, secret, ...(previousSecret === null ? {} : { previousSecret }) },
        store: deliveries,
        ...sender,
        clock: () => this.#now(),
        ...(this.#o.source === undefined ? {} : { source: this.#o.source }),
      });
      for (let i = 0; i < MAX_TICKS_PER_PASS; i += 1) {
        let result: TickResult;
        try {
          result = await dispatcher.tick();
        } catch (err) {
          if (err instanceof LeaseEndingError) {
            break;
          }
          throw err;
        } finally {
          await deliveries.flush();
        }
        metrics?.inc("hub_deliveries_delivered_total", "Deliveries answered 2xx", {}, result.delivered);
        metrics?.inc("hub_deliveries_retried_total", "Deliveries scheduled for retry", {}, result.retried);
        metrics?.inc("hub_deliveries_dead_lettered_total", "Deliveries dead-lettered (HUB-0115)", {}, result.deadLettered);
        if (result.paused) {
          metrics?.inc("hub_subscriptions_paused_total", "Subscriptions paused by a 410 (HUB-0114)");
        }
        // Go again only when a success may have made a later event of an aggregate deliverable.
        if (result.paused || result.delivered === 0) {
          break;
        }
      }
      return true;
    } finally {
      await store.release(tenantId, subscriptionId, leaseUntil).catch(() => undefined);
    }
  }

  start(intervalMs: number): void {
    this.#timer = setInterval(() => {
      this.#running = this.#running.then(() => this.pollOnce().then(() => undefined)).catch((err) => this.#o.onError?.(err));
    }, intervalMs);
    this.#timer.unref();
  }

  async stop(): Promise<void> {
    if (this.#timer !== null) {
      clearInterval(this.#timer);
      this.#timer = null;
    }
    await this.#running;
  }
}
