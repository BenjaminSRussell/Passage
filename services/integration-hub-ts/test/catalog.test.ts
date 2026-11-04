import { randomUUID } from "node:crypto";
import { afterAll, beforeAll, describe, expect, test } from "vitest";

import {
  addMonths,
  CatalogVersionError,
  checkDepth,
  checkSunset,
  compareSemver,
  docIssues,
  MAX_SPEC_DEPTH,
  parseSemver,
  placeVersion,
  readSpec,
  SpecError,
  validateCatalogName,
  validateOwnerService,
  type PublishedVersion,
} from "../src/catalog.ts";
import { aggregateTypeOf, EVENT_TYPES } from "../src/events.ts";
import { api, fixture, ownerUrl, type Api, type Fixture } from "./pgHelper.ts";

const openapi = (extra: Record<string, unknown> = {}) => ({
  openapi: "3.1.0",
  info: { title: "Sales", version: "1.0.0" },
  paths: {
    "/v1/orders": {
      get: { operationId: "listOrders", summary: "List orders", "x-passage-duty": "sales.order.read" },
      post: { operationId: "createOrder", summary: "Create an order", "x-passage-duty": "sales.order.write" },
    },
    "/v1/orders/{id}": { get: { operationId: "getOrder", summary: "Read an order", "x-passage-duty": "sales.order.read" } },
  },
  ...extra,
});
const asyncapi = () => ({
  asyncapi: "3.0.0",
  info: { title: "Sales events", version: "1.0.0" },
  components: { messages: { orderCreated: { name: "dev.passage.sales.order.created.v1", summary: "An order was created." }, orderShipped: { name: "dev.passage.sales.order.shipped.v1" } } },
});

/** `depth` nested arrays inside an `x-deep` extension of a valid document. */
const nested = (depth: number) => JSON.parse(`${"[".repeat(depth)}${"]".repeat(depth)}`) as unknown;

