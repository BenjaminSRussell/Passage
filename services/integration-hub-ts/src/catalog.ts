// The API and event catalog (FR-35.8, §6.35.10): the hub's registry of published
// OpenAPI and AsyncAPI documents that the developer portal browses. A published
// version is immutable; a newer version of the same contract must have a higher
// semantic version within its major line (MAJOR.MINOR.PATCH; a new major line must
// be higher than every published major, so v1 can still get additive versions while
// v2 runs beside it for the deprecation overlap); a deprecation announces a sunset
// date at least `api_deprecation_months` (default 12, never below 12) away and is
// inherited by later versions of the same major line; only a deprecated entry past
// its sunset can be retired, and a retired major line takes no new versions.
// Nothing is deleted. Each change writes its event to the outbox in the same
// transaction.

import { createHash, randomUUID } from "node:crypto";

import { enqueue } from "./events.ts";
import { decodeCursor, encodeCursor, type HubStore, type Page, VersionConflictError } from "./pgStore.ts";

export type CatalogKind = "api" | "event";
export type Lifecycle = "active" | "deprecated" | "retired";
export const CATALOG_KINDS: readonly CatalogKind[] = ["api", "event"];
export const LIFECYCLES: readonly Lifecycle[] = ["active", "deprecated", "retired"];

/** The sunset notice floor in months (api_deprecation_months is "≥ 12", default 12, PRD §6.35.13). */
export const MIN_DEPRECATION_MONTHS = 12;
/** The largest document accepted, as serialized JSON. */
export const MAX_SPEC_BYTES = 2 * 1024 * 1024;
/**
 * The deepest nesting accepted. Postgres parses JSON recursively and fails with "stack
 * depth limit exceeded" (a 500) on a few hundred thousand levels, which fit in 2 MiB
 * as `[[[...]]]`; the deepest OpenAPI or AsyncAPI document in this repository is 18.
 */
export const MAX_SPEC_DEPTH = 64;
const MAX_OPERATIONS = 5000;

/** One operation of an API, or one message of an event contract. */
export interface CatalogOperation {
  /** operationId, or the message key; null when the document gives none. */
  id: string | null;
  /** HTTP method of an operation, or `event` for a message. */
  method: string;
  /** The path of an operation, or the CloudEvents type of a message. */
  path: string;
  summary: string | null;
  /** x-passage-duty of an operation; messages have none. */
  duty: string | null;
}

export interface CatalogEntry {
  id: string;
  kind: CatalogKind;
  name: string;
  specVersion: string;
  title: string;
  summary: string;
  ownerService: string;
  specFormat: "openapi" | "asyncapi";
  specBytes: number;
  operationCount: number;
  lifecycle: Lifecycle;
  deprecatedAt: string | null;
  sunsetAt: string | null;
  replacedBy: string | null;
  retiredAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface CatalogDetail extends CatalogEntry {
  operations: CatalogOperation[];
  /** Documentation gaps found in the operations (the doc gate's rules), empty when clean. */
  issues: string[];
}

export interface NewCatalogEntry {
  kind: CatalogKind;
  name: string;
  specVersion: string;
  title: string;
  summary: string;
  ownerService: string;
  spec: Record<string, unknown>;
  createdBy: string | null;
  idempotencyKey: string | null;
}

export interface CatalogPatch {
  version: number;
  title?: string;
  summary?: string;
  ownerService?: string;
}

export interface CatalogFilter {
  kind?: CatalogKind;
  lifecycle?: Lifecycle;
  name?: string;
  q?: string;
}

/** The document is not a usable OpenAPI 3.x or AsyncAPI 3.x document (a 400). */
export class SpecError extends RangeError {
  constructor(message: string) {
    super(message);
    this.name = "SpecError";
  }
}

/** The Idempotency-Key was used before for a different publish (a 422). */
export class IdempotencyKeyReuseError extends Error {
  constructor() {
    super("the Idempotency-Key was already used to publish a different kind, name, version or document");
    this.name = "IdempotencyKeyReuseError";
  }
}

/** The (kind, name, specVersion) is already published, a newer version of its major line exists, or the line is retired. */
export class CatalogVersionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "CatalogVersionError";
  }
}

/** The lifecycle does not allow the change (deprecate a non-active entry, retire before the sunset). */
export class CatalogStateError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "CatalogStateError";
  }
}

