// API clients (FR-35.8, BR-35.8): the hub's registry of Keycloak clients with the
// duties they may use (scopes), a per-client token bucket quota and per-minute
// usage. The client secret stays in Keycloak; the edge asks the hub to spend
// quota (`consumeQuota`) for the client id in the verified token and answers 429
// with the returned Retry-After when the bucket is empty (HUB-0113).
//
// Money is not involved; counts are integers. Writes carry optimistic versions
// and idempotency keys, and each change writes its event to the outbox in the
// same transaction.

import { randomUUID } from "node:crypto";

import { enqueue } from "./events.ts";
import { decodeCursor, encodeCursor, type HubStore, type Page, VersionConflictError } from "./pgStore.ts";
import { DEFAULT_BURST, DEFAULT_QUOTA_PER_MIN, exceedsBurst, take, validateBucket } from "./quota.ts";

export type ClientStatus = "active" | "revoked";

export interface ApiClientRecord {
  id: string;
  clientId: string;
  name: string;
  scopes: string[];
  quotaPerMin: number;
  burst: number;
  sandboxTenantId: string | null;
  ownerContact: string;
  status: ClientStatus;
  revokedAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface NewApiClient {
  clientId: string;
  name: string;
  scopes: string[];
  quotaPerMin: number;
  burst: number;
  sandboxTenantId: string | null;
  ownerContact: string;
  createdBy: string | null;
  idempotencyKey: string | null;
}

export interface ApiClientPatch {
  version: number;
  name?: string;
  scopes?: string[];
  quotaPerMin?: number;
  burst?: number;
  sandboxTenantId?: string | null;
  ownerContact?: string;
}

export type ConsumeResult =
  | { outcome: "allowed"; remaining: number; limitPerMin: number; burst: number }
  | { outcome: "throttled"; retryAfterSeconds: number; limitPerMin: number; burst: number }
  | { outcome: "too_costly"; burst: number }
  | { outcome: "revoked" }
  | { outcome: "unknown" };

export interface UsageMinute {
  minute: string;
  allowed: number;
  throttled: number;
}

/** A client id is already registered in this tenant. */
export class ClientIdTakenError extends Error {
  constructor() {
    super("an API client with this client id is already registered");
    this.name = "ClientIdTakenError";
  }
}

/** The change is not allowed on a revoked client. */
export class ClientRevokedError extends Error {
  constructor() {
    super("the API client is revoked");
    this.name = "ClientRevokedError";
  }
}

const COLUMNS = "id, client_id, name, scopes, quota_per_min, burst, sandbox_tenant_id, owner_contact, status, revoked_at, version, created_at, updated_at, created_at::text as cursor_ts";

interface ClientRow {
  id: string;
  client_id: string;
  name: string;
  scopes: string[];
  quota_per_min: number;
  burst: number;
  sandbox_tenant_id: string | null;
  owner_contact: string;
  status: ClientStatus;
  revoked_at: Date | null;
  version: number;
  created_at: Date;
  updated_at: Date;
  cursor_ts: string;
}

function toClient(r: ClientRow): ApiClientRecord {
  return {
    id: r.id,
    clientId: r.client_id,
    name: r.name,
    scopes: r.scopes,
    quotaPerMin: r.quota_per_min,
    burst: r.burst,
    sandboxTenantId: r.sandbox_tenant_id,
    ownerContact: r.owner_contact,
    status: r.status,
    revokedAt: r.revoked_at === null ? null : r.revoked_at.toISOString(),
    version: r.version,
    createdAt: r.created_at.toISOString(),
    updatedAt: r.updated_at.toISOString(),
  };
}

function eventData(r: ApiClientRecord): Record<string, unknown> {
  return {
    api_client_id: r.id,
    client_id: r.clientId,
    name: r.name,
    scopes: r.scopes,
    quota_per_min: r.quotaPerMin,
    burst: r.burst,
    sandbox_tenant_id: r.sandboxTenantId,
    status: r.status,
    version: r.version,
  };
}

const CLIENT_ID = /^[a-z0-9][a-z0-9._-]{2,63}$/;
// Duty names are dotted lower-case (sales.order.read; policies/authz/README.md). Clients registered
// before #210 may still hold scopes in the old PascalCase, SCREAMING_CASE or prose styles, so a
// scope stays any duty-shaped name: letters, digits, space, '.', '_' or '-', starting with a letter
// and not ending in a space. Commas are refused because the gateway joins duties with commas in
// X-Passage-Duties.
const SCOPE = /^[A-Za-z](?:[A-Za-z0-9 ._-]{0,98}[A-Za-z0-9._-])?$/;
const UNIQUE_VIOLATION = "23505";

/** Throws RangeError with a message fit for a 400 when the client id does not fit the Keycloak id rule. */
export function validateClientId(id: string): void {
  if (!CLIENT_ID.test(id)) {
    throw new RangeError("clientId must be 3 to 64 characters of a-z, 0-9, '.', '_' or '-', starting with a letter or digit");
  }
}

/** Throws RangeError when a scope is not a duty name (at most 100 characters) or the list is not 1..50 scopes. */
export function validateScopes(scopes: readonly string[]): void {
  if (scopes.length < 1 || scopes.length > 50) {
    throw new RangeError("scopes must list 1 to 50 duties");
  }
  for (const s of scopes) {
    if (!SCOPE.test(s)) {
      throw new RangeError(`scope ${JSON.stringify(s)} is not a duty name such as sales.order.read or receiving.vendor-invoice.post`);
    }
  }
}

export function validateOwner(contact: string): void {
  if (contact.length < 3 || contact.length > 320 || !/^[^\s@]+@[^\s@]+$/.test(contact)) {
    throw new RangeError("ownerContact must be an e-mail address");
  }
}

export const quotaDefaults = { quotaPerMin: DEFAULT_QUOTA_PER_MIN, burst: DEFAULT_BURST } as const;

export class ClientStore {
  readonly #db: HubStore;

