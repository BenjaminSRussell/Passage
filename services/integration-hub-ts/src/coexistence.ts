// Legacy coexistence adapters (wave 49, FR-49.7, AC-49.7). While a legacy company is cut over to
// Passage entity by entity, each entity kind is mastered on one side: on LEGACY the kind's adapter
// (AX OData/DMF, SAP IDoc/BAPI) brings the legacy changes in; on PASSAGE its flow is suspended so
// the two sides never both write one record. The migration factory owns the flags and announces
// each change (dev.passage.migration.coexistence.changed.v1); a cutover rollback hands every
// cut-over kind of the plan's companies back to LEGACY and lists them on
// dev.passage.cutover.plan.rolledback.v1, on which the adapters must resume.
//
// The consumer records the mode each adapter should be in (record), in flag version order, so a
// replayed or late event never undoes a newer change. The sweep (CoexistenceSweeper) then tells the
// adapter: suspend for PASSAGE, resume for LEGACY. A failure backs off (one minute doubling to an
// hour) and is parked after five attempts, at once when the adapter refuses or no adapter serves
// the system; other rows go on. Suspend and resume are idempotent on the adapter side, so a row
// told twice (two replicas, a crash between the call and its record) does no harm.

import pg from "pg";

import type { HubEvent } from "./dispatcher.ts";
import type { Metrics } from "./metrics.ts";

export const ROLLED_BACK_TYPE = "dev.passage.cutover.plan.rolledback.v1";
export const COEXISTENCE_CHANGED_TYPE = "dev.passage.migration.coexistence.changed.v1";

export const SOURCE_SYSTEMS = ["AX2012", "D365FO", "SAPECC", "S4HANA"] as const;
export type SourceSystem = (typeof SOURCE_SYSTEMS)[number];
export type CoexistMode = "LEGACY" | "PASSAGE";

/** Attempts before a failing row is parked. */
export const MAX_ATTEMPTS = 5;
/** Codes a row is parked or backed off with. */
export const CODE_NO_ADAPTER = "HUB-COEXIST-NO-ADAPTER";
export const CODE_REFUSED = "HUB-COEXIST-REFUSED";
export const CODE_FAILED = "HUB-COEXIST-FAILED";
/** How long a claimed row is held from other replicas while its adapter is called. */
const CLAIM_SECONDS = 60;

/** One entity kind's flag as an event carries it. */
export interface CoexistenceChange {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  entity: string;
  mode: CoexistMode;
  version: number;
  planId: string | null;
}

/** A legacy system's coexistence adapter, as the hub drives it. Both calls must be idempotent. */
export interface CoexistenceAdapter {
  readonly system: SourceSystem;
  suspend(tenantId: string, company: string, entity: string): Promise<void>;
  resume(tenantId: string, company: string, entity: string): Promise<void>;
}