const NAME = /^[a-z0-9][a-z0-9._-]{1,99}$/;
const OWNER = /^[a-z0-9][a-z0-9._-]{1,63}$/;
const SEMVER = /^([0-9]{1,6})\.([0-9]{1,6})\.([0-9]{1,6})$/;
const HTTP_METHODS = ["get", "put", "post", "delete", "patch", "head", "options"] as const;
const UNIQUE_VIOLATION = "23505";

export function validateCatalogName(name: string): void {
  if (!NAME.test(name)) {
    throw new RangeError("name must be 2 to 100 characters of a-z, 0-9, '.', '_' or '-', starting with a letter or digit");
  }
}

export function validateOwnerService(owner: string): void {
  if (!OWNER.test(owner)) {
    throw new RangeError("ownerService must be 2 to 64 characters of a-z, 0-9, '.', '_' or '-', such as sales or integration-hub");
  }
}

/** Parses MAJOR.MINOR.PATCH; RangeError for anything else (no prerelease tags, so ordering is total). */
export function parseSemver(v: string): [number, number, number] {
  const m = SEMVER.exec(v);
  if (m === null) {
    throw new RangeError("specVersion must be MAJOR.MINOR.PATCH, such as 1.4.0");
  }
  return [Number(m[1]), Number(m[2]), Number(m[3])];
}

/** Throws SpecError when the value nests deeper than `max` objects or arrays (an iterative walk, no recursion). */
export function checkDepth(value: unknown, max: number = MAX_SPEC_DEPTH): void {
  const stack: [unknown, number][] = [[value, 1]];
  while (stack.length > 0) {
    const [v, depth] = stack.pop() as [unknown, number];
    if (typeof v !== "object" || v === null) continue;
    if (depth > max) {
      throw new SpecError(`spec nests deeper than ${max} levels`);
    }
    for (const child of Array.isArray(v) ? v : Object.values(v)) {
      if (typeof child === "object" && child !== null) stack.push([child, depth + 1]);
    }
  }
}

