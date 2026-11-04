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

import { envelopeKeyId, OpenError, type FieldKeys } from "./crypt.ts";
import type { DeadLetter, DeliveryStore, HubEvent, PendingDelivery } from "./dispatcher.ts";
import { enqueue } from "./events.ts";
import { CesqlSyntaxError, filterFor, type FilterEvent } from "./cesql.ts";
import { matchesAny } from "./filter.ts";
import type { RestConfig, RestCredentials, RestTarget } from "./restPush.ts";

/** The fieldcrypt field name the signing secret is sealed under (bound with the tenant id). */
export const SECRET_FIELD = "hub_subscription.secret";
/** The field the rest-push credentials (one JSON object) are sealed under. */
export const CREDENTIALS_FIELD = "hub_subscription.credentials";

/** Events written by the migration loader (the legacy ERP's own import) are not pushed back to it. */
export const ECHO_ACTOR = "service:migration-loader";

/** Per aggregate, the pending deliveries one poller pass can reach (it stops after 100 ticks). */
const LOAD_PER_AGGREGATE = 100;
/** Pending deliveries one poller pass loads, whole per-aggregate prefixes, earliest due head first. */
const LOAD_TOTAL = 5_000;
/** Rows read per page while a replay scans the subscription's recorded deliveries. */
const REPLAY_PAGE = 1_000;
/** The dead-letter code a replay never touches (a pushed record whose legacy key conflicts; see replayDelivery). A constant, never input. */
const NOT_REPLAYABLE = "HUB-0118";

export type DeliveryStatus = "pending" | "delivered" | "dead_letter";
export const DELIVERY_STATUSES: readonly DeliveryStatus[] = ["pending", "delivered", "dead_letter"];

export type SubscriptionKind = "webhook" | "rest-push";

export interface SubscriptionRecord {
  id: string;
  kind: SubscriptionKind;
  name: string;
  url: string;
  /** rest-push only: the settings without credentials. `auth.hasCredentials` is always true; the secrets are never returned. */
  rest: (Omit<RestConfig, "auth"> & { auth: RestConfig["auth"] & { hasCredentials: true } }) | null;
  patterns: string[];
  /** A CESQL expression over the event (src/cesql.ts); null delivers every event the patterns match. */
  filter: string | null;
  status: "active" | "paused";
  pausedReason: string | null;
  version: number;
  /** Webhook only: when the signing secret was last rotated. */
  secretRotatedAt: string | null;
  /** Webhook only: until when the secret before the last rotation still signs deliveries (null: none does). */
  previousSecretExpiresAt: string | null;
  createdAt: string;
  updatedAt: string;
}