/** Thrown by an adapter that refuses a change a retry would only repeat (an unknown company or entity): the row is parked. */
export class CoexistenceRefusedError extends Error {
  override name = "CoexistenceRefusedError";
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function change(raw: unknown, planId: string | null): CoexistenceChange | null {
  if (typeof raw !== "object" || raw === null) return null;
  const o = raw as Record<string, unknown>;
  const { source_system: system, source_company: company, entity, mode, version } = o;
  if (typeof system !== "string" || !(SOURCE_SYSTEMS as readonly string[]).includes(system)) return null;
  if (typeof company !== "string" || company.length < 1 || company.length > 64) return null;
  if (typeof entity !== "string" || entity.length < 1 || entity.length > 64) return null;
  if (mode !== "LEGACY" && mode !== "PASSAGE") return null;
  if (typeof version !== "number" || !Number.isSafeInteger(version) || version < 1) return null;
  return { sourceSystem: system as SourceSystem, sourceCompany: company, entity, mode, version, planId };
}

/**
 * The coexistence changes an event asks for: the flags a rollback handed back (every one LEGACY) or
 * the one flag that changed. Entries that do not describe a flag are counted in `invalid` and skipped.
 */
export function coexistenceChanges(event: HubEvent): { changes: CoexistenceChange[]; invalid: number } {
  const data = (typeof event.data === "object" && event.data !== null ? event.data : {}) as Record<string, unknown>;
  const planId = typeof data["plan_id"] === "string" && UUID.test(data["plan_id"]) ? data["plan_id"].toLowerCase() : null;
  const out: CoexistenceChange[] = [];
  let invalid = 0;
  if (event.type === ROLLED_BACK_TYPE) {
    const list = Array.isArray(data["coexistence_resumed"]) ? data["coexistence_resumed"] : [];
    for (const raw of list) {
      const c = change(raw, planId);
      if (c === null || c.mode !== "LEGACY") invalid += 1;
      else out.push(c);
    }
  } else if (event.type === COEXISTENCE_CHANGED_TYPE) {
    const c = change(data, planId);
    if (c === null) invalid += 1;
    else out.push(c);
  }
  return { changes: out, invalid };
}

/** Backoff after the n-th failed attempt: one minute doubled per attempt, at most an hour. */
export function backoffSeconds(attempts: number): number {
  return Math.min(60 * 2 ** Math.max(attempts - 1, 0), 3600);
}

export interface AdapterState {
  sourceSystem: SourceSystem;
  sourceCompany: string;
  entity: string;
  desiredMode: CoexistMode;
  desiredVersion: number;
  appliedMode: CoexistMode | null;
  appliedVersion: number;
  status: "pending" | "applied" | "parked";
  attempts: number;
  lastCode: string;
  nextAttemptAt: Date;
  planId: string | null;
}

interface StateRow {
  source_system: SourceSystem;
  source_company: string;
  entity: string;
  desired_mode: CoexistMode;
  desired_version: number;
  applied_mode: CoexistMode | null;
  applied_version: number;
  status: AdapterState["status"];
  attempts: number;
  last_code: string;
  next_attempt_at: Date;
  plan_id: string | null;
}

const STATE_COLUMNS =
  "source_system, source_company, entity, desired_mode, desired_version, applied_mode, applied_version, status, attempts, last_code, next_attempt_at, plan_id";

function toState(r: StateRow): AdapterState {
  return {
    sourceSystem: r.source_system,
    sourceCompany: r.source_company,
    entity: r.entity,
    desiredMode: r.desired_mode,
    desiredVersion: r.desired_version,
    appliedMode: r.applied_mode,
    appliedVersion: r.applied_version,
    status: r.status,
    attempts: r.attempts,
    lastCode: r.last_code,
    nextAttemptAt: r.next_attempt_at,
    planId: r.plan_id,
  };
}

export interface AdapterKey {
  tenantId: string;
  sourceSystem: SourceSystem;
  sourceCompany: string;
  entity: string;
}

/** Postgres persistence of the adapter state; every query runs in a transaction scoped to one tenant. */
export class CoexistenceStore {
  readonly #pool: pg.Pool;

  constructor(pool: pg.Pool) {
    this.#pool = pool;
  }

  async #withTenant<T>(tenantId: string, fn: (c: pg.PoolClient) => Promise<T>): Promise<T> {
    const c = await this.#pool.connect();
    try {
      await c.query("begin");
      await c.query("select set_config('passage.tenant_id', $1, true)", [tenantId]);
      const out = await fn(c);
      await c.query("commit");
      return out;
    } catch (err) {
      await c.query("rollback").catch(() => undefined);
      throw err;
    } finally {
      c.release();
    }
  }

