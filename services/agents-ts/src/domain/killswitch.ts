import type { KillSwitchPort } from "../runtime/engine.js";

/** BR-47.5 / AC-47.6: a toggle must reach every agent worker within this window. */
export const KILL_PROPAGATION_MS = 10_000;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export type KillScope = { scope: "GLOBAL"; tenantId: null } | { scope: "TENANT"; tenantId: string };

/** One toggle, ordered by `version` (assigned by the store, strictly increasing). */
export type KillSwitchChange = KillScope & {
  version: number;
  active: boolean;
  byUser: string;
  changedAt: string;
};

export function buildKillSwitchChange(input: {
  scope: "GLOBAL" | "TENANT";
  tenantId?: string;
  active: boolean;
  byUser: string;
  version: number;
  changedAt: string;
}): KillSwitchChange {
  if (!UUID.test(input.byUser)) throw new Error("byUser must be a user id");
  if (!Number.isSafeInteger(input.version) || input.version < 1) throw new Error("version must be a positive integer");
  if (Number.isNaN(Date.parse(input.changedAt))) throw new Error("changedAt must be an RFC 3339 timestamp");
  if (input.scope === "GLOBAL") {
    return { scope: "GLOBAL", tenantId: null, active: input.active, byUser: input.byUser, version: input.version, changedAt: input.changedAt };
  }
  if (input.tenantId === undefined || !UUID.test(input.tenantId)) throw new Error("TENANT scope needs a tenant id");
  return { scope: "TENANT", tenantId: input.tenantId, active: input.active, byUser: input.byUser, version: input.version, changedAt: input.changedAt };
}

/**
 * Worker-side kill switch state (FR-47.12). Every worker applies the
 * `killswitch.changed` stream in version order and ignores duplicates or
 * stale versions. A worker that has not synced for KILL_PROPAGATION_MS fails
 * closed: it reports every tenant as halted until it hears from the stream
 * again, so a partitioned worker can never keep running after a kill.
 */
export class KillSwitchRegistry implements KillSwitchPort {
  private appliedVersion = 0;
  private globalOn = false;
  private readonly tenantsOn = new Set<string>();
  private syncedAtMs: number;
  private readonly nowMs: () => number;
  private readonly maxStalenessMs: number;

  constructor(nowMs: () => number, maxStalenessMs: number = KILL_PROPAGATION_MS) {
    this.nowMs = nowMs;
    this.maxStalenessMs = maxStalenessMs;
    this.syncedAtMs = nowMs();
  }

  /** Applies a change. Returns false for a duplicate or out-of-order version. */
  apply(change: KillSwitchChange): boolean {
    this.syncedAtMs = this.nowMs();
    if (change.version <= this.appliedVersion) return false;
    this.appliedVersion = change.version;
    if (change.scope === "GLOBAL") {
      this.globalOn = change.active;
    } else if (change.active) {
      this.tenantsOn.add(change.tenantId);
    } else {
      this.tenantsOn.delete(change.tenantId);
    }
    return true;
  }

  /** Called on every stream heartbeat, even when no toggle arrived. */
  heartbeat(): void {
    this.syncedAtMs = this.nowMs();
  }

  get version(): number {
    return this.appliedVersion;
  }

  isStale(): boolean {
    return this.nowMs() - this.syncedAtMs > this.maxStalenessMs;
  }

  isHalted(tenantId: string): boolean {
    if (this.isStale()) return true;
    return this.globalOn || this.tenantsOn.has(tenantId);
  }
}