  constructor(db: HubStore) {
    this.#db = db;
  }

  /** Registers a client; the same idempotency key returns the first one created (`created` false). A taken client id is ClientIdTakenError. */
  async create(tenantId: string, input: NewApiClient): Promise<{ client: ApiClientRecord; created: boolean }> {
    validateBucket({ ratePerMinute: input.quotaPerMin, burst: input.burst });
    try {
      return await this.#db.withTenant(tenantId, async (c) => {
        const res = await c.query<ClientRow>(
          `insert into hub_api_client (tenant_id, id, client_id, name, scopes, quota_per_min, burst, sandbox_tenant_id, owner_contact, created_by, idempotency_key)
           values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
           on conflict (tenant_id, idempotency_key) do nothing
           returning ${COLUMNS}`,
          [tenantId, randomUUID(), input.clientId, input.name, input.scopes, input.quotaPerMin, input.burst, input.sandboxTenantId, input.ownerContact, input.createdBy, input.idempotencyKey],
        );
        if (res.rows[0] === undefined) {
          const existing = await c.query<ClientRow>(`select ${COLUMNS} from hub_api_client where idempotency_key = $1`, [input.idempotencyKey]);
          return { client: toClient(existing.rows[0] as ClientRow), created: false };
        }
        const client = toClient(res.rows[0]);
        await enqueue(c, tenantId, { type: "dev.passage.hub.apiclient.registered.v1", aggregateId: client.id, actor: input.createdBy, data: eventData(client) });
        return { client, created: true };
      });
    } catch (err) {
      if ((err as { code?: string }).code === UNIQUE_VIOLATION) {
        throw new ClientIdTakenError();
      }
      throw err;
    }
  }

  async list(tenantId: string, limit: number, cursor: string | null): Promise<Page<ApiClientRecord>> {
    const after = cursor === null ? null : decodeCursor(cursor);
    return this.#db.withTenant(tenantId, async (c) => {
      const res = await c.query<ClientRow>(
        `select ${COLUMNS} from hub_api_client
          where ($1::timestamptz is null or (created_at, id) < ($1::timestamptz, $2::uuid))
          order by created_at desc, id desc limit $3`,
        [after?.createdAt ?? null, after?.id ?? null, limit + 1],
      );
      const rows = res.rows.slice(0, limit);
      const last = rows[rows.length - 1];
      return { items: rows.map(toClient), nextCursor: res.rows.length > limit && last !== undefined ? encodeCursor(last.cursor_ts, last.id) : null };
    });
  }

  async get(tenantId: string, id: string): Promise<ApiClientRecord | null> {
    return this.#db.withTenant(tenantId, async (c) => {
      const res = await c.query<ClientRow>(`select ${COLUMNS} from hub_api_client where id = $1`, [id]);
      return res.rows[0] === undefined ? null : toClient(res.rows[0]);
    });
  }

  /** Optimistic update of a live client: VersionConflictError when stale, ClientRevokedError when revoked, null when missing. */
  async patch(tenantId: string, id: string, patch: ApiClientPatch, actor: string | null): Promise<ApiClientRecord | null> {
    return this.#db.withTenant(tenantId, async (c) => {
      const cur = await c.query<ClientRow>(`select ${COLUMNS} from hub_api_client where id = $1 for update`, [id]);
      const row = cur.rows[0];
      if (row === undefined) return null;
      if (row.status === "revoked") throw new ClientRevokedError();
      if (row.version !== patch.version) throw new VersionConflictError();
      const quotaPerMin = patch.quotaPerMin ?? row.quota_per_min;
      const burst = patch.burst ?? row.burst;
      validateBucket({ ratePerMinute: quotaPerMin, burst });
      const sandbox = patch.sandboxTenantId === undefined ? row.sandbox_tenant_id : patch.sandboxTenantId;
      const res = await c.query<ClientRow>(
        `update hub_api_client
            set name = $2, scopes = $3, quota_per_min = $4, burst = $5, sandbox_tenant_id = $6, owner_contact = $7,
                version = version + 1, updated_at = now()
          where id = $1 returning ${COLUMNS}`,
        [id, patch.name ?? row.name, patch.scopes ?? row.scopes, quotaPerMin, burst, sandbox, patch.ownerContact ?? row.owner_contact],
      );
      const client = toClient(res.rows[0] as ClientRow);
      await enqueue(c, tenantId, { type: "dev.passage.hub.apiclient.updated.v1", aggregateId: id, actor, data: eventData(client) });
      return client;
    });
  }

  /** Revokes a client (idempotent: a revoked client stays as it is and writes no second event). Null when missing. */
  async revoke(tenantId: string, id: string, actor: string | null): Promise<ApiClientRecord | null> {
    return this.#db.withTenant(tenantId, async (c) => {
      const res = await c.query<ClientRow>(
        `update hub_api_client set status = 'revoked', revoked_at = now(), version = version + 1, updated_at = now()
          where id = $1 and status = 'active' returning ${COLUMNS}`,
        [id],
      );
      if (res.rows[0] !== undefined) {
        const client = toClient(res.rows[0]);
        await enqueue(c, tenantId, { type: "dev.passage.hub.apiclient.revoked.v1", aggregateId: id, actor, data: eventData(client) });
        return client;
      }
      const existing = await c.query<ClientRow>(`select ${COLUMNS} from hub_api_client where id = $1`, [id]);
      return existing.rows[0] === undefined ? null : toClient(existing.rows[0]);
    });
  }

  /**
   * Spends `cost` units of the client's token bucket (BR-35.8). The bucket row is
   * locked for the decision, so concurrent requests of one client are serialized
   * and never overspend. Usage for the minute counts admitted and refused units.
   */
  async consume(tenantId: string, clientId: string, cost: number, nowMs: number): Promise<ConsumeResult> {
    return this.#db.withTenant(tenantId, async (c) => {
      const found = await c.query<{ id: string; quota_per_min: number; burst: number; status: ClientStatus }>(
        "select id, quota_per_min, burst, status from hub_api_client where client_id = $1",
        [clientId],
      );
      const client = found.rows[0];
      if (client === undefined) return { outcome: "unknown" };
      if (client.status === "revoked") return { outcome: "revoked" };
      const cfg = { ratePerMinute: client.quota_per_min, burst: client.burst };
      if (exceedsBurst(cfg, cost)) return { outcome: "too_costly", burst: cfg.burst };
      await c.query(
        "insert into hub_client_bucket (tenant_id, api_client_id, tokens, updated_at_ms) values ($1, $2, $3, $4) on conflict do nothing",
        [tenantId, client.id, cfg.burst, nowMs],
      );
      const locked = await c.query<{ tokens: number; updated_at_ms: number }>(
        "select tokens, updated_at_ms from hub_client_bucket where api_client_id = $1 for update",
        [client.id],
      );
      const state = locked.rows[0] as { tokens: number; updated_at_ms: number };
      const out = take(cfg, { tokens: state.tokens, updatedAtMs: state.updated_at_ms }, nowMs, cost);
      await c.query("update hub_client_bucket set tokens = $2, updated_at_ms = $3 where api_client_id = $1", [client.id, out.state.tokens, out.state.updatedAtMs]);
      await c.query(
        `insert into hub_client_usage (tenant_id, api_client_id, minute, allowed, throttled)
         values ($1, $2, date_trunc('minute', to_timestamp($3::double precision / 1000)), $4, $5)
         on conflict (tenant_id, api_client_id, minute)
         do update set allowed = hub_client_usage.allowed + excluded.allowed, throttled = hub_client_usage.throttled + excluded.throttled`,
        [tenantId, client.id, nowMs, out.allowed ? cost : 0, out.allowed ? 0 : cost],
      );
      return out.allowed
        ? { outcome: "allowed", remaining: out.remaining, limitPerMin: cfg.ratePerMinute, burst: cfg.burst }
        : { outcome: "throttled", retryAfterSeconds: out.retryAfterSeconds, limitPerMin: cfg.ratePerMinute, burst: cfg.burst };
    });
  }

  /** Per-minute usage for the last `minutes` minutes ending at `nowMs`, oldest first; null when the client does not exist. */
  async usage(tenantId: string, id: string, minutes: number, nowMs: number): Promise<UsageMinute[] | null> {
    return this.#db.withTenant(tenantId, async (c) => {
      const exists = await c.query("select 1 from hub_api_client where id = $1", [id]);
      if (exists.rowCount === 0) return null;
      const res = await c.query<{ minute: Date; allowed: string; throttled: string }>(
        `select minute, allowed, throttled from hub_client_usage
          where api_client_id = $1 and minute > to_timestamp($2::double precision / 1000) - make_interval(mins => $3)
          order by minute`,
        [id, nowMs, minutes],
      );
      return res.rows.map((r) => ({ minute: r.minute.toISOString(), allowed: Number(r.allowed), throttled: Number(r.throttled) }));
    });
  }
}