/** Negative when a < b, zero when equal, positive when a > b. */
export function compareSemver(a: string, b: string): number {
  const x = parseSemver(a);
  const y = parseSemver(b);
  return x[0] - y[0] || x[1] - y[1] || x[2] - y[2];
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function str(v: unknown): string | null {
  return typeof v === "string" && v !== "" ? v : null;
}

/**
 * Checks that `spec` is an OpenAPI 3.x document for kind api or an AsyncAPI 3.x document
 * for kind event, and derives its operations. Only the shape the portal needs is checked
 * (version field, info.title, paths or channels/messages); full schema validation is the
 * owning service's own gate.
 */
export function readSpec(kind: CatalogKind, spec: unknown): CatalogOperation[] {
  if (!isObject(spec)) {
    throw new SpecError("spec must be a JSON object");
  }
  checkDepth(spec);
  const size = Buffer.byteLength(JSON.stringify(spec));
  if (size > MAX_SPEC_BYTES) {
    throw new SpecError(`spec is larger than ${MAX_SPEC_BYTES} bytes`);
  }
  const info = spec["info"];
  if (!isObject(info) || str(info["title"]) === null || str(info["version"]) === null) {
    throw new SpecError("spec.info must carry a title and a version");
  }
  const ops: CatalogOperation[] = [];
  if (kind === "api") {
    const v = str(spec["openapi"]);
    if (v === null || !/^3\.[0-9]+(\.[0-9]+)?$/.test(v)) {
      throw new SpecError("an api entry needs an OpenAPI 3.x document (openapi: 3.x.y)");
    }
    const paths = spec["paths"];
    if (!isObject(paths) || Object.keys(paths).length === 0) {
      throw new SpecError("spec.paths must list at least one path");
    }
    for (const [path, item] of Object.entries(paths)) {
      if (!isObject(item)) continue;
      for (const m of HTTP_METHODS) {
        const op = item[m];
        if (!isObject(op)) continue;
        ops.push({ id: str(op["operationId"]), method: m.toUpperCase(), path, summary: str(op["summary"]), duty: str(op["x-passage-duty"]) });
      }
    }
    if (ops.length === 0) {
      throw new SpecError("spec.paths holds no operation");
    }
  } else {
    const v = str(spec["asyncapi"]);
    if (v === null || !/^3\.[0-9]+(\.[0-9]+)?$/.test(v)) {
      throw new SpecError("an event entry needs an AsyncAPI 3.x document (asyncapi: 3.x.y)");
    }
    const components = spec["components"];
    const messages = isObject(components) && isObject(components["messages"]) ? components["messages"] : {};
    for (const [key, m] of Object.entries(messages)) {
      if (!isObject(m)) continue;
      ops.push({ id: key, method: "event", path: str(m["name"]) ?? key, summary: str(m["summary"]) ?? str(m["title"]), duty: null });
    }
    if (ops.length === 0) {
      throw new SpecError("spec.components.messages must define at least one message");
    }
  }
  if (ops.length > MAX_OPERATIONS) {
    throw new SpecError(`spec has more than ${MAX_OPERATIONS} operations`);
  }
  return ops;
}

/** The documentation gaps the doc gate flags: an operation without operationId, summary or x-passage-duty; a message without a summary. */
export function docIssues(kind: CatalogKind, ops: readonly CatalogOperation[]): string[] {
  const out: string[] = [];
  for (const o of ops) {
    const what = kind === "api" ? `${o.method} ${o.path}` : o.path;
    if (kind === "api" && o.id === null) out.push(`${what}: no operationId`);
    if (o.summary === null) out.push(`${what}: no summary`);
    if (kind === "api" && o.duty === null) out.push(`${what}: no x-passage-duty`);
  }
  return out;
}

/** A version of the contract already in the catalog, as the version rule needs it. */
export interface PublishedVersion {
  specVersion: string;
  lifecycle: Lifecycle;
  deprecatedAt: Date | null;
  sunsetAt: Date | null;
  replacedBy: string | null;
}

/** The deprecation a new version takes over from its major line. */
export interface InheritedDeprecation {
  deprecatedAt: Date;
  sunsetAt: Date;
  replacedBy: string | null;
}

/**
 * Where `next` may go among the published versions of one contract (kind and name).
 * It must differ from every published version and be higher than the newest version of
 * its major line; the first version of a major line must be higher than every published
 * major. A major line whose newest version is retired takes no more versions. A version
 * added to a line whose newest version is deprecated inherits that deprecation (same
 * sunset and replacement), so the line's notice never disappears. CatalogVersionError
 * when the version does not fit; the inherited deprecation, or null.
 */
export function placeVersion(next: string, prior: readonly PublishedVersion[]): InheritedDeprecation | null {
  const major = parseSemver(next)[0];
  for (const p of prior) {
    if (compareSemver(next, p.specVersion) === 0) {
      throw new CatalogVersionError(`${next} is already published; published versions never change`);
    }
  }
  const line = prior.filter((p) => parseSemver(p.specVersion)[0] === major);
  if (line.length === 0) {
    const highest = prior.reduce<string | null>((h, p) => (h === null || compareSemver(p.specVersion, h) > 0 ? p.specVersion : h), null);
    if (highest !== null && parseSemver(highest)[0] > major) {
      throw new CatalogVersionError(`${highest} is already published; a new major version must be higher than ${parseSemver(highest)[0]}`);
    }
    return null;
  }
  const newest = line.reduce((h, p) => (compareSemver(p.specVersion, h.specVersion) > 0 ? p : h));
  if (compareSemver(next, newest.specVersion) < 0) {
    throw new CatalogVersionError(`${newest.specVersion} is already published; a new ${major}.x version must be higher than ${newest.specVersion}`);
  }
  if (newest.lifecycle === "retired") {
    throw new CatalogVersionError(`major version ${major} is retired (${newest.specVersion}); publish a new major version`);
  }
  if (newest.lifecycle === "deprecated" && newest.deprecatedAt !== null && newest.sunsetAt !== null) {
    return { deprecatedAt: newest.deprecatedAt, sunsetAt: newest.sunsetAt, replacedBy: newest.replacedBy };
  }
  return null;
}

/** `now` plus whole calendar months in UTC, clamped to the last day of a shorter month. */
export function addMonths(now: Date, months: number): Date {
  const d = new Date(now.getTime());
  const day = d.getUTCDate();
  d.setUTCDate(1);
  d.setUTCMonth(d.getUTCMonth() + months);
  const last = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 0)).getUTCDate();
  d.setUTCDate(Math.min(day, last));
  return d;
}

/** Throws RangeError unless `sunsetAt` is at least `months` months after `now` (BR: api_deprecation_months ≥ 12). */
export function checkSunset(sunsetAt: Date, now: Date, months: number): void {
  if (!Number.isFinite(sunsetAt.getTime())) {
    throw new RangeError("sunsetAt must be an RFC 3339 timestamp");
  }
  if (sunsetAt.getTime() < addMonths(now, months).getTime()) {
    throw new RangeError(`sunsetAt must be at least ${months} months from now (api_deprecation_months)`);
  }
}