  /**
   * Records the mode each adapter should be in. A change is taken only when its version is newer
   * than the one recorded, so an event replayed or delivered late changes nothing. Returns how many
   * rows changed.
   */
  async record(tenantId: string, changes: CoexistenceChange[], eventId: string, now: Date): Promise<number> {
    if (changes.length === 0) return 0;
    return this.#withTenant(tenantId, async (c) => {
      let n = 0;
      for (const ch of changes) {
        const res = await c.query(
          `insert into hub_coexistence_adapter (tenant_id, source_system, source_company, entity, desired_mode, desired_version, status,
             next_attempt_at, plan_id, last_event_id, updated_at)
           values ($1, $2, $3, $4, $5, $6, 'pending', $7, $8, $9, $7)
           on conflict (tenant_id, source_system, source_company, entity) do update
             set desired_mode = excluded.desired_mode, desired_version = excluded.desired_version,
                 status = case when hub_coexistence_adapter.applied_mode = excluded.desired_mode
                                and hub_coexistence_adapter.applied_version = excluded.desired_version then 'applied' else 'pending' end,
                 attempts = 0, last_code = '', last_error = '', next_attempt_at = excluded.next_attempt_at,
                 plan_id = excluded.plan_id, last_event_id = excluded.last_event_id, updated_at = excluded.updated_at
             where excluded.desired_version > hub_coexistence_adapter.desired_version`,
          [tenantId, ch.sourceSystem, ch.sourceCompany, ch.entity, ch.mode, ch.version, now, ch.planId, eventId],
        );
        n += res.rowCount ?? 0;
      }
      return n;
    });
  }

  /** Rows due for their adapter call, across tenants (hub_coexistence_due). */
  async due(now: Date, limit: number): Promise<AdapterKey[]> {
    const res = await this.#pool.query<{ tenant_id: string; source_system: SourceSystem; source_company: string; entity: string }>(
      "select tenant_id, source_system, source_company, entity from hub_coexistence_due($1, $2)",
      [now, limit],
    );
    return res.rows.map((r) => ({ tenantId: r.tenant_id, sourceSystem: r.source_system, sourceCompany: r.source_company, entity: r.entity }));
  }

  /** Takes a due row for CLAIM_SECONDS and returns the change to apply; null when another replica has it or it is no longer due. */
  async claim(k: AdapterKey, now: Date): Promise<{ mode: CoexistMode; version: number; attempts: number } | null> {
    return this.#withTenant(k.tenantId, async (c) => {
      const res = await c.query<{ desired_mode: CoexistMode; desired_version: number; attempts: number }>(
        `update hub_coexistence_adapter set next_attempt_at = $4::timestamptz + make_interval(secs => $5)
          where source_system = $1 and source_company = $2 and entity = $3 and status = 'pending' and next_attempt_at <= $4
          returning desired_mode, desired_version, attempts`,
        [k.sourceSystem, k.sourceCompany, k.entity, now, CLAIM_SECONDS],
      );
      const r = res.rows[0];
      return r === undefined ? null : { mode: r.desired_mode, version: r.desired_version, attempts: r.attempts };
    });
  }

  /** Records a successful call for `version`; a newer change recorded meanwhile stays pending and is due at once. */
  async applied(k: AdapterKey, mode: CoexistMode, version: number, now: Date): Promise<void> {
    await this.#withTenant(k.tenantId, (c) =>
      c.query(
        `update hub_coexistence_adapter
            set applied_mode = $4, applied_version = $5, attempts = 0, last_code = '', last_error = '', updated_at = $6,
                status = case when desired_version = $5 then 'applied' else 'pending' end,
                next_attempt_at = case when desired_version = $5 then next_attempt_at else $6 end
          where source_system = $1 and source_company = $2 and entity = $3 and desired_version >= $5`,
        [k.sourceSystem, k.sourceCompany, k.entity, mode, version, now],
      ),
    );
  }

  /** Records a failed call for `version`: back off, or park after a refusal or MAX_ATTEMPTS. A newer change recorded meanwhile is left as it is. */
  async failed(k: AdapterKey, version: number, code: string, error: string, park: boolean, now: Date): Promise<"parked" | "retry" | "superseded"> {
    return this.#withTenant(k.tenantId, async (c) => {
      const res = await c.query<{ status: string }>(
        `update hub_coexistence_adapter
            set attempts = attempts + 1, last_code = $5, last_error = left($6, 1000), updated_at = $7,
                status = case when $8 or attempts + 1 >= $9 then 'parked' else 'pending' end,
                next_attempt_at = $7::timestamptz + make_interval(secs => least(60 * power(2, attempts), 3600))
          where source_system = $1 and source_company = $2 and entity = $3 and desired_version = $4 and status = 'pending'
          returning status`,
        [k.sourceSystem, k.sourceCompany, k.entity, version, code, error, now, park, MAX_ATTEMPTS],
      );
      const r = res.rows[0];
      return r === undefined ? "superseded" : r.status === "parked" ? "parked" : "retry";
    });
  }

  /** Puts a tenant's parked rows back in the queue (an operator's retry after fixing the adapter). Returns how many. */
  async retryParked(tenantId: string, now: Date): Promise<number> {
    return this.#withTenant(tenantId, async (c) => {
      const res = await c.query(
        "update hub_coexistence_adapter set status = 'pending', attempts = 0, next_attempt_at = $1, updated_at = $1 where status = 'parked'",
        [now],
      );
      return res.rowCount ?? 0;
    });
  }

  /** The adapter state of a tenant's legacy company, by entity kind. */
  async list(tenantId: string, sourceSystem: SourceSystem, sourceCompany: string): Promise<AdapterState[]> {
    return this.#withTenant(tenantId, async (c) => {
      const res = await c.query<StateRow>(
        `select ${STATE_COLUMNS} from hub_coexistence_adapter where source_system = $1 and source_company = $2 order by entity`,
        [sourceSystem, sourceCompany],
      );
      return res.rows.map(toState);
    });
  }
}