/** What the dispatcher needs to send: the URL and the sealed signing secret (opened by openSecret). */
export interface SubscriptionTarget {
  id: string;
  kind: SubscriptionKind;
  url: string;
  sealedSecret: Buffer | null;
  /** The secret before the last rotation and when it stops signing (unix seconds), while its grace period runs. */
  sealedPreviousSecret: Buffer | null;
  previousSecretExpiresAt: number | null;
  restConfig: RestConfig | null;
  sealedCredentials: Buffer | null;
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
  /** The receiver's last answer, truncated, credentials removed (rest-push). */
  responseExcerpt: string | null;
  /** The legacy key the receiver answered with (rest-push). */
  legacyKey: string | null;
  /** The legacy keys of the elements of the pushed collection, by position (rest-push with lineKeyPath). */
  lineKeys: { index: number; legacyKey: string }[];
  /** The lookup table versions the last render read (rest-push, migration slice 20), for audit. */
  lookups: { name: string; table: string; version: number }[];
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

const COLUMNS_SUB =
  "id, kind, rest_config, name, url, patterns, event_filter, status, paused_reason, version, secret_rotated_at, previous_secret_expires_at, created_at, updated_at, created_at::text as cursor_ts";

interface SubRow {
  id: string;
  kind: SubscriptionKind;
  rest_config: RestConfig | null;
  name: string;
  url: string;
  patterns: string[];
  event_filter: string | null;
  status: "active" | "paused";
  paused_reason: string | null;
  version: number;
  secret_rotated_at: Date | null;
  previous_secret_expires_at: Date | null;
  created_at: Date;
  updated_at: Date;
  cursor_ts: string;
}

function toSubscription(r: SubRow): SubscriptionRecord {
  return {
    id: r.id,
    kind: r.kind,
    rest: r.rest_config === null ? null : { ...r.rest_config, auth: { ...r.rest_config.auth, hasCredentials: true } },
    name: r.name,
    url: r.url,
    patterns: r.patterns,
    filter: r.event_filter,
    status: r.status,
    pausedReason: r.paused_reason,
    version: r.version,
    secretRotatedAt: r.secret_rotated_at === null ? null : r.secret_rotated_at.toISOString(),
    previousSecretExpiresAt: r.previous_secret_expires_at === null ? null : r.previous_secret_expires_at.toISOString(),
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
  response_excerpt: string | null;
  legacy_key: string | null;
  line_keys: { index: number; legacyKey: string }[] | null;
  render_lookups: { name: string; table: string; version: number }[] | null;
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
    responseExcerpt: r.response_excerpt,
    legacyKey: r.legacy_key,
    lineKeys: r.line_keys ?? [],
    lookups: r.render_lookups ?? [],
    createdAt: r.created_at.toISOString(),
  };
}

// created_at has microsecond precision, which a JS Date cannot hold: the cursor carries Postgres's own text.
export function encodeCursor(createdAt: string, id: string): string {
  return Buffer.from(`${createdAt}|${id}`).toString("base64url");
}

export function decodeCursor(cursor: string): { createdAt: string; id: string } {
  const [createdAt, id] = Buffer.from(cursor, "base64url").toString("utf8").split("|");
  if (createdAt === undefined || id === undefined || !/^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(\.\d{1,6})?[+-]\d\d(:\d\d)?$/.test(createdAt) || !/^[0-9a-f-]{36}$/.test(id)) {
    throw new BadCursorError();
  }
  return { createdAt, id };
}

/**
 * Delivering one of the hub's own outcome events (webhook.delivered or webhook.deadlettered,
 * when the hub consumes passage.hub.webhook and a subscription matches it) writes no outcome
 * event of its own: otherwise every delivery would feed the next one without end.
 */
function isOutcomeEvent(type: string): boolean {
  return type.startsWith("dev.passage.hub.webhook.");
}

/** The subscription event payload: identity and shape only; the URL, secret and credentials never leave the store. */
function subscriptionData(s: SubscriptionRecord): Record<string, unknown> {
  return { subscription_id: s.id, kind: s.kind, name: s.name, patterns: s.patterns, filter: s.filter, status: s.status, paused_reason: s.pausedReason, version: s.version };
}

/** What a subscription's CESQL filter sees of an event: its context attributes and its data. */
export function filterEventOf(tenantId: string, e: HubEvent): FilterEvent {
  return {
    attributes: {
      id: e.eventId, type: e.type, subject: e.aggregateId, time: new Date(e.occurredAt * 1000).toISOString(), tenantid: tenantId,
      source: e.source, actor: e.actor, legalentityid: e.legalEntityId, passagesequence: e.sequence,
    },
    data: e.data,
  };
}

/**
 * Whether a stored filter holds for the event. A stored filter that no longer compiles (validated on write, so only
 * after the grammar narrows) matches nothing for that one subscription rather than failing the event for every
 * subscription of the tenant.
 */
function filterHolds(source: string, tenantId: string, event: HubEvent): boolean {
  let compiled;
  try {
    compiled = filterFor(source);
  } catch (err) {
    if (err instanceof CesqlSyntaxError) {
      return false;
    }
    throw err;
  }
  return compiled.matches(filterEventOf(tenantId, event));
}

export interface NewSubscription {
  name: string;
  url: string;
  /** The webhook signing secret; empty for a rest-push subscription. */
  secret: string;
  /** Set for a rest-push subscription. */
  rest?: { config: RestConfig; credentials: RestCredentials };
  patterns: string[];
  /** A CESQL expression, already validated by the caller (validateFilter). */
  filter?: string | null;
  createdBy: string | null;
  idempotencyKey: string | null;
}

export interface SubscriptionPatch {
  version: number;
  name?: string;
  url?: string;
  patterns?: string[];
  /** A CESQL expression, already validated; null removes the filter. */
  filter?: string | null;
  /** rest-push only: replaces the whole configuration (and with it the credentials). */
  rest?: { config: RestConfig; credentials?: RestCredentials };
}

/** A webhook-only change was asked of a rest-push subscription or the reverse. */
export class WrongKindError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "WrongKindError";
  }
}