describe("catalog rules", () => {
  test("semantic versions are MAJOR.MINOR.PATCH and compare numerically", () => {
    expect(parseSemver("1.10.0")).toEqual([1, 10, 0]);
    for (const bad of ["1.0", "v1.0.0", "1.0.0-rc1", "", "1.0.0.0", "01x.0.0"]) expect(() => parseSemver(bad), bad).toThrow(RangeError);
    expect(compareSemver("1.10.0", "1.9.9")).toBeGreaterThan(0);
    expect(compareSemver("2.0.0", "10.0.0")).toBeLessThan(0);
    expect(compareSemver("1.2.3", "1.2.3")).toBe(0);
  });
  test("names and owners follow the slug rule", () => {
    expect(() => validateCatalogName("sales-api")).not.toThrow();
    expect(() => validateOwnerService("integration-hub")).not.toThrow();
    for (const bad of ["a", "Upper", "has space", "-lead"]) {
      expect(() => validateCatalogName(bad), bad).toThrow(RangeError);
      expect(() => validateOwnerService(bad), bad).toThrow(RangeError);
    }
  });
  test("an OpenAPI document yields one operation per method and path, with its duty", () => {
    const ops = readSpec("api", openapi());
    expect(ops).toHaveLength(3);
    expect(ops[0]).toEqual({ id: "listOrders", method: "GET", path: "/v1/orders", summary: "List orders", duty: "sales.order.read" });
    expect(docIssues("api", ops)).toEqual([]);
  });
  test("an AsyncAPI document yields one entry per message", () => {
    const ops = readSpec("event", asyncapi());
    expect(ops.map((o) => o.path)).toEqual(["dev.passage.sales.order.created.v1", "dev.passage.sales.order.shipped.v1"]);
    expect(docIssues("event", ops)).toEqual(["dev.passage.sales.order.shipped.v1: no summary"]);
  });
  test("the doc gate flags a missing operationId, summary and x-passage-duty", () => {
    const ops = readSpec("api", { openapi: "3.1.0", info: { title: "t", version: "1" }, paths: { "/x": { get: {} } } });
    expect(docIssues("api", ops)).toEqual(["GET /x: no operationId", "GET /x: no summary", "GET /x: no x-passage-duty"]);
  });
  test("documents of the wrong shape or kind are refused", () => {
    expect(() => readSpec("api", [])).toThrow(SpecError);
    expect(() => readSpec("api", { openapi: "2.0", info: { title: "t", version: "1" }, paths: { "/x": { get: {} } } })).toThrow(/OpenAPI 3/);
    expect(() => readSpec("api", { openapi: "3.1.0", info: { title: "t" }, paths: { "/x": { get: {} } } })).toThrow(/info/);
    expect(() => readSpec("api", { openapi: "3.1.0", info: { title: "t", version: "1" }, paths: {} })).toThrow(/path/);
    expect(() => readSpec("api", { openapi: "3.1.0", info: { title: "t", version: "1" }, paths: { "/x": {} } })).toThrow(/no operation/);
    expect(() => readSpec("api", asyncapi())).toThrow(SpecError);
    expect(() => readSpec("event", openapi())).toThrow(SpecError);
    expect(() => readSpec("event", { asyncapi: "3.0.0", info: { title: "t", version: "1" } })).toThrow(/message/);
    expect(() => readSpec("api", openapi({ big: "x".repeat(2 * 1024 * 1024) }))).toThrow(/larger/);
  });
  test("a document nested deeper than the limit is refused before Postgres has to parse it", () => {
    expect(() => checkDepth({ a: { b: [1, { c: 2 }] } }, 4)).not.toThrow();
    expect(() => checkDepth({ a: { b: [1, { c: 2 }] } }, 3)).toThrow(SpecError);
    expect(() => readSpec("api", openapi({ "x-deep": nested(MAX_SPEC_DEPTH - 1) }))).not.toThrow();
    expect(() => readSpec("api", openapi({ "x-deep": nested(MAX_SPEC_DEPTH) }))).toThrow(/deeper than 64/);
    // Hundreds of thousands of levels fit in 2 MiB; the walk is iterative, so this is a SpecError, not a stack overflow.
    expect(() => readSpec("api", openapi({ "x-deep": nested(300_000) }))).toThrow(SpecError);
  });
  test("versions: unique, higher than the newest of their major line, a new major above every major", () => {
    const v = (specVersion: string, over: Partial<PublishedVersion> = {}): PublishedVersion => ({ specVersion, lifecycle: "active", deprecatedAt: null, sunsetAt: null, replacedBy: null, ...over });
    expect(placeVersion("1.0.0", [])).toBeNull();
    const published = [v("1.2.0"), v("1.10.0"), v("2.0.0")];
    expect(placeVersion("1.10.1", published)).toBeNull();
    expect(placeVersion("1.11.0", published)).toBeNull();
    expect(placeVersion("3.0.0", published)).toBeNull();
    expect(() => placeVersion("2.0.0", published)).toThrow(/never change/);
    expect(() => placeVersion("1.9.0", published)).toThrow(/higher than 1\.10\.0/);
    expect(() => placeVersion("0.9.0", published)).toThrow(/major version must be higher than 2/);
    expect(() => placeVersion("1.10.0", published)).toThrow(CatalogVersionError);
  });
  test("versions: a deprecated line passes its notice on, a retired line takes no more versions", () => {
    const deprecatedAt = new Date("2026-11-03T00:00:00Z");
    const sunsetAt = new Date("2027-11-03T00:00:00Z");
    const line = [
      { specVersion: "1.0.0", lifecycle: "active" as const, deprecatedAt: null, sunsetAt: null, replacedBy: null },
      { specVersion: "1.1.0", lifecycle: "deprecated" as const, deprecatedAt, sunsetAt, replacedBy: "r" },
    ];
    expect(placeVersion("1.2.0", line)).toEqual({ deprecatedAt, sunsetAt, replacedBy: "r" });
    expect(placeVersion("2.0.0", line)).toBeNull();
    const retired = [{ ...line[1]!, lifecycle: "retired" as const }];
    expect(() => placeVersion("1.2.0", retired)).toThrow(/major version 1 is retired/);
    expect(placeVersion("2.0.0", retired)).toBeNull();
  });
  test("addMonths clamps to the end of a shorter month", () => {
    expect(addMonths(new Date("2026-01-31T10:00:00Z"), 1).toISOString()).toBe("2026-02-28T10:00:00.000Z");
    expect(addMonths(new Date("2026-03-15T00:00:00Z"), 12).toISOString()).toBe("2027-03-15T00:00:00.000Z");
    expect(addMonths(new Date("2024-02-29T00:00:00Z"), 12).toISOString()).toBe("2025-02-28T00:00:00.000Z");
  });
  test("a sunset closer than the notice period is refused, exactly the period is fine", () => {
    const now = new Date("2026-11-03T00:00:00Z");
    expect(() => checkSunset(new Date("2027-11-02T23:59:59Z"), now, 12)).toThrow(/12 months/);
    expect(() => checkSunset(new Date("2027-11-03T00:00:00Z"), now, 12)).not.toThrow();
    expect(() => checkSunset(new Date("nope"), now, 12)).toThrow(RangeError);
  });
  test("catalog event types follow the outbox naming and route to passage.hub.catalogentry", () => {
    const types = EVENT_TYPES.filter((t) => t.includes(".catalogentry."));
    expect(types).toHaveLength(4);
    for (const t of types) expect(aggregateTypeOf(t)).toBe("catalogentry");
  });
});