export interface SweeperOptions {
  store: CoexistenceStore;
  adapters: CoexistenceAdapter[];
  metrics?: Metrics;
  clock?: () => Date;
  batch?: number;
  onError?: (err: unknown) => void;
}

export interface SweepResult {
  applied: number;
  retried: number;
  parked: number;
}

/** Tells each due adapter the mode its entity kind is in now: suspend for PASSAGE, resume for LEGACY. */
export class CoexistenceSweeper {
  readonly #o: SweeperOptions;
  readonly #adapters: Map<SourceSystem, CoexistenceAdapter>;
  #timer: NodeJS.Timeout | null = null;
  #running: Promise<unknown> | null = null;

  constructor(o: SweeperOptions) {
    this.#o = o;
    this.#adapters = new Map(o.adapters.map((a) => [a.system, a]));
  }

  async runOnce(): Promise<SweepResult> {
    const { store, metrics } = this.#o;
    const now = (this.#o.clock ?? (() => new Date()))();
    const out: SweepResult = { applied: 0, retried: 0, parked: 0 };
    for (const k of await store.due(now, this.#o.batch ?? 200)) {
      const job = await store.claim(k, now);
      if (job === null) continue;
      const adapter = this.#adapters.get(k.sourceSystem);
      let outcome: "applied" | "parked" | "retry" | "superseded";
      if (adapter === undefined) {
        outcome = await store.failed(k, job.version, CODE_NO_ADAPTER, `no coexistence adapter serves ${k.sourceSystem}`, true, now);
      } else {
        try {
          if (job.mode === "PASSAGE") await adapter.suspend(k.tenantId, k.sourceCompany, k.entity);
          else await adapter.resume(k.tenantId, k.sourceCompany, k.entity);
          await store.applied(k, job.mode, job.version, now);
          outcome = "applied";
        } catch (err) {
          const refused = err instanceof CoexistenceRefusedError;
          outcome = await store.failed(k, job.version, refused ? CODE_REFUSED : CODE_FAILED, (err as Error).message ?? String(err), refused, now);
          this.#o.onError?.(err);
        }
      }
      if (outcome === "applied") out.applied += 1;
      else if (outcome === "parked") out.parked += 1;
      else if (outcome === "retry") out.retried += 1;
      metrics?.inc("hub_coexistence_adapter_calls_total", "Coexistence adapter suspend and resume calls by outcome", { outcome, mode: job.mode });
    }
    return out;
  }

  start(intervalMs: number): void {
    if (this.#timer !== null) return;
    const tick = (): void => {
      if (this.#running !== null) return;
      this.#running = this.runOnce()
        .catch((err: unknown) => {
          this.#o.metrics?.inc("hub_coexistence_sweep_errors_total", "Coexistence sweeps that failed");
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

/** The consumer's hook: records the coexistence changes an event asks for; other events change nothing. */
export function coexistenceRecorder(store: CoexistenceStore, metrics?: Metrics): (tenantId: string, event: HubEvent, now: Date) => Promise<number> {
  return async (tenantId, event, now) => {
    if (event.type !== ROLLED_BACK_TYPE && event.type !== COEXISTENCE_CHANGED_TYPE) return 0;
    const { changes, invalid } = coexistenceChanges(event);
    if (invalid > 0) {
      metrics?.inc("hub_coexistence_invalid_entries_total", "Coexistence entries in an event that do not describe a flag", {}, invalid);
    }
    const n = await store.record(tenantId, changes, event.eventId, now);
    metrics?.inc("hub_coexistence_changes_recorded_total", "Coexistence flag changes recorded for the adapters", { type: event.type }, n);
    return n;
  };
}
