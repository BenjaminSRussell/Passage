// Postgres persistence for the integration hub (FR-35.9): subscriptions,
// deliveries and attempts, every query inside a transaction that sets
// passage.tenant_id so row-level security scopes it to one tenant.
//
// PgDeliveryStore implements the Dispatcher's synchronous DeliveryStore for one
// subscription. It is loaded from the database, the Dispatcher mutates it, and
// the changes are written by flush(). The poller flushes before each send and
// after the tick, so a send is always preceded by the recorded outcome of the
// one before it, and a crash between a send and its record leaves the event
// pending: it is sent again (at-least-once).

import { randomUUID } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import pg from "pg";

import type { FieldKeys } from "./crypt.ts";
import type { DeadLetter, DeliveryStore, HubEvent, PendingDelivery } from "./dispatcher.ts";
import { matchesAny } from "./filter.ts";

/** The fieldcrypt field name the signing secret is sealed under (bound with the tenant id). */
export const SECRET_FIELD = "hub_subscription.secret";

/** Per aggregate, the pending deliveries one poller pass can reach (it stops after 100 ticks). */
const LOAD_PER_AGGREGATE = 100;
/** Pending deliveries one poller pass loads, whole per-aggregate prefixes, earliest due head first. */
const LOAD_TOTAL = 5_000;
/** Rows read per page while a replay scans the subscription's recorded deliveries. */
const REPLAY_PAGE = 1_000;

export type DeliveryStatus = "pending" | "delivered" | "dead_letter";
export const DELIVERY_STATUSES: readonly DeliveryStatus[] = ["pending", "delivered", "dead_letter"];

export interface SubscriptionRecord {
  id: string;
  name: string;
  url: string;
  patterns: string[];
  status: "active" | "paused";
  pausedReason: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

/** What the dispatcher needs to send: the URL and the sealed signing secret (opened by openSecret). */
export interface SubscriptionTarget {
  id: string;
  url: string;
  sealedSecret: Buffer;
}

export interface DeliveryRecord {
  id: string;
  subscriptionId: string;
  eventId: string;
  eventType: string;
  aggregateId: string;
  sequence: number;
  status: DeliveryStatus;
  attempts: number;
  lastStatus: number | null;
  nextRetryAt: number | null;
  replay: boolean;
  deadLetterCode: string | null;
  createdAt: string;
}

export interface Page<T> {
  items: T[];
  nextCursor: string | null;
}

export class VersionConflictError extends Error {
  constructor() {
    super("the subscription changed since it was read");
    this.name = "VersionConflictError";
  }
}

/** No FIELDCRYPT keys are configured, so a signing secret can be neither sealed nor opened. */
export class FieldKeysMissingError extends Error {
  constructor() {
    super("webhook signing secrets cannot be stored until field encryption keys are configured");
    this.name = "FieldKeysMissingError";
  }
}

export class BadCursorError extends Error {
  constructor() {
    super("cursor is not valid");
    this.name = "BadCursorError";
  }
}

/** Applies migrations/*.sql in order, each in its own transaction (the dbx.Migrate contract). */
export async function migrate(pool: pg.Pool, dir: URL | string): Promise<void> {
  await pool.query("create table if not exists schema_migrations (name text primary key, applied_at timestamptz not null default now())");
  const names = (await readdir(dir)).filter((n) => n.endsWith(".sql")).sort();
  for (const name of names) {
    const done = await pool.query("select 1 from schema_migrations where name = $1", [name]);
    if (done.rowCount) {
      continue;
    }
    const body = await readFile(new URL(name, dir instanceof URL ? dir : `file://${dir}/`), "utf8");
    const client = await pool.connect();
    try {
      await client.query("begin");
      await client.query(body);
      await client.query("insert into schema_migrations(name) values ($1)", [name]);
      await client.query("commit");
    } catch (err) {
      await client.query("rollback").catch(() => undefined);
      throw new Error(`migration ${name}: ${(err as Error).message}`);
    } finally {
      client.release();
    }
  }
}

export const MIGRATIONS_DIR = new URL("../migrations/", import.meta.url);

const COLUMNS_SUB = "id, name, url, patterns, status, paused_reason, version, created_at, updated_at, created_at::text as cursor_ts";

interface SubRow {
  id: string;
  name: string;
  url: string;
  patterns: string[];
  status: "active" | "paused";
  paused_reason: string | null;
  version: number;
  created_at: Date;
  updated_at: Date;
  cursor_ts: string;
}

function toSubscription(r: SubRow): SubscriptionRecord {
  return {
    id: r.id,
    name: r.name,
    url: r.url,
    patterns: r.patterns,
    status: r.status,
    pausedReason: r.paused_reason,
    version: r.version,
    createdAt: r.created_at.toISOString(),
    updatedAt: r.updated_at.toISOString(),
  };
}

interface DeliveryRow {
  id: string;
  subscription_id: string;
  event_id: string;
  event_type: string;
  aggregate_id: string;
  sequence: string;
  status: DeliveryStatus;
  attempts: number;
  last_status: number | null;
  next_retry_at: number;
  replay: boolean;
  dead_letter_code: string | null;
  created_at: Date;
  cursor_ts: string;
}

function toDelivery(r: DeliveryRow): DeliveryRecord {
  return {
    id: r.id,
    subscriptionId: r.subscription_id,
    eventId: r.event_id,
    eventType: r.event_type,
    aggregateId: r.aggregate_id,
    sequence: Number(r.sequence),
    status: r.status,
    attempts: r.attempts,
    lastStatus: r.last_status,
    nextRetryAt: r.status === "pending" ? r.next_retry_at : null,
    replay: r.replay,
    deadLetterCode: r.dead_letter_code,
    createdAt: r.created_at.toISOString(),
  };
}

// created_at has microsecond precision, which a JS Date cannot hold: the cursor carries Postgres's own text.
function encodeCursor(createdAt: string, id: string): string {
  return Buffer.from(`${createdAt}|${id}`).toString("base64url");
}

function decodeCursor(cursor: string): { createdAt: string; id: string } {
  const [createdAt, id] = Buffer.from(cursor, "base64url").toString("utf8").split("|");
  if (createdAt === undefined || id === undefined || !/^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(\.\d{1,6})?[+-]\d\d(:\d\d)?$/.test(createdAt) || !/^[0-9a-f-]{36}$/.test(id)) {
    throw new BadCursorError();
  }
  return { createdAt, id };
}

export interface NewSubscription {
  name: string;
  url: string;
  secret: string;
  patterns: string[];
  createdBy: string | null;
  idempotencyKey: string | null;
}

export interface SubscriptionPatch {
  version: number;
  name?: string;
  url?: string;
  patterns?: string[];
}

export class HubStore {
  readonly pool: pg.Pool;
  readonly #keys: FieldKeys | null;