const COLUMNS = `id, kind, name, spec_version, title, summary, owner_service, spec_format, spec_bytes, operation_count, lifecycle,
  deprecated_at, sunset_at, replaced_by, retired_at, version, created_at, updated_at, created_at::text as cursor_ts`;

interface Row {
  id: string;
  kind: CatalogKind;
  name: string;
  spec_version: string;
  title: string;
  summary: string;
  owner_service: string;
  spec_format: "openapi" | "asyncapi";
  spec_bytes: number;
  operation_count: number;
  lifecycle: Lifecycle;
  deprecated_at: Date | null;
  sunset_at: Date | null;
  replaced_by: string | null;
  retired_at: Date | null;
  version: number;
  created_at: Date;
  updated_at: Date;
  cursor_ts: string;
}

const iso = (d: Date | null): string | null => (d === null ? null : d.toISOString());

function toEntry(r: Row): CatalogEntry {
  return {
    id: r.id,
    kind: r.kind,
    name: r.name,
    specVersion: r.spec_version,
    title: r.title,
    summary: r.summary,
    ownerService: r.owner_service,
    specFormat: r.spec_format,
    specBytes: r.spec_bytes,
    operationCount: r.operation_count,
    lifecycle: r.lifecycle,
    deprecatedAt: iso(r.deprecated_at),
    sunsetAt: iso(r.sunset_at),
    replacedBy: r.replaced_by,
    retiredAt: iso(r.retired_at),
    version: r.version,
    createdAt: r.created_at.toISOString(),
    updatedAt: r.updated_at.toISOString(),
  };
}

function eventData(e: CatalogEntry): Record<string, unknown> {
  return {
    catalog_entry_id: e.id,
    kind: e.kind,
    name: e.name,
    spec_version: e.specVersion,
    title: e.title,
    owner_service: e.ownerService,
    lifecycle: e.lifecycle,
    sunset_at: e.sunsetAt,
    replaced_by: e.replacedBy,
    version: e.version,
  };
}

/** Escapes LIKE wildcards so a search term is matched literally. */
function likeTerm(q: string): string {
  return `%${q.replace(/[\\%_]/g, (c) => `\\${c}`)}%`;
}

export class CatalogStore {
  readonly #db: HubStore;

  constructor(db: HubStore) {
    this.#db = db;
  }