const TARGET_COLUMNS =
  "id, kind, url, secret_sealed, previous_secret_sealed, extract(epoch from previous_secret_expires_at)::float8 as previous_expires, rest_config, credentials_sealed, status";

interface TargetRow {
  id: string;
  kind: SubscriptionKind;
  url: string;
  secret_sealed: Buffer | null;
  previous_secret_sealed: Buffer | null;
  previous_expires: number | null;
  rest_config: RestConfig | null;
  credentials_sealed: Buffer | null;
  status: string;
}

function toTarget(r: TargetRow): SubscriptionTarget {
  return {
    id: r.id, kind: r.kind, url: r.url, sealedSecret: r.secret_sealed, sealedPreviousSecret: r.previous_secret_sealed, previousSecretExpiresAt: r.previous_expires,
    restConfig: r.rest_config, sealedCredentials: r.credentials_sealed,
  };
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
    if (target.sealedSecret === null) {
      throw new WrongKindError("a rest-push subscription has no signing secret");
    }
    return this.#keys.open(tenantId, SECRET_FIELD, target.sealedSecret).toString("utf8");
  }

  /**
   * The secret before the last rotation while its grace period runs at `nowSeconds`, else null. A previous
   * secret that does not open (its key was removed) is skipped: the current secret still signs.
   */
  openPreviousSecret(tenantId: string, target: SubscriptionTarget, nowSeconds: number): string | null {
    if (this.#keys === null || target.sealedPreviousSecret === null || target.previousSecretExpiresAt === null || target.previousSecretExpiresAt <= nowSeconds) {
      return null;
    }
    try {
      return this.#keys.open(tenantId, SECRET_FIELD, target.sealedPreviousSecret).toString("utf8");
    } catch {
      return null;
    }
  }

  /** Opens a rest-push subscription's credentials for one send; never log or return the result. */
  openRest(tenantId: string, target: SubscriptionTarget): RestTarget {
    if (this.#keys === null) {
      throw new FieldKeysMissingError();
    }
    if (target.restConfig === null || target.sealedCredentials === null) {
      throw new WrongKindError("not a rest-push subscription");
    }
    const credentials = JSON.parse(this.#keys.open(tenantId, CREDENTIALS_FIELD, target.sealedCredentials).toString("utf8")) as RestCredentials;
    return { tenantId, subscriptionId: target.id, urlTemplate: target.url, config: target.restConfig, credentials };
  }

  /** The target for the subscription (credentials still sealed); null when it does not exist. */
  async getTarget(tenantId: string, id: string): Promise<SubscriptionTarget | null> {
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query<TargetRow>(`select ${TARGET_COLUMNS} from hub_subscription where id = $1`, [id]);
      return res.rows[0] === undefined ? null : toTarget(res.rows[0]);
    });
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
    const sealed = input.rest === undefined ? this.#keys.seal(tenantId, SECRET_FIELD, Buffer.from(input.secret, "utf8")) : null;
    const credentials =
      input.rest === undefined ? null : this.#keys.seal(tenantId, CREDENTIALS_FIELD, Buffer.from(JSON.stringify(input.rest.credentials), "utf8"));
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query<SubRow>(
        `insert into hub_subscription (tenant_id, id, kind, name, url, secret_sealed, rest_config, credentials_sealed, patterns, created_by, idempotency_key, event_filter)
         values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
         on conflict (tenant_id, idempotency_key) do nothing
         returning ${COLUMNS_SUB}`,
        [tenantId, randomUUID(), input.rest === undefined ? "webhook" : "rest-push", input.name, input.url, sealed,
         input.rest === undefined ? null : JSON.stringify(input.rest.config), credentials, input.patterns, input.createdBy, input.idempotencyKey, input.filter ?? null],
      );
      if (res.rows[0] !== undefined) {
        const subscription = toSubscription(res.rows[0]);
        await enqueue(c, tenantId, {
          type: "dev.passage.hub.subscription.created.v1", aggregateId: subscription.id,
          actor: input.createdBy, data: subscriptionData(subscription),
        });
        return { subscription, created: true };
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
  async patchSubscription(tenantId: string, id: string, patch: SubscriptionPatch, actor: string | null = null): Promise<SubscriptionRecord | null> {
    if (patch.rest?.credentials !== undefined && this.#keys === null) {
      throw new FieldKeysMissingError();
    }
    const credentials =
      patch.rest?.credentials === undefined ? null : (this.#keys as FieldKeys).seal(tenantId, CREDENTIALS_FIELD, Buffer.from(JSON.stringify(patch.rest.credentials), "utf8"));
    return this.withTenant(tenantId, async (c) => {
      const kind = await c.query<{ kind: SubscriptionKind }>("select kind from hub_subscription where id = $1", [id]);
      if (kind.rows[0] === undefined) {
        return null;
      }
      if (patch.rest !== undefined && kind.rows[0].kind !== "rest-push") {
        throw new WrongKindError("only a rest-push subscription has rest settings");
      }
      const res = await c.query<SubRow>(
        `update hub_subscription
            set name = coalesce($3, name), url = coalesce($4, url), patterns = coalesce($5, patterns),
                rest_config = coalesce($6::jsonb, rest_config), credentials_sealed = coalesce($7, credentials_sealed),
                event_filter = case when $8::boolean then $9::text else event_filter end,
                version = version + 1, updated_at = now()
          where id = $1 and version = $2
          returning ${COLUMNS_SUB}`,
        [id, patch.version, patch.name ?? null, patch.url ?? null, patch.patterns ?? null,
         patch.rest === undefined ? null : JSON.stringify(patch.rest.config), credentials,
         patch.filter !== undefined, patch.filter ?? null],
      );
      if (res.rows[0] !== undefined) {
        const subscription = toSubscription(res.rows[0]);
        await enqueue(c, tenantId, { type: "dev.passage.hub.subscription.updated.v1", aggregateId: id, actor, data: subscriptionData(subscription) });
        return subscription;
      }
      throw new VersionConflictError();
    });
  }

  /**
   * Replaces a webhook subscription's signing secret. The old secret keeps signing (next to the new one) for
   * `graceSeconds` (0 ends it at once), so the receiver can switch without dropping events. Optimistic on
   * `version`: VersionConflictError when stale, null when the subscription does not exist, WrongKindError for
   * rest-push. The caller shows the new secret once; the store never returns it.
   */
  async rotateSecret(
    tenantId: string, id: string, input: { version: number; secret: string; graceSeconds: number }, actor: string | null = null,
  ): Promise<SubscriptionRecord | null> {
    if (this.#keys === null) {
      throw new FieldKeysMissingError();
    }
    const sealed = this.#keys.seal(tenantId, SECRET_FIELD, Buffer.from(input.secret, "utf8"));
    return this.withTenant(tenantId, async (c) => {
      const kind = await c.query<{ kind: SubscriptionKind }>("select kind from hub_subscription where id = $1", [id]);
      if (kind.rows[0] === undefined) {
        return null;
      }
      if (kind.rows[0].kind !== "webhook") {
        throw new WrongKindError("only a webhook subscription has a signing secret");
      }
      const res = await c.query<SubRow>(
        `update hub_subscription
            set previous_secret_sealed = case when $4::integer > 0 then secret_sealed else null end,
                previous_secret_expires_at = case when $4::integer > 0 then now() + make_interval(secs => $4::integer) else null end,
                secret_sealed = $3, secret_rotated_at = now(), version = version + 1, updated_at = now()
          where id = $1 and version = $2
          returning ${COLUMNS_SUB}`,
        [id, input.version, sealed, input.graceSeconds],
      );
      if (res.rows[0] === undefined) {
        throw new VersionConflictError();
      }
      const subscription = toSubscription(res.rows[0]);
      await enqueue(c, tenantId, {
        type: "dev.passage.hub.subscription.secretrotated.v1", aggregateId: id, actor,
        data: { ...subscriptionData(subscription), secret_rotated_at: subscription.secretRotatedAt, previous_secret_expires_at: subscription.previousSecretExpiresAt },
      });
      return subscription;
    });
  }

  /**
   * One re-seal pass (FR-35.9, master key rotation): every sealed value (signing secret, previous secret,
   * rest-push credentials) of up to `limit` subscriptions that is sealed under a key other than the current one is
   * opened with its own key and sealed again under the current key, and a previous secret past its grace period is
   * removed. Each subscription is its own tenant transaction holding the row lock, so a concurrent edit or rotation
   * waits and no value is lost; the version does not change (nothing a client can see changed). A value that
   * cannot be opened (its key was dropped too early) is counted as failed and left as it is.
   */
  async resealDue(limit: number, now: Date = new Date()): Promise<{ resealed: number; expired: number; failed: number }> {
    if (this.#keys === null) {
      throw new FieldKeysMissingError();
    }
    const keys = this.#keys;
    const due = await this.pool.query<{ tenant_id: string; subscription_id: string }>(
      "select tenant_id, subscription_id from hub_reseal_due($1, $2, $3)",
      [keys.currentId, now, limit],
    );
    const out = { resealed: 0, expired: 0, failed: 0 };
    for (const d of due.rows) {
      await this.withTenant(d.tenant_id, async (c) => {
        const res = await c.query<{ secret_sealed: Buffer | null; previous_secret_sealed: Buffer | null; previous_secret_expires_at: Date | null; credentials_sealed: Buffer | null }>(
          "select secret_sealed, previous_secret_sealed, previous_secret_expires_at, credentials_sealed from hub_subscription where id = $1 for update",
          [d.subscription_id],
        );
        const row = res.rows[0];
        if (row === undefined) {
          return;
        }
        const again = (field: string, value: Buffer | null): Buffer | null => {
          if (value === null || envelopeKeyId(value) === keys.currentId) {
            return value;
          }
          return keys.seal(d.tenant_id, field, keys.open(d.tenant_id, field, value));
        };
        try {
          const expired = row.previous_secret_sealed !== null && row.previous_secret_expires_at !== null && row.previous_secret_expires_at <= now;
          const previous = expired ? null : again(SECRET_FIELD, row.previous_secret_sealed);
          const secret = again(SECRET_FIELD, row.secret_sealed);
          const credentials = again(CREDENTIALS_FIELD, row.credentials_sealed);
          await c.query(
            `update hub_subscription set secret_sealed = $2, previous_secret_sealed = $3, credentials_sealed = $4,
                    previous_secret_expires_at = case when $3::bytea is null then null else previous_secret_expires_at end
              where id = $1`,
            [d.subscription_id, secret, previous, credentials],
          );
          if (expired) out.expired += 1;
          else out.resealed += 1;
        } catch (err) {
          if (!(err instanceof OpenError)) {
            throw err;
          }
          out.failed += 1;
        }
      });
    }
    return out;
  }

  /** Deletes up to `limit` per-minute usage rows older than `before`, across tenants (hub_purge_client_usage); returns how many. */
  async purgeClientUsage(before: Date, limit: number): Promise<number> {
    const res = await this.pool.query<{ n: string }>("select hub_purge_client_usage($1, $2) as n", [before, limit]);
    return Number(res.rows[0]?.n ?? 0);
  }

  async deleteSubscription(tenantId: string, id: string, actor: string | null = null): Promise<boolean> {
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query<SubRow>(`delete from hub_subscription where id = $1 returning ${COLUMNS_SUB}`, [id]);
      if (res.rows[0] === undefined) {
        return false;
      }
      await enqueue(c, tenantId, { type: "dev.passage.hub.subscription.deleted.v1", aggregateId: id, actor, data: subscriptionData(toSubscription(res.rows[0])) });
      return true;
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
                next_retry_at, replay, dead_letter_code, response_excerpt, legacy_key, line_keys, render_lookups, created_at, created_at::text as cursor_ts
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
  async targets(client: pg.PoolClient): Promise<{ id: string; kind: SubscriptionKind; patterns: string[]; filter: string | null }[]> {
    const res = await client.query<{ id: string; kind: SubscriptionKind; patterns: string[]; filter: string | null }>(
      "select id, kind, patterns, event_filter as filter from hub_subscription",
    );
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
        if (sub.filter !== null && !filterHolds(sub.filter, tenantId, event)) {
          continue;
        }
        // Echo suppression: a rest-push subscription never sends the legacy system what the legacy system's own
        // import wrote into Passage.
        if (sub.kind === "rest-push" && event.actor === ECHO_ACTOR) {
          continue;
        }
        const res = await c.query(
          `insert into hub_delivery (tenant_id, id, subscription_id, event_id, event_type, aggregate_id, sequence, occurred_at, data, next_retry_at, event_legal_entity)
           values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
           on conflict (tenant_id, subscription_id, event_id) do nothing`,
          [tenantId, randomUUID(), sub.id, event.eventId, event.type, event.aggregateId, event.sequence, event.occurredAt, JSON.stringify(event.data ?? null), now, event.legalEntityId ?? null],
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
      const sub = await c.query<TargetRow>(`select ${TARGET_COLUMNS} from hub_subscription where id = $1`, [subscriptionId]);
      const row = sub.rows[0];
      if (row === undefined) {
        return null;
      }
      const pending = await c.query<{
        event_id: string; event_type: string; aggregate_id: string; sequence: string; occurred_at: number; data: unknown;
        retries_done: number; next_retry_at: number; replay: boolean; event_legal_entity: string | null;
      }>(
        `select event_id, event_type, aggregate_id, sequence, occurred_at, data, retries_done, next_retry_at, replay, event_legal_entity
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
          event: {
            eventId: p.event_id, type: p.event_type, aggregateId: p.aggregate_id, sequence: Number(p.sequence), occurredAt: p.occurred_at, data: p.data,
            ...(p.event_legal_entity === null ? {} : { legalEntityId: p.event_legal_entity }),
          },
          retriesDone: p.retries_done,
          nextRetryAt: p.next_retry_at,
          replay: p.replay,
        });
      }
      return { store, target: toTarget(row) };
    });
  }

  /**
   * Puts one dead-lettered delivery back in the pending set as a replay, due now, with a fresh retry schedule.
   * "missing" when no such delivery, "not_dead" when it is not dead-lettered (pending or delivered),
   * "not_replayable" for HUB-0118: the receiver already holds the record and the event's legacy key is fixed in the
   * migration service, so a replay could only push one more copy and dead-letter again.
   */
  async replayDelivery(tenantId: string, deliveryId: string, now: number): Promise<"replayed" | "missing" | "not_dead" | "not_replayable"> {
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query(
        `update hub_delivery
            set status = 'pending', replay = true, retries_done = 0, next_retry_at = $2, attempts = 0,
                dead_letter_code = null, response_excerpt = null, updated_at = now()
          where id = $1 and status = 'dead_letter' and dead_letter_code is distinct from '${NOT_REPLAYABLE}'`,
        [deliveryId, now],
      );
      if ((res.rowCount ?? 0) > 0) {
        return "replayed";
      }
      const exists = await c.query<{ status: string; dead_letter_code: string | null }>("select status, dead_letter_code from hub_delivery where id = $1", [deliveryId]);
      const row = exists.rows[0];
      if (row === undefined) return "missing";
      return row.status === "dead_letter" && row.dead_letter_code === NOT_REPLAYABLE ? "not_replayable" : "not_dead";
    });
  }

  /** Clears a 410 pause (HUB-0114); null when the subscription does not exist. Resuming an active subscription changes nothing. */
  async resumeSubscription(tenantId: string, id: string, actor: string | null = null): Promise<SubscriptionRecord | null> {
    return this.withTenant(tenantId, async (c) => {
      const resumed = await c.query<SubRow>(
        `update hub_subscription set status = 'active', paused_reason = null, paused_at = null, version = version + 1, updated_at = now()
          where id = $1 and status = 'paused' returning ${COLUMNS_SUB}`,
        [id],
      );
      if (resumed.rows[0] !== undefined) {
        const subscription = toSubscription(resumed.rows[0]);
        await enqueue(c, tenantId, { type: "dev.passage.hub.subscription.resumed.v1", aggregateId: id, actor, data: subscriptionData(subscription) });
        return subscription;
      }
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
   * when more matching events remain after the cap. HUB-0118 dead letters are
   * skipped (see replayDelivery). Null when the subscription does not exist.
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
            where subscription_id = $1 and status <> 'pending' and dead_letter_code is distinct from '${NOT_REPLAYABLE}' and occurred_at >= $2
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
                    dead_letter_code = null, response_excerpt = null, updated_at = now()
              where id = any($1::uuid[]) and status <> 'pending' and dead_letter_code is distinct from '${NOT_REPLAYABLE}'`,
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
  | { kind: "attempt"; eventId: string; status: number | null; excerpt: string | null; legacyKey: string | null; lineKeys: string | null; lookups: string | null };

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
  recordAttempt(eventId: string, status: number | null, detail: { excerpt?: string; legacyKey?: string; lineKeys?: { index: number; legacyKey: string }[]; lookups?: { name: string; table: string; version: number }[] } = {}): void {
    this.#ops.push({
      kind: "attempt", eventId, status, excerpt: detail.excerpt ?? null, legacyKey: detail.legacyKey ?? null,
      lineKeys: detail.lineKeys === undefined ? null : JSON.stringify(detail.lineKeys),
      lookups: detail.lookups === undefined ? null : JSON.stringify(detail.lookups),
    });
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
      case "remove": {
        const done = await c.query<{ attempts: number; event_type: string }>(
          "update hub_delivery set status = 'delivered', updated_at = now() where subscription_id = $1 and event_id = $2 and status = 'pending' returning attempts, event_type",
          [sub, op.eventId],
        );
        const row = done.rows[0];
        if (row !== undefined && !isOutcomeEvent(row.event_type)) {
          await enqueue(c, this.#tenantId, {
            type: "dev.passage.hub.webhook.delivered.v1", aggregateId: sub, actor: null,
            data: { subscription_id: sub, event_id: op.eventId, attempts: row.attempts },
          });
        }
        return;
      }
      case "dead": {
        const dead = await c.query<{ event_type: string }>(
          `update hub_delivery set status = 'dead_letter', dead_letter_code = $3, attempts = $4, last_status = $5, updated_at = now()
            where subscription_id = $1 and event_id = $2 and status = 'pending' returning event_type`,
          [sub, op.letter.event.eventId, op.letter.code, op.letter.attempts, op.letter.lastStatus],
        );
        const row = dead.rows[0];
        if (row !== undefined && !isOutcomeEvent(row.event_type)) {
          await enqueue(c, this.#tenantId, {
            type: "dev.passage.hub.webhook.deadlettered.v1", aggregateId: sub, actor: null,
            data: { subscription_id: sub, event_id: op.letter.event.eventId, attempts: op.letter.attempts, code: op.letter.code, last_status: op.letter.lastStatus },
          });
        }
        return;
      }
      case "paused": {
        const moved = await c.query<SubRow>(
          `update hub_subscription
              set status = $2, paused_reason = $3, paused_at = $4, version = version + 1, updated_at = now()
            where id = $1 and status <> $2 returning ${COLUMNS_SUB}`,
          [sub, op.paused ? "paused" : "active", op.paused ? "HUB-0114" : null, op.paused ? new Date() : null],
        );
        if (moved.rows[0] !== undefined) {
          await enqueue(c, this.#tenantId, {
            type: op.paused ? "dev.passage.hub.subscription.paused.v1" : "dev.passage.hub.subscription.resumed.v1",
            aggregateId: sub, actor: null, data: subscriptionData(toSubscription(moved.rows[0])),
          });
        }
        return;
      }
      case "attempt":
        await c.query(
          `with d as (
             update hub_delivery set attempts = attempts + 1, last_status = $3, response_excerpt = $6,
                    legacy_key = coalesce($7, legacy_key), line_keys = coalesce($8::jsonb, line_keys),
                    render_lookups = coalesce($9::jsonb, render_lookups), updated_at = now()
              where subscription_id = $1 and event_id = $2 returning id, attempts)
           insert into hub_attempt (tenant_id, id, delivery_id, attempt_no, http_status)
           select $4, $5, d.id, d.attempts, $3 from d`,
          [sub, op.eventId, op.status, this.#tenantId, randomUUID(), op.excerpt, op.legacyKey, op.lineKeys, op.lookups],
        );
        return;
    }
  }
}