  constructor(pool: pg.Pool, keys: FieldKeys | null = null) {
    this.pool = pool;
    this.#keys = keys;
  }

  /** Whether signing secrets can be sealed (FIELDCRYPT keys are configured). */
  get canSeal(): boolean {
    return this.#keys !== null;
  }

  /** Opens a subscription's signing secret for signing; FieldKeysMissingError without keys, OpenError when it does not authenticate. */
  openSecret(tenantId: string, target: SubscriptionTarget): string {
    if (this.#keys === null) {
      throw new FieldKeysMissingError();
    }
    return this.#keys.open(tenantId, SECRET_FIELD, target.sealedSecret).toString("utf8");
  }

  /** Runs fn in a transaction scoped to one tenant by row-level security. */
  async withTenant<T>(tenantId: string, fn: (client: pg.PoolClient) => Promise<T>): Promise<T> {
    const client = await this.pool.connect();
    try {
      await client.query("begin");
      await client.query("select set_config('passage.tenant_id', $1, true)", [tenantId]);
      const out = await fn(client);
      await client.query("commit");
      return out;
    } catch (err) {
      await client.query("rollback").catch(() => undefined);
      throw err;
    } finally {
      client.release();
    }
  }

  /** Creates a subscription; the same idempotency key returns the one created first (`created` false). */
  async createSubscription(tenantId: string, input: NewSubscription): Promise<{ subscription: SubscriptionRecord; created: boolean }> {
    if (this.#keys === null) {
      throw new FieldKeysMissingError();
    }
    const sealed = this.#keys.seal(tenantId, SECRET_FIELD, Buffer.from(input.secret, "utf8"));
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query<SubRow>(
        `insert into hub_subscription (tenant_id, id, name, url, secret_sealed, patterns, created_by, idempotency_key)
         values ($1, $2, $3, $4, $5, $6, $7, $8)
         on conflict (tenant_id, idempotency_key) do nothing
         returning ${COLUMNS_SUB}`,
        [tenantId, randomUUID(), input.name, input.url, sealed, input.patterns, input.createdBy, input.idempotencyKey],
      );
      if (res.rows[0] !== undefined) {
        return { subscription: toSubscription(res.rows[0]), created: true };
      }
      const existing = await c.query<SubRow>(`select ${COLUMNS_SUB} from hub_subscription where idempotency_key = $1`, [input.idempotencyKey]);
      return { subscription: toSubscription(existing.rows[0] as SubRow), created: false };
    });
  }

  async listSubscriptions(tenantId: string, limit: number, cursor: string | null): Promise<Page<SubscriptionRecord>> {
    const after = cursor === null ? null : decodeCursor(cursor);
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query<SubRow>(
        `select ${COLUMNS_SUB} from hub_subscription
          where ($1::timestamptz is null or (created_at, id) < ($1::timestamptz, $2::uuid))
          order by created_at desc, id desc limit $3`,
        [after?.createdAt ?? null, after?.id ?? null, limit + 1],
      );
      const rows = res.rows.slice(0, limit);
      const last = rows[rows.length - 1];
      return {
        items: rows.map(toSubscription),
        nextCursor: res.rows.length > limit && last !== undefined ? encodeCursor(last.cursor_ts, last.id) : null,
      };
    });
  }

  async getSubscription(tenantId: string, id: string): Promise<SubscriptionRecord | null> {
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query<SubRow>(`select ${COLUMNS_SUB} from hub_subscription where id = $1`, [id]);
      return res.rows[0] === undefined ? null : toSubscription(res.rows[0]);
    });
  }

  /** Optimistic update: VersionConflictError when `version` is stale, null when the subscription does not exist. */
  async patchSubscription(tenantId: string, id: string, patch: SubscriptionPatch): Promise<SubscriptionRecord | null> {
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query<SubRow>(
        `update hub_subscription
            set name = coalesce($3, name), url = coalesce($4, url), patterns = coalesce($5, patterns),
                version = version + 1, updated_at = now()
          where id = $1 and version = $2
          returning ${COLUMNS_SUB}`,
        [id, patch.version, patch.name ?? null, patch.url ?? null, patch.patterns ?? null],
      );
      if (res.rows[0] !== undefined) {
        return toSubscription(res.rows[0]);
      }
      const exists = await c.query("select 1 from hub_subscription where id = $1", [id]);
      if (exists.rowCount) {
        throw new VersionConflictError();
      }
      return null;
    });
  }

  async deleteSubscription(tenantId: string, id: string): Promise<boolean> {
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query("delete from hub_subscription where id = $1", [id]);
      return (res.rowCount ?? 0) > 0;
    });
  }

  async listDeliveries(
    tenantId: string,
    filter: { status?: DeliveryStatus; subscriptionId?: string },
    limit: number,
    cursor: string | null,
  ): Promise<Page<DeliveryRecord>> {
    const after = cursor === null ? null : decodeCursor(cursor);
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query<DeliveryRow>(
        `select id, subscription_id, event_id, event_type, aggregate_id, sequence, status, attempts, last_status,
                next_retry_at, replay, dead_letter_code, created_at, created_at::text as cursor_ts
           from hub_delivery
          where ($1::text is null or status = $1) and ($2::uuid is null or subscription_id = $2)
            and ($3::timestamptz is null or (created_at, id) < ($3::timestamptz, $4::uuid))
          order by created_at desc, id desc limit $5`,
        [filter.status ?? null, filter.subscriptionId ?? null, after?.createdAt ?? null, after?.id ?? null, limit + 1],
      );
      const rows = res.rows.slice(0, limit);
      const last = rows[rows.length - 1];
      return {
        items: rows.map(toDelivery),
        nextCursor: res.rows.length > limit && last !== undefined ? encodeCursor(last.cursor_ts, last.id) : null,
      };
    });
  }

  /** The tenant's subscriptions, paused ones included: a paused subscription keeps collecting events until it is resumed. */
  async targets(client: pg.PoolClient): Promise<{ id: string; patterns: string[] }[]> {
    const res = await client.query<{ id: string; patterns: string[] }>("select id, patterns from hub_subscription");
    return res.rows;
  }

  /**
   * Adds the event to each subscription's pending set, in one tenant
   * transaction. An event already known for a subscription (pending, delivered
   * or dead-lettered) is left as it is, so a redelivered Kafka message changes
   * nothing. Returns how many subscriptions took the event.
   */
  async enqueueEvent(
    tenantId: string,
    event: HubEvent,
    matches: (patterns: readonly string[], type: string) => boolean,
    now: number,
  ): Promise<number> {
    return this.withTenant(tenantId, async (c) => {
      let count = 0;
      for (const sub of await this.targets(c)) {
        if (!matches(sub.patterns, event.type)) {
          continue;
        }
        const res = await c.query(
          `insert into hub_delivery (tenant_id, id, subscription_id, event_id, event_type, aggregate_id, sequence, occurred_at, data, next_retry_at)
           values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
           on conflict (tenant_id, subscription_id, event_id) do nothing`,
          [tenantId, randomUUID(), sub.id, event.eventId, event.type, event.aggregateId, event.sequence, event.occurredAt, JSON.stringify(event.data ?? null), now],
        );
        count += res.rowCount ?? 0;
      }
      return count;
    });
  }

  /** Subscriptions with a delivery due and no live lease, across tenants (hub_due_subscriptions). */
  async dueSubscriptions(now: number): Promise<{ tenantId: string; subscriptionId: string }[]> {
    const res = await this.pool.query<{ tenant_id: string; subscription_id: string }>(
      "select tenant_id, subscription_id from hub_due_subscriptions($1)",
      [now],
    );
    return res.rows.map((r) => ({ tenantId: r.tenant_id, subscriptionId: r.subscription_id }));
  }

  /** Takes the poller lease for `seconds` and returns when it ends; null when another replica holds it. */
  async claim(tenantId: string, subscriptionId: string, now: number, seconds: number): Promise<number | null> {
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query(
        "update hub_subscription set lease_until = $3 where id = $1 and (lease_until is null or lease_until <= $2)",
        [subscriptionId, now, now + seconds],
      );
      return (res.rowCount ?? 0) > 0 ? now + seconds : null;
    });
  }

  /** Gives the lease back, unless it already lapsed and another replica took it. */
  async release(tenantId: string, subscriptionId: string, leaseUntil: number): Promise<void> {
    await this.withTenant(tenantId, async (c) => {
      await c.query("update hub_subscription set lease_until = null where id = $1 and lease_until = $2", [subscriptionId, leaseUntil]);
    });
  }

  /**
   * Loads one subscription's pending deliveries for the Dispatcher; null when it
   * does not exist. Each aggregate contributes a prefix of its pending events in
   * sequence order (at most what one pass can deliver), and the aggregates whose
   * head is due first come first, so per-aggregate order holds however large the
   * backlog is and memory stays bounded.
   */
  async loadDeliveryStore(tenantId: string, subscriptionId: string): Promise<{ store: PgDeliveryStore; target: SubscriptionTarget } | null> {
    return this.withTenant(tenantId, async (c) => {
      const sub = await c.query<{ id: string; url: string; secret_sealed: Buffer; status: string }>(
        "select id, url, secret_sealed, status from hub_subscription where id = $1",
        [subscriptionId],
      );
      const row = sub.rows[0];
      if (row === undefined) {
        return null;
      }
      const pending = await c.query<{
        event_id: string; event_type: string; aggregate_id: string; sequence: string; occurred_at: number; data: unknown;
        retries_done: number; next_retry_at: number; replay: boolean;
      }>(
        `select event_id, event_type, aggregate_id, sequence, occurred_at, data, retries_done, next_retry_at, replay
           from (select d.*,
                        row_number() over w as rn,
                        first_value(next_retry_at) over w as head_due
                   from hub_delivery d
                  where subscription_id = $1 and status = 'pending'
                 window w as (partition by aggregate_id order by sequence, event_id)) p
          where rn <= $2
          order by head_due, aggregate_id, rn
          limit $3`,
        [subscriptionId, LOAD_PER_AGGREGATE, LOAD_TOTAL],
      );
      const store = new PgDeliveryStore(this, tenantId, subscriptionId, row.status === "paused");
      for (const p of pending.rows) {
        store.preload({
          event: { eventId: p.event_id, type: p.event_type, aggregateId: p.aggregate_id, sequence: Number(p.sequence), occurredAt: p.occurred_at, data: p.data },
          retriesDone: p.retries_done,
          nextRetryAt: p.next_retry_at,
          replay: p.replay,
        });
      }
      return { store, target: { id: row.id, url: row.url, sealedSecret: row.secret_sealed } };
    });
  }

  /** Clears a 410 pause (HUB-0114); null when the subscription does not exist. Resuming an active subscription changes nothing. */
  async resumeSubscription(tenantId: string, id: string): Promise<SubscriptionRecord | null> {
    return this.withTenant(tenantId, async (c) => {
      await c.query(
        `update hub_subscription set status = 'active', paused_reason = null, paused_at = null, version = version + 1, updated_at = now()
          where id = $1 and status = 'paused'`,
        [id],
      );
      const res = await c.query<SubRow>(`select ${COLUMNS_SUB} from hub_subscription where id = $1`, [id]);
      return res.rows[0] === undefined ? null : toSubscription(res.rows[0]);
    });
  }

  /**
   * Replays the subscription's recorded deliveries (delivered or dead-lettered)
   * from `fromSeconds` (BR-35.9): at most `cap` events whose type still matches
   * the subscription's current patterns become pending again as replays (sent
   * with ce-passagereplay: true), oldest first; the dispatcher's per-aggregate
   * order applies when they are sent. Everything runs in the subscription's
   * tenant, so another tenant's events cannot be reached. `truncated` is true
   * when more matching events remain after the cap. Null when the subscription
   * does not exist.
   */
  async replay(
    tenantId: string,
    subscriptionId: string,
    fromSeconds: number,
    cap: number,
    now: number,
  ): Promise<{ enqueued: number; truncated: boolean } | null> {
    return this.withTenant(tenantId, async (c) => {
      // The row lock serializes replays of one subscription.
      const sub = await c.query<{ patterns: string[] }>("select patterns from hub_subscription where id = $1 for update", [subscriptionId]);
      const patterns = sub.rows[0]?.patterns;
      if (patterns === undefined) {
        return null;
      }
      let enqueued = 0;
      let after: { at: number; id: string } | null = null;
      for (;;) {
        const page: pg.QueryResult<{ id: string; event_type: string; occurred_at: number }> = await c.query(
          `select id, event_type, occurred_at from hub_delivery
            where subscription_id = $1 and status <> 'pending' and occurred_at >= $2
              and ($3::double precision is null or (occurred_at, id) > ($3, $4::uuid))
            order by occurred_at, id limit $5`,
          [subscriptionId, fromSeconds, after?.at ?? null, after?.id ?? null, REPLAY_PAGE],
        );
        const matching = page.rows.filter((r) => matchesAny(patterns, r.event_type));
        const take = matching.slice(0, cap - enqueued).map((r) => r.id);
        if (take.length > 0) {
          await c.query(
            `update hub_delivery
                set status = 'pending', replay = true, retries_done = 0, next_retry_at = $2, attempts = 0,
                    dead_letter_code = null, updated_at = now()
              where id = any($1::uuid[]) and status <> 'pending'`,
            [take, now],
          );
          enqueued += take.length;
        }
        if (matching.length > take.length) {
          return { enqueued, truncated: true };
        }
        const last = page.rows[page.rows.length - 1];
        if (page.rows.length < REPLAY_PAGE || last === undefined) {
          return { enqueued, truncated: false };
        }
        after = { at: last.occurred_at, id: last.id };
      }
    });
  }
}