  /**
   * Publishes a contract version (see `placeVersion` for the version rule). A repeated
   * idempotency key returns the first entry (`created` false) when the kind, name, version
   * and document are the same, and IdempotencyKeyReuseError when they are not.
   */
  async publish(tenantId: string, input: NewCatalogEntry): Promise<{ entry: CatalogEntry; created: boolean }> {
    const ops = readSpec(input.kind, input.spec);
    parseSemver(input.specVersion);
    const declared = (input.spec["info"] as Record<string, unknown>)["version"];
    if (declared !== input.specVersion) {
      throw new SpecError(`spec.info.version (${String(declared)}) must equal specVersion ${input.specVersion}`);
    }
    const specText = JSON.stringify(input.spec);
    const digest = createHash("sha256").update(specText).digest("hex");
    const key = input.idempotencyKey;
    try {
      return await this.#db.withTenant(tenantId, async (c) => {
        const replay = async (): Promise<{ entry: CatalogEntry; created: boolean } | null> => {
          const res = await c.query<Row & { spec_sha256: string }>(`select ${COLUMNS}, spec_sha256 from hub_catalog_entry where idempotency_key = $1`, [key]);
          const row = res.rows[0];
          if (row === undefined) return null;
          if (row.kind !== input.kind || row.name !== input.name || row.spec_version !== input.specVersion || row.spec_sha256 !== digest) {
            throw new IdempotencyKeyReuseError();
          }
          return { entry: toEntry(row), created: false };
        };
        // Serialize publishes of one contract; the key is checked under the lock so a concurrent
        // repeat of the same publish finds the first one instead of a "version already published".
        await c.query("select pg_advisory_xact_lock(hashtextextended($1, 0))", [`catalog:${tenantId}:${input.kind}:${input.name}`]);
        if (key !== null) {
          const first = await replay();
          if (first !== null) return first;
        }
        const prior = await c.query<{ spec_version: string; lifecycle: Lifecycle; deprecated_at: Date | null; sunset_at: Date | null; replaced_by: string | null }>(
          "select spec_version, lifecycle, deprecated_at, sunset_at, replaced_by from hub_catalog_entry where kind = $1 and name = $2",
          [input.kind, input.name],
        );
        const inherited = placeVersion(
          input.specVersion,
          prior.rows.map((r) => ({ specVersion: r.spec_version, lifecycle: r.lifecycle, deprecatedAt: r.deprecated_at, sunsetAt: r.sunset_at, replacedBy: r.replaced_by })),
        );
        const res = await c.query<Row>(
          `insert into hub_catalog_entry (tenant_id, id, kind, name, spec_version, title, summary, owner_service, spec_format, spec, spec_bytes, spec_sha256,
             operations, operation_count, lifecycle, deprecated_at, sunset_at, replaced_by, created_by, idempotency_key)
           values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::json, $11, $12, $13::jsonb, $14, $15, $16, $17, $18, $19, $20)
           on conflict (tenant_id, idempotency_key) do nothing
           returning ${COLUMNS}`,
          [
            tenantId, randomUUID(), input.kind, input.name, input.specVersion, input.title, input.summary, input.ownerService,
            input.kind === "api" ? "openapi" : "asyncapi", specText, Buffer.byteLength(specText), digest, JSON.stringify(ops), ops.length,
            inherited === null ? "active" : "deprecated", inherited?.deprecatedAt.toISOString() ?? null, inherited?.sunsetAt.toISOString() ?? null,
            inherited?.replacedBy ?? null, input.createdBy, key,
          ],
        );
        if (res.rows[0] === undefined) {
          // A concurrent publish with the same key committed first.
          return (await replay()) as { entry: CatalogEntry; created: boolean };
        }
        const entry = toEntry(res.rows[0]);
        await enqueue(c, tenantId, { type: "dev.passage.hub.catalogentry.published.v1", aggregateId: entry.id, actor: input.createdBy, data: eventData(entry) });
        return { entry, created: true };
      });
    } catch (err) {
      if ((err as { code?: string }).code === UNIQUE_VIOLATION) {
        throw new CatalogVersionError(`${input.name} ${input.specVersion} is already published`);
      }
      throw err;
    }
  }

  async list(tenantId: string, filter: CatalogFilter, limit: number, cursor: string | null): Promise<Page<CatalogEntry>> {
    const after = cursor === null ? null : decodeCursor(cursor);
    return this.#db.withTenant(tenantId, async (c) => {
      const res = await c.query<Row>(
        `select ${COLUMNS} from hub_catalog_entry
          where ($1::timestamptz is null or (created_at, id) < ($1::timestamptz, $2::uuid))
            and ($4::text is null or kind = $4)
            and ($5::text is null or lifecycle = $5)
            and ($6::text is null or name = $6)
            and ($7::text is null or name ilike $7 escape '\\' or title ilike $7 escape '\\' or owner_service ilike $7 escape '\\' or summary ilike $7 escape '\\')
          order by created_at desc, id desc limit $3`,
        [after?.createdAt ?? null, after?.id ?? null, limit + 1, filter.kind ?? null, filter.lifecycle ?? null, filter.name ?? null, filter.q === undefined ? null : likeTerm(filter.q)],
      );
      const rows = res.rows.slice(0, limit);
      const last = rows[rows.length - 1];
      return { items: rows.map(toEntry), nextCursor: res.rows.length > limit && last !== undefined ? encodeCursor(last.cursor_ts, last.id) : null };
    });
  }

  async get(tenantId: string, id: string): Promise<CatalogDetail | null> {
    return this.#db.withTenant(tenantId, async (c) => {
      const res = await c.query<Row & { operations: CatalogOperation[] }>(`select ${COLUMNS}, operations from hub_catalog_entry where id = $1`, [id]);
      const row = res.rows[0];
      if (row === undefined) return null;
      return { ...toEntry(row), operations: row.operations, issues: docIssues(row.kind, row.operations) };
    });
  }

  /** The published document, or null when the entry does not exist. */
  async spec(tenantId: string, id: string): Promise<unknown | null> {
    return this.#db.withTenant(tenantId, async (c) => {
      const res = await c.query<{ spec: unknown }>("select spec from hub_catalog_entry where id = $1", [id]);
      return res.rows[0] === undefined ? null : res.rows[0].spec;
    });
  }

  /** Optimistic change of the title, summary or owner (the document is immutable), with an updated event: VersionConflictError when stale, null when missing. */
  async patch(tenantId: string, id: string, patch: CatalogPatch, actor: string | null = null): Promise<CatalogEntry | null> {
    return this.#db.withTenant(tenantId, async (c) => {
      const cur = await c.query<Row>(`select ${COLUMNS} from hub_catalog_entry where id = $1 for update`, [id]);
      const row = cur.rows[0];
      if (row === undefined) return null;
      if (row.version !== patch.version) throw new VersionConflictError();
      const res = await c.query<Row>(
        `update hub_catalog_entry set title = $2, summary = $3, owner_service = $4, version = version + 1, updated_at = now()
          where id = $1 returning ${COLUMNS}`,
        [id, patch.title ?? row.title, patch.summary ?? row.summary, patch.ownerService ?? row.owner_service],
      );
      const entry = toEntry(res.rows[0] as Row);
      await enqueue(c, tenantId, { type: "dev.passage.hub.catalogentry.updated.v1", aggregateId: id, actor, data: eventData(entry) });
      return entry;
    });
  }

  /**
   * Announces the end of an active entry: `sunsetAt` must be at least `months` months after `now`.
   * `replacedBy` must be another active entry of the same kind. CatalogStateError unless the entry is active.
   */
  async deprecate(
    tenantId: string,
    id: string,
    input: { version: number; sunsetAt: Date; replacedBy: string | null },
    months: number,
    now: Date,
    actor: string | null,
  ): Promise<CatalogEntry | null> {
    checkSunset(input.sunsetAt, now, months);
    return this.#db.withTenant(tenantId, async (c) => {
      const cur = await c.query<Row>(`select ${COLUMNS} from hub_catalog_entry where id = $1 for update`, [id]);
      const row = cur.rows[0];
      if (row === undefined) return null;
      if (row.version !== input.version) throw new VersionConflictError();
      if (row.lifecycle !== "active") throw new CatalogStateError(`only an active entry can be deprecated; this one is ${row.lifecycle}`);
      if (input.replacedBy !== null) {
        const repl = await c.query<{ id: string }>("select id from hub_catalog_entry where id = $1 and kind = $2 and lifecycle = 'active' and id <> $3", [input.replacedBy, row.kind, id]);
        if (repl.rows[0] === undefined) throw new RangeError("replacedBy must be another active catalog entry of the same kind");
      }
      const res = await c.query<Row>(
        `update hub_catalog_entry set lifecycle = 'deprecated', deprecated_at = $2, sunset_at = $3, replaced_by = $4, version = version + 1, updated_at = now()
          where id = $1 returning ${COLUMNS}`,
        [id, now.toISOString(), input.sunsetAt.toISOString(), input.replacedBy],
      );
      const entry = toEntry(res.rows[0] as Row);
      await enqueue(c, tenantId, { type: "dev.passage.hub.catalogentry.deprecated.v1", aggregateId: id, actor, data: eventData(entry) });
      return entry;
    });
  }

  /** Retires a deprecated entry once its sunset date has passed; CatalogStateError before that or when not deprecated. */
  async retire(tenantId: string, id: string, version: number, now: Date, actor: string | null): Promise<CatalogEntry | null> {
    return this.#db.withTenant(tenantId, async (c) => {
      const cur = await c.query<Row>(`select ${COLUMNS} from hub_catalog_entry where id = $1 for update`, [id]);
      const row = cur.rows[0];
      if (row === undefined) return null;
      if (row.version !== version) throw new VersionConflictError();
      if (row.lifecycle !== "deprecated" || row.sunset_at === null) {
        throw new CatalogStateError(`only a deprecated entry can be retired; this one is ${row.lifecycle}`);
      }
      if (row.sunset_at.getTime() > now.getTime()) {
        throw new CatalogStateError(`the sunset date ${row.sunset_at.toISOString()} has not passed`);
      }
      const res = await c.query<Row>(
        `update hub_catalog_entry set lifecycle = 'retired', retired_at = $2, version = version + 1, updated_at = now()
          where id = $1 returning ${COLUMNS}`,
        [id, now.toISOString()],
      );
      const entry = toEntry(res.rows[0] as Row);
      await enqueue(c, tenantId, { type: "dev.passage.hub.catalogentry.retired.v1", aggregateId: id, actor, data: eventData(entry) });
      return entry;
    });
  }
}