describe.skipIf(ownerUrl === undefined)("catalog on Postgres", () => {
  const WRITE = ["hub.catalog.write", "hub.delivery.read"];
  const READ = ["hub.delivery.read"];
  const T0 = Date.parse("2026-11-03T12:00:00Z") / 1000;
  let now = T0;
  let fx: Fixture;
  let hub: Api;

  beforeAll(async () => {
    fx = await fixture();
    hub = await api({ store: fx.store, clock: () => now });
  });
  afterAll(async () => {
    hub.server.close();
    await fx.close();
  });

  /** Publishes with the document's info.version set to the specVersion, as the hub requires. */
  const publish = (tenant: string, over: Record<string, unknown> = {}, extra: Record<string, string> = {}) => {
    const body: Record<string, unknown> = { kind: "api", name: "sales-api", specVersion: "1.0.0", title: "Sales API", ownerService: "sales", spec: openapi(), ...over };
    const spec = body["spec"] as { info?: Record<string, unknown> };
    if (typeof spec === "object" && spec !== null && !Array.isArray(spec) && typeof spec.info === "object" && !("keepInfo" in over)) {
      body["spec"] = { ...spec, info: { ...spec.info, version: body["specVersion"] } };
    }
    delete body["keepInfo"];
    return hub.call("POST", "/v1/hub/catalog", { tenant, duties: WRITE }, body, extra);
  };
  const events = async (tenant: string) =>
    (await fx.owner.query("select type, aggregate_type, aggregate_id, payload from outbox where tenant_id = $1 and aggregate_type = 'catalogentry' order by created_at, id", [tenant])).rows as {
      type: string; aggregate_type: string; aggregate_id: string; payload: { actor?: string; data: Record<string, unknown> };
    }[];

  test("publishes an API version, lists, reads detail with operations and returns the document", async () => {
    const t = randomUUID();
    const res = await publish(t, { summary: "Orders" });
    expect(res.status).toBe(201);
    expect(res.body).toMatchObject({ kind: "api", name: "sales-api", specVersion: "1.0.0", lifecycle: "active", operationCount: 3, specFormat: "openapi", version: 1, summary: "Orders" });
    expect(res.body.spec).toBeUndefined();
    const list = await hub.call("GET", "/v1/hub/catalog", { tenant: t, duties: READ });
    expect(list.body.items).toHaveLength(1);
    const detail = await hub.call("GET", `/v1/hub/catalog/${res.body.id}`, { tenant: t, duties: READ });
    expect(detail.body.operations).toHaveLength(3);
    expect(detail.body.operations[0]).toMatchObject({ id: "listOrders", method: "GET", duty: "sales.order.read" });
    expect(detail.body.issues).toEqual([]);
    const spec = await hub.call("GET", `/v1/hub/catalog/${res.body.id}/spec`, { tenant: t, duties: READ });
    expect(spec.body).toEqual(openapi());
    const [e] = await events(t);
    expect(e).toMatchObject({ type: "dev.passage.hub.catalogentry.published.v1", aggregate_type: "catalogentry", aggregate_id: res.body.id });
    expect(e!.payload.data).toMatchObject({ kind: "api", name: "sales-api", spec_version: "1.0.0", lifecycle: "active", sunset_at: null });
  });

  test("publishes an event contract and reports documentation gaps", async () => {
    const t = randomUUID();
    const res = await publish(t, { kind: "event", name: "sales-events", spec: asyncapi() });
    expect(res.status).toBe(201);
    expect(res.body.specFormat).toBe("asyncapi");
    const detail = await hub.call("GET", `/v1/hub/catalog/${res.body.id}`, { tenant: t, duties: READ });
    expect(detail.body.issues).toEqual(["dev.passage.sales.order.shipped.v1: no summary"]);
    expect((await publish(t, { kind: "event", name: "x-events", spec: openapi() })).status).toBe(400);
  });

  test("an idempotency key returns the first entry for the same publish and refuses a different one", async () => {
    const t = randomUUID();
    const first = await publish(t, {}, { "idempotency-key": "k1" });
    const again = await publish(t, {}, { "idempotency-key": "k1" });
    expect(first.status).toBe(201);
    expect(again.status).toBe(200);
    expect(again.body.id).toBe(first.body.id);
    for (const other of [{ specVersion: "1.1.0" }, { name: "billing-api" }, { spec: openapi({ "x-note": "changed" }) }]) {
      const reused = await publish(t, other, { "idempotency-key": "k1" });
      expect(reused.status, JSON.stringify(other)).toBe(422);
      expect(reused.body.code).toBe("HUB-0422");
    }
    // Concurrent repeats of one keyed publish create one entry.
    const burst = await Promise.all(Array.from({ length: 4 }, () => publish(t, { specVersion: "4.0.0" }, { "idempotency-key": "k2" })));
    expect(burst.filter((r) => r.status === 201)).toHaveLength(1);
    expect(new Set(burst.map((r) => r.body.id)).size).toBe(1);
    expect(await events(t)).toHaveLength(2);
  });

  test("the document's info.version must equal specVersion", async () => {
    const t = randomUUID();
    const res = await publish(t, { specVersion: "1.2.0", keepInfo: true });
    expect(res.status).toBe(400);
    expect(res.body.detail).toMatch(/info\.version \(1\.0\.0\) must equal specVersion 1\.2\.0/);
  });

  test("a deeply nested document is a 400, not a Postgres stack overflow", async () => {
    const t = randomUUID();
    // Sent as text: 200,000 levels (400 KB) are past what JSON.stringify can recurse through.
    const deep = `${"[".repeat(200_000)}${"]".repeat(200_000)}`;
    const spec = JSON.stringify(openapi()).replace(/}$/, `,"x-deep":${deep}}`);
    const body = `{"kind":"api","name":"deep-api","specVersion":"1.0.0","title":"Deep","ownerService":"sales","spec":${spec}}`;
    const res = await fetch(`${hub.base}/v1/hub/catalog`, {
      method: "POST",
      headers: { "x-passage-tenant": t, "x-passage-user": randomUUID(), "x-passage-duties": WRITE.join(","), "content-type": "application/json" },
      body,
    });
    expect(res.status).toBe(400);
    expect(((await res.json()) as { detail: string }).detail).toMatch(/deeper than 64/);
    expect(await events(t)).toHaveLength(0);
  });

  test("the document comes back with its keys in the published order", async () => {
    const t = randomUUID();
    const spec = { ...openapi(), paths: { "/v1/zeta": { get: { summary: "z" } }, "/v1/a": { get: { summary: "a" } }, "/v1/mid": { get: { summary: "m" } } } };
    const e = (await publish(t, { spec })).body;
    const back = (await hub.call("GET", `/v1/hub/catalog/${e.id}/spec`, { tenant: t, duties: READ })).body;
    expect(Object.keys(back)).toEqual(Object.keys(spec));
    expect(Object.keys(back.paths)).toEqual(["/v1/zeta", "/v1/a", "/v1/mid"]);
  });

  test("a published version never changes and a new version must be higher", async () => {
    const t = randomUUID();
    expect((await publish(t, { specVersion: "1.2.0" })).status).toBe(201);
    const same = await publish(t, { specVersion: "1.2.0" });
    expect(same.status).toBe(409);
    expect(same.body.code).toBe("HUB-0409");
    const lower = await publish(t, { specVersion: "1.10.0" });
    expect(lower.status).toBe(201);
    expect((await publish(t, { specVersion: "1.9.0" })).status).toBe(409);
    // Version 1 keeps taking additive versions while version 2 runs beside it; a new major must be the highest.
    expect((await publish(t, { specVersion: "2.0.0" })).status).toBe(201);
    expect((await publish(t, { specVersion: "1.11.0" })).status).toBe(201);
    expect((await publish(t, { specVersion: "0.9.0" })).status).toBe(409);
    // Another name, another kind and another tenant are separate contracts.
    expect((await publish(t, { name: "billing-api", specVersion: "0.1.0" })).status).toBe(201);
    expect((await publish(t, { kind: "event", spec: asyncapi(), specVersion: "0.1.0" })).status).toBe(201);
    expect((await publish(randomUUID(), { specVersion: "1.0.0" })).status).toBe(201);
  });

  test("concurrent publishes of one version create exactly one entry", async () => {
    const t = randomUUID();
    const out = await Promise.all(Array.from({ length: 6 }, () => publish(t, { specVersion: "3.0.0" })));
    expect(out.filter((r) => r.status === 201)).toHaveLength(1);
    expect(out.filter((r) => r.status === 409)).toHaveLength(5);
    expect(await events(t)).toHaveLength(1);
  });

  test("validation: bad kind, name, version, owner, spec and extra fields are 400; missing duty is 403", async () => {
    const t = randomUUID();
    for (const bad of [
      { kind: "soap" }, { name: "Bad Name" }, { specVersion: "1.0" }, { ownerService: "Sales!" }, { spec: [] }, { spec: "x" },
      { spec: { openapi: "3.1.0", info: { title: "t", version: "1" }, paths: {} } }, { title: "" }, { summary: 5 }, { extra: 1 },
    ]) {
      expect((await publish(t, bad)).status, JSON.stringify(bad)).toBe(400);
    }
    expect((await hub.call("POST", "/v1/hub/catalog", { tenant: t, duties: READ }, { kind: "api" })).status).toBe(403);
    expect((await hub.call("GET", "/v1/hub/catalog", { tenant: t, duties: [] })).status).toBe(403);
    expect((await hub.call("GET", `/v1/hub/catalog/${randomUUID()}/spec`, { tenant: t, duties: [] })).status).toBe(403);
    expect(await events(t)).toHaveLength(0);
  });

  test("accepts a document well above the 64 KiB general body limit", async () => {
    const t = randomUUID();
    const big = openapi({ "x-notes": "n".repeat(300 * 1024) });
    expect((await publish(t, { spec: big })).status).toBe(201);
    const huge = openapi({ "x-notes": "n".repeat(2 * 1024 * 1024 + 20 * 1024) });
    expect((await publish(t, { name: "huge-api", spec: huge })).status).toBe(413);
  });

  test("lists with kind, lifecycle, name and text filters and keyset paging", async () => {
    const t = randomUUID();
    await publish(t, { name: "sales-api", title: "Sales API" });
    await publish(t, { name: "billing-api", title: "Billing 100% API", ownerService: "billing" });
    await publish(t, { kind: "event", name: "sales-events", spec: asyncapi(), title: "Sales events" });
    const get = async (qs: string) => (await hub.call("GET", `/v1/hub/catalog${qs}`, { tenant: t, duties: READ })).body;
    expect((await get("?kind=event")).items.map((i: { name: string }) => i.name)).toEqual(["sales-events"]);
    expect((await get("?q=billing")).items).toHaveLength(1);
    expect((await get("?q=100%25")).items).toHaveLength(1);
    expect((await get("?q=%25")).items).toHaveLength(1);
    expect((await get("?q=_ales")).items).toHaveLength(0);
    expect((await get("?name=sales-api")).items).toHaveLength(1);
    expect((await get("?lifecycle=deprecated")).items).toHaveLength(0);
    const p1 = await get("?limit=2");
    expect(p1.items).toHaveLength(2);
    expect(p1.nextCursor).not.toBeNull();
    const p2 = await get(`?limit=2&cursor=${encodeURIComponent(p1.nextCursor)}`);
    expect(p2.items).toHaveLength(1);
    expect(p2.nextCursor).toBeNull();
    for (const bad of ["?kind=x", "?lifecycle=x", "?limit=0", `?q=${"x".repeat(101)}`]) {
      expect((await hub.call("GET", `/v1/hub/catalog${bad}`, { tenant: t, duties: READ })).status, bad).toBe(400);
    }
  });

  test("PATCH changes title, summary and owner under an optimistic version; the document stays", async () => {
    const t = randomUUID();
    const e = (await publish(t)).body;
    const up = await hub.call("PATCH", `/v1/hub/catalog/${e.id}`, { tenant: t, duties: WRITE }, { version: 1, title: "Sales public API", ownerService: "sales-core" });
    expect(up.body).toMatchObject({ version: 2, title: "Sales public API", ownerService: "sales-core", specVersion: "1.0.0" });
    const updated = (await events(t)).filter((x) => x.type === "dev.passage.hub.catalogentry.updated.v1");
    expect(updated.map((x) => x.payload.data)).toEqual([expect.objectContaining({ catalog_entry_id: e.id, title: "Sales public API", owner_service: "sales-core", version: 2 })]);
    expect((await hub.call("PATCH", `/v1/hub/catalog/${e.id}`, { tenant: t, duties: WRITE }, { version: 1, title: "late" })).status).toBe(409);
    expect((await hub.call("PATCH", `/v1/hub/catalog/${e.id}`, { tenant: t, duties: WRITE }, { version: 2, spec: {} })).status).toBe(400);
    expect((await hub.call("PATCH", `/v1/hub/catalog/${e.id}`, { tenant: t, duties: WRITE }, { title: "no version" })).status).toBe(400);
    expect((await hub.call("PATCH", `/v1/hub/catalog/${randomUUID()}`, { tenant: t, duties: WRITE }, { version: 1 })).status).toBe(404);
    expect((await hub.call("GET", `/v1/hub/catalog/${e.id}/spec`, { tenant: t, duties: READ })).body).toEqual(openapi());
  });

  test("deprecation needs 12 months of notice, may name a replacement, and writes an event", async () => {
    const t = randomUUID();
    const v1 = (await publish(t, { specVersion: "1.0.0" })).body;
    const v2 = (await publish(t, { specVersion: "2.0.0" })).body;
    const call = (id: string, body: unknown) => hub.call("POST", `/v1/hub/catalog/${id}:deprecate`, { tenant: t, duties: WRITE }, body);
    const tooSoon = await call(v1.id, { version: 1, sunsetAt: "2027-11-02T11:59:59Z" });
    expect(tooSoon.status).toBe(400);
    expect(tooSoon.body.detail).toMatch(/12 months/);
    expect((await call(v1.id, { version: 1, sunsetAt: "not a date" })).status).toBe(400);
    expect((await call(v1.id, { version: 1, sunsetAt: "2028-01-01T00:00:00Z", replacedBy: v1.id })).status).toBe(400);
    expect((await call(v1.id, { version: 1, sunsetAt: "2028-01-01T00:00:00Z", replacedBy: randomUUID() })).status).toBe(400);
    expect((await call(v1.id, { version: 9, sunsetAt: "2028-01-01T00:00:00Z" })).status).toBe(409);
    const ok = await call(v1.id, { version: 1, sunsetAt: "2027-11-03T12:00:00Z", replacedBy: v2.id });
    expect(ok.status).toBe(200);
    expect(ok.body).toMatchObject({ lifecycle: "deprecated", version: 2, replacedBy: v2.id, sunsetAt: "2027-11-03T12:00:00.000Z" });
    expect(ok.body.deprecatedAt).toBe("2026-11-03T12:00:00.000Z");
    // A deprecated entry cannot be deprecated again; an entry can not be replaced by a deprecated one.
    expect((await call(v1.id, { version: 2, sunsetAt: "2029-01-01T00:00:00Z" })).status).toBe(409);
    const v3 = (await publish(t, { specVersion: "3.0.0" })).body;
    expect((await call(v3.id, { version: 1, sunsetAt: "2029-01-01T00:00:00Z", replacedBy: v1.id })).status).toBe(400);
    expect((await hub.call("GET", "/v1/hub/catalog?lifecycle=deprecated", { tenant: t, duties: READ })).body.items).toHaveLength(1);
    expect((await events(t)).filter((e) => e.type.endsWith("deprecated.v1")).map((e) => e.payload.data)).toEqual([
      expect.objectContaining({ catalog_entry_id: v1.id, lifecycle: "deprecated", replaced_by: v2.id, sunset_at: "2027-11-03T12:00:00.000Z" }),
    ]);
  });

  test("retire only after the sunset date has passed; nothing is deleted", async () => {
    const t = randomUUID();
    const e = (await publish(t)).body;
    const retire = (v: number) => hub.call("POST", `/v1/hub/catalog/${e.id}:retire`, { tenant: t, duties: WRITE }, { version: v });
    expect((await retire(1)).status).toBe(409);
    expect((await hub.call("POST", `/v1/hub/catalog/${e.id}:deprecate`, { tenant: t, duties: WRITE }, { version: 1, sunsetAt: "2027-11-03T12:00:00Z" })).status).toBe(200);
    const early = await retire(2);
    expect(early.status).toBe(409);
    expect(early.body.detail).toMatch(/not passed/);
    now = Date.parse("2027-11-03T12:00:01Z") / 1000;
    try {
      expect((await retire(1)).status).toBe(409);
      const done = await retire(2);
      expect(done.status).toBe(200);
      expect(done.body).toMatchObject({ lifecycle: "retired", version: 3 });
      expect(done.body.retiredAt).toBe("2027-11-03T12:00:01.000Z");
      expect((await retire(3)).status).toBe(409);
    } finally {
      now = T0;
    }
    expect((await hub.call("GET", `/v1/hub/catalog/${e.id}/spec`, { tenant: t, duties: READ })).status).toBe(200);
    expect((await events(t)).map((x) => x.type)).toEqual([
      "dev.passage.hub.catalogentry.published.v1", "dev.passage.hub.catalogentry.deprecated.v1", "dev.passage.hub.catalogentry.retired.v1",
    ]);
  });

  test("a new version of a deprecated major line is published deprecated; a retired line takes none", async () => {
    const t = randomUUID();
    const v1 = (await publish(t, { specVersion: "1.0.0" })).body;
    const v2 = (await publish(t, { specVersion: "2.0.0" })).body;
    expect((await hub.call("POST", `/v1/hub/catalog/${v1.id}:deprecate`, { tenant: t, duties: WRITE }, { version: 1, sunsetAt: "2027-12-01T00:00:00Z", replacedBy: v2.id })).status).toBe(200);
    const patch = await publish(t, { specVersion: "1.0.1" });
    expect(patch.status).toBe(201);
    expect(patch.body).toMatchObject({ lifecycle: "deprecated", sunsetAt: "2027-12-01T00:00:00.000Z", replacedBy: v2.id, deprecatedAt: "2026-11-03T12:00:00.000Z" });
    const published = (await events(t)).filter((x) => x.type.endsWith("published.v1")).map((x) => x.payload.data);
    expect(published.at(-1)).toMatchObject({ spec_version: "1.0.1", lifecycle: "deprecated", sunset_at: "2027-12-01T00:00:00.000Z" });
    // Version 2 is unaffected.
    expect((await publish(t, { specVersion: "2.1.0" })).body.lifecycle).toBe("active");
    now = Date.parse("2027-12-02T00:00:00Z") / 1000;
    try {
      expect((await hub.call("POST", `/v1/hub/catalog/${patch.body.id}:retire`, { tenant: t, duties: WRITE }, { version: 1 })).status).toBe(200);
      const late = await publish(t, { specVersion: "1.0.2" });
      expect(late.status).toBe(409);
      expect(late.body.detail).toMatch(/major version 1 is retired/);
      expect((await publish(t, { specVersion: "3.0.0" })).status).toBe(201);
    } finally {
      now = T0;
    }
  });

  test("a configured notice period below 12 months is raised to 12", async () => {
    const short = await api({ store: fx.store, clock: () => now, deprecationMonths: 3 });
    const longer = await api({ store: fx.store, clock: () => now, deprecationMonths: 18 });
    try {
      const t = randomUUID();
      const e = (await publish(t)).body;
      const dep = (h: Api, at: string) => h.call("POST", `/v1/hub/catalog/${e.id}:deprecate`, { tenant: t, duties: WRITE }, { version: 1, sunsetAt: at });
      expect((await dep(short, "2027-06-01T00:00:00Z")).status).toBe(400);
      expect((await dep(longer, "2028-03-01T00:00:00Z")).status).toBe(400);
      expect((await dep(longer, "2028-05-03T12:00:00Z")).status).toBe(200);
    } finally {
      short.server.close();
      longer.server.close();
    }
  });

  test("tenants cannot see or change each other's entries", async () => {
    const a = randomUUID();
    const b = randomUUID();
    const e = (await publish(a)).body;
    expect((await hub.call("GET", "/v1/hub/catalog", { tenant: b, duties: READ })).body.items).toEqual([]);
    for (const [m, p, body] of [
      ["GET", `/v1/hub/catalog/${e.id}`, undefined], ["GET", `/v1/hub/catalog/${e.id}/spec`, undefined],
      ["PATCH", `/v1/hub/catalog/${e.id}`, { version: 1, title: "x" }], ["POST", `/v1/hub/catalog/${e.id}:retire`, { version: 1 }],
      ["POST", `/v1/hub/catalog/${e.id}:deprecate`, { version: 1, sunsetAt: "2030-01-01T00:00:00Z" }],
    ] as const) {
      expect((await hub.call(m, p, { tenant: b, duties: WRITE }, body)).status, `${m} ${p}`).toBe(404);
    }
    expect((await hub.call("GET", `/v1/hub/catalog/${e.id}`, { tenant: a, duties: READ })).body.version).toBe(1);
  });

  test("method checks", async () => {
    const t = randomUUID();
    const e = (await publish(t)).body;
    expect((await hub.call("DELETE", "/v1/hub/catalog", { tenant: t })).status).toBe(405);
    expect((await hub.call("DELETE", `/v1/hub/catalog/${e.id}`, { tenant: t })).status).toBe(405);
    expect((await hub.call("GET", `/v1/hub/catalog/${e.id}:retire`, { tenant: t })).status).toBe(405);
    expect((await hub.call("POST", `/v1/hub/catalog/${e.id}/spec`, { tenant: t })).status).toBe(405);
  });
});
