// Background upkeep that works across tenants (FR-35.8, FR-35.9): re-sealing stored
// secrets under the current master key after a key rotation, removing a rotated
// signing secret once its grace period is over, and purging old per-minute client
// usage. Each pass is bounded and idempotent, so several replicas may run it.

import type { Metrics } from "./metrics.ts";
import type { HubStore } from "./pgStore.ts";

/** Per-minute usage rows are kept this long unless HUB_USAGE_RETENTION_DAYS says otherwise. */
export const DEFAULT_USAGE_RETENTION_DAYS = 90;
/** The usage API reads up to 1,440 minutes; a shorter retention could not serve it. */
export const MIN_USAGE_RETENTION_DAYS = 2;
const MAX_USAGE_RETENTION_DAYS = 3650;
const BATCH = 500;
const MAX_ROUNDS = 20;
const DAY_MS = 86_400_000;

/** HUB_USAGE_RETENTION_DAYS: a whole number of days in 2..3650; anything else stops the start rather than silently keeping or dropping data. */
export function usageRetentionDaysFromEnv(env: NodeJS.ProcessEnv = process.env): number {
  const raw = (env["HUB_USAGE_RETENTION_DAYS"] ?? "").trim();
  if (raw === "") {
    return DEFAULT_USAGE_RETENTION_DAYS;
  }
  const n = Number(raw);
  if (!/^\d+$/.test(raw) || n < MIN_USAGE_RETENTION_DAYS || n > MAX_USAGE_RETENTION_DAYS) {
    throw new Error(`HUB_USAGE_RETENTION_DAYS must be a whole number of days from ${MIN_USAGE_RETENTION_DAYS} to ${MAX_USAGE_RETENTION_DAYS}`);
  }
  return n;
}

export interface MaintenanceOptions {
  store: HubStore;
  metrics?: Metrics;
  usageRetentionDays?: number;
  clock?: () => Date;
  onError?: (err: unknown) => void;
}

export interface MaintenanceResult {
  resealed: number;
  expiredSecrets: number;
  resealFailed: number;
  usagePurged: number;
}

export class Maintenance {
  readonly #o: MaintenanceOptions;
  #timer: NodeJS.Timeout | null = null;
  #running: Promise<unknown> | null = null;

  constructor(o: MaintenanceOptions) {
    this.#o = o;
  }

  /** One full pass: re-seal (when keys are configured), then purge usage. */
  async runOnce(): Promise<MaintenanceResult> {
    const { store, metrics } = this.#o;
    const now = (this.#o.clock ?? (() => new Date()))();
    const out: MaintenanceResult = { resealed: 0, expiredSecrets: 0, resealFailed: 0, usagePurged: 0 };
    if (store.canSeal) {
      for (let round = 0; round < MAX_ROUNDS; round += 1) {
        const r = await store.resealDue(BATCH, now);
        out.resealed += r.resealed;
        out.expiredSecrets += r.expired;
        out.resealFailed += r.failed;
        // A value that cannot be opened is found again by the next query: stop rather than spin on it.
        if (r.resealed + r.expired === 0 || r.failed > 0) {
          break;
        }
      }
    }
    const cutoff = new Date(now.getTime() - (this.#o.usageRetentionDays ?? DEFAULT_USAGE_RETENTION_DAYS) * DAY_MS);
    for (let round = 0; round < MAX_ROUNDS; round += 1) {
      const n = await store.purgeClientUsage(cutoff, BATCH);
      out.usagePurged += n;
      if (n < BATCH) {
        break;
      }
    }
    metrics?.inc("hub_secrets_resealed_total", "Subscriptions whose sealed values were re-sealed under the current key", {}, out.resealed);
    metrics?.inc("hub_previous_secrets_expired_total", "Rotated signing secrets removed after their grace period", {}, out.expiredSecrets);
    metrics?.inc("hub_reseal_failures_total", "Subscriptions with a sealed value that could not be opened for re-sealing", {}, out.resealFailed);
    metrics?.inc("hub_client_usage_purged_total", "Per-minute client usage rows deleted by retention", {}, out.usagePurged);
    return out;
  }

  start(intervalMs: number): void {
    if (this.#timer !== null) {
      return;
    }
    const tick = (): void => {
      if (this.#running !== null) {
        return;
      }
      this.#running = this.runOnce()
        .catch((err: unknown) => {
          this.#o.metrics?.inc("hub_maintenance_errors_total", "Maintenance passes that failed");
          this.#o.onError?.(err);
        })
        .finally(() => {
          this.#running = null;
        });
    };
    this.#timer = setInterval(tick, intervalMs);
    this.#timer.unref();
    tick();
  }

  async stop(): Promise<void> {
    if (this.#timer !== null) {
      clearInterval(this.#timer);
      this.#timer = null;
    }
    await this.#running;
  }
}
