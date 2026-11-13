import { randomUUID } from "node:crypto";
import type pg from "pg";
import { killSwitchChangedEvent } from "../domain/events.js";
import { buildKillSwitchChange, type KillSwitchChange } from "../domain/killswitch.js";
import type { KillSwitchLog, KillSwitchToggle, KillSwitchToggleResult } from "../api/killswitch.js";
import type { PgAgentStore } from "./pg.js";

/** One advisory lock key for every toggle: versions are drawn and committed in order (0005_kill_switch.sql). */
export const KILL_SWITCH_LOCK_KEY = 4_747_0005;

interface Row {
  version: string;
  scope: "GLOBAL" | "TENANT";
  target_tenant_id: string | null;
  active: boolean;
  by_user: string;
  changed_at: Date;
}

const asChange = (r: Row): KillSwitchChange =>
  buildKillSwitchChange({
    scope: r.scope,
    ...(r.target_tenant_id !== null ? { tenantId: r.target_tenant_id } : {}),
    active: r.active,
    byUser: r.by_user,
    version: Number(r.version),
    changedAt: r.changed_at.toISOString(),
  });

const COLUMNS = "version, scope, target_tenant_id, active, by_user, changed_at";

/**
 * Toggles after `version`, oldest first. From version 0 only the latest toggle of each switch, so a starting pod
 * does not replay the whole history.
 */
async function readSince(q: Pick<pg.PoolClient, "query">, version: number): Promise<KillSwitchChange[]> {
  const sql =
    version === 0
      ? `select * from (select distinct on (scope, target_tenant_id) ${COLUMNS} from kill_switch_change
           order by scope, target_tenant_id, version desc) latest order by version`
      : `select ${COLUMNS} from kill_switch_change where version > $1 order by version`;
  const res = await q.query<Row>(sql, version === 0 ? [] : [version]);
  return res.rows.map(asChange);
}

/**
 * `kill_switch_change` (migration 0005): the versioned kill switch every pod polls (AC-47.6).
 * A toggle is stored and its `killswitch.changed` event written to the outbox in one transaction.
 */
export class PgKillSwitchLog implements KillSwitchLog {
  constructor(
    private readonly store: PgAgentStore,
    private readonly pool: pg.Pool,
  ) {}

  async toggle(t: KillSwitchToggle, sinceVersion: number): Promise<KillSwitchToggleResult> {
    return this.store.withTenant(t.actorTenantId, async (c) => {
      await c.query("select pg_advisory_xact_lock($1)", [KILL_SWITCH_LOCK_KEY]);
      const current = (
        await c.query<Row>(
          `select ${COLUMNS} from kill_switch_change where scope = $1 and target_tenant_id is not distinct from $2 order by version desc limit 1`,
          [t.scope, t.tenantId ?? null],
        )
      ).rows[0];
      // Read under the lock: every lower version has committed, so the list has no gap.
      if (current !== undefined && current.active === t.active) {
        return { change: asChange(current), created: false, changes: await readSince(c, sinceVersion) };
      }
      const inserted = (
        await c.query<Row>(
          `insert into kill_switch_change (tenant_id, scope, target_tenant_id, active, by_user, changed_at)
           values ($1, $2, $3, $4, $5, $6) returning ${COLUMNS}`,
          [t.actorTenantId, t.scope, t.tenantId ?? null, t.active, t.byUser, t.changedAt],
        )
      ).rows[0]!;
      const change = asChange(inserted);
      const event = killSwitchChangedEvent(change, t.actorTenantId);
      await c.query(
        "insert into outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values ($1, $2, 'kill_switch', $3, $4, $5::jsonb)",
        [randomUUID(), t.actorTenantId, t.actorTenantId, event.type, JSON.stringify(event.data)],
      );
      return { change, created: true, changes: await readSince(c, sinceVersion) };
    });
  }

  /** Toggles after `version`, oldest first (readSince); what each pod polls. */
  async changesSince(version: number): Promise<KillSwitchChange[]> {
    return readSince(this.pool, version);
  }
}