type Op =
  | { kind: "put"; delivery: PendingDelivery }
  | { kind: "remove"; eventId: string }
  | { kind: "dead"; letter: DeadLetter }
  | { kind: "paused"; paused: boolean }
  | { kind: "attempt"; eventId: string; status: number | null };

export class PgDeliveryStore implements DeliveryStore {
  readonly #db: HubStore;
  readonly #tenantId: string;
  readonly #subscriptionId: string;
  #pending = new Map<string, PendingDelivery>();
  #dead: DeadLetter[] = [];
  #paused: boolean;
  #ops: Op[] = [];

  constructor(db: HubStore, tenantId: string, subscriptionId: string, paused: boolean) {
    this.#db = db;
    this.#tenantId = tenantId;
    this.#subscriptionId = subscriptionId;
    this.#paused = paused;
  }

  preload(d: PendingDelivery): void {
    this.#pending.set(d.event.eventId, d);
  }

  #check(subscriptionId: string): void {
    if (subscriptionId !== this.#subscriptionId) {
      throw new Error(`store is loaded for subscription ${this.#subscriptionId}, not ${subscriptionId}`);
    }
  }

  pending(subscriptionId: string): PendingDelivery[] {
    this.#check(subscriptionId);
    return [...this.#pending.values()].map((d) => ({ ...d }));
  }
  put(subscriptionId: string, delivery: PendingDelivery): void {
    this.#check(subscriptionId);
    this.#pending.set(delivery.event.eventId, { ...delivery });
    this.#ops.push({ kind: "put", delivery: { ...delivery } });
  }
  remove(subscriptionId: string, eventId: string): void {
    this.#check(subscriptionId);
    this.#pending.delete(eventId);
    this.#ops.push({ kind: "remove", eventId });
  }
  deadLetter(subscriptionId: string, letter: DeadLetter): void {
    this.#check(subscriptionId);
    this.#dead.push(letter);
    this.#ops.push({ kind: "dead", letter });
  }
  deadLetters(subscriptionId: string): DeadLetter[] {
    this.#check(subscriptionId);
    return [...this.#dead];
  }
  isPaused(subscriptionId: string): boolean {
    this.#check(subscriptionId);
    return this.#paused;
  }
  setPaused(subscriptionId: string, paused: boolean): void {
    this.#check(subscriptionId);
    this.#paused = paused;
    this.#ops.push({ kind: "paused", paused });
  }

  /** Queues the record of one send (its HTTP status, or null for no response). */
  recordAttempt(eventId: string, status: number | null): void {
    this.#ops.push({ kind: "attempt", eventId, status });
  }

  /** Writes the queued changes in order, in one tenant transaction. */
  async flush(): Promise<void> {
    if (this.#ops.length === 0) {
      return;
    }
    const ops = this.#ops;
    this.#ops = [];
    try {
      await this.#db.withTenant(this.#tenantId, async (c) => {
        for (const op of ops) {
          await this.#apply(c, op);
        }
      });
    } catch (err) {
      this.#ops = [...ops, ...this.#ops];
      throw err;
    }
  }

  async #apply(c: pg.PoolClient, op: Op): Promise<void> {
    const sub = this.#subscriptionId;
    switch (op.kind) {
      case "put": {
        const e = op.delivery.event;
        // A late retry never revives a delivered or dead-lettered event; a replay does.
        await c.query(
          `insert into hub_delivery (tenant_id, id, subscription_id, event_id, event_type, aggregate_id, sequence, occurred_at, data,
                                     retries_done, next_retry_at, replay)
           values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
           on conflict (tenant_id, subscription_id, event_id) do update
              set status = 'pending', retries_done = excluded.retries_done, next_retry_at = excluded.next_retry_at,
                  replay = excluded.replay, dead_letter_code = null, updated_at = now(),
                  attempts = case when hub_delivery.status = 'pending' then hub_delivery.attempts else 0 end
            where hub_delivery.status = 'pending' or excluded.replay`,
          [this.#tenantId, randomUUID(), sub, e.eventId, e.type, e.aggregateId, e.sequence, e.occurredAt, JSON.stringify(e.data ?? null),
           op.delivery.retriesDone, op.delivery.nextRetryAt, op.delivery.replay],
        );
        return;
      }
      case "remove":
        await c.query(
          "update hub_delivery set status = 'delivered', updated_at = now() where subscription_id = $1 and event_id = $2 and status = 'pending'",
          [sub, op.eventId],
        );
        return;
      case "dead":
        await c.query(
          `update hub_delivery set status = 'dead_letter', dead_letter_code = $3, attempts = $4, last_status = $5, updated_at = now()
            where subscription_id = $1 and event_id = $2 and status = 'pending'`,
          [sub, op.letter.event.eventId, op.letter.code, op.letter.attempts, op.letter.lastStatus],
        );
        return;
      case "paused":
        await c.query(
          `update hub_subscription
              set status = $2, paused_reason = $3, paused_at = $4, version = version + 1, updated_at = now()
            where id = $1 and status <> $2`,
          [sub, op.paused ? "paused" : "active", op.paused ? "HUB-0114" : null, op.paused ? new Date() : null],
        );
        return;
      case "attempt":
        await c.query(
          `with d as (
             update hub_delivery set attempts = attempts + 1, last_status = $3, updated_at = now()
              where subscription_id = $1 and event_id = $2 returning id, attempts)
           insert into hub_attempt (tenant_id, id, delivery_id, attempt_no, http_status)
           select $4, $5, d.id, d.attempts, $3 from d`,
          [sub, op.eventId, op.status, this.#tenantId, randomUUID()],
        );
        return;
    }
  }
}
