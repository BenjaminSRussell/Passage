// rest-push end to end on Postgres: sales.order.confirmed -> render (fake migration service) -> POST to a fake
// legacy ERP on an operator-allowlisted private address -> legacy key recorded (fake xref).

import { randomUUID } from "node:crypto";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterAll, afterEach, beforeAll, describe, expect, test, vi } from "vitest";

import { createMessageHandler } from "../src/consumer.ts";
import type { MigrationClient, RenderInput, RenderOutcome, XrefInput, XrefOutcome } from "../src/migrationClient.ts";
import { httpExchange, RestPusher } from "../src/restPush.ts";
import { Poller } from "../src/poller.ts";
import { EgressPolicy, validateWebhookUrl } from "../src/ssrf.ts";
import { api, fixture, ownerUrl, type Api, type Fixture } from "./pgHelper.ts";

const T0 = 1_800_000_000;
const MAPPING = "6f1c6d62-3c0a-4bb7-8f5e-0d6f2a9a7a11";
const PASSWORD = "erp-service-password-9f3a";
const ERP_HOST = "erp.corp.example";
const ERP_ADDRESS = "10.9.0.7"; // the operator-allowlisted private address the fake ERP pretends to live at

interface ErpHit {
  method: string;
  url: string;
  headers: Record<string, string | string[] | undefined>;
  body: string;
}

/** A legacy ERP: creates one record per Idempotency-Key and answers the same record for a repeat. */
interface FakeErp {
  server: Server;
  port: number;
  hits: ErpHit[];
  records: Map<string, string>;
  /** Overrides the answer for hit number n (1-based). */
  script: (n: number, hit: ErpHit) => { status: number; body?: string; headers?: Record<string, string> } | undefined;
}

async function fakeErp(): Promise<FakeErp> {
  const erp: FakeErp = { server: createServer(), port: 0, hits: [], records: new Map(), script: () => undefined };
  erp.server.on("request", (req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      const hit = { method: req.method ?? "", url: req.url ?? "", headers: req.headers, body: Buffer.concat(chunks).toString() };
      erp.hits.push(hit);
      const scripted = erp.script(erp.hits.length, hit);
      if (scripted !== undefined) {
        res.writeHead(scripted.status, { "content-type": "application/json", ...scripted.headers }).end(scripted.body ?? "");
        return;
      }
      const key = String(req.headers["idempotency-key"]);
      const existing = erp.records.get(key);
      const id = existing ?? `SO-${1000 + erp.records.size + 1}`;
      erp.records.set(key, id);
      res.writeHead(existing === undefined ? 201 : 200, { "content-type": "application/json" }).end(JSON.stringify({ data: { id, status: "open" } }));
    });
  });
  await new Promise<void>((ok) => erp.server.listen(0, "127.0.0.1", ok));
  erp.port = (erp.server.address() as AddressInfo).port;
  return erp;
}

/** A migration service whose render maps sales.order.confirmed to a legacy salesorder body. */
class FakeMigration implements MigrationClient {
  renders: RenderInput[] = [];
  xrefs: XrefInput[] = [];
  unresolved = new Set<string>();
  /** Migration slice 23: the render names a ref per element of a pushed collection. */
  lineRefs = false;
  lookups: { name: string; table: string; version: number }[] = [];
  renderStatus: number | null = null;
  xrefStatus: number | null = null;
  async render(input: RenderInput): Promise<RenderOutcome> {
    this.renders.push(input);
    const data = input.event["data"] as { order_id: string; customer_id: string; currency: string; net_total: string; lines: { line_id: string; product_id: string; quantity: string }[] };
    if (this.renderStatus !== null) {
      return { ok: false, status: this.renderStatus, code: null, detail: "unavailable", retryable: true };
    }
    if (this.unresolved.has(data.customer_id)) {
      return { ok: false, status: 422, code: "MIG-XREF-UNRESOLVED", detail: `customer ${data.customer_id} has no legacy key`, retryable: false };
    }
    return {
      ok: true,
      rendered: {
        body: {
          CustAccount: `C-${data.customer_id.slice(0, 4)}`,
          CurrencyCode: data.currency,
          Lines: data.lines.map((l, i) => ({ LineNum: i + 1, ItemId: l.product_id.slice(0, 6), Qty: l.quantity })),
          NetAmount: data.net_total,
        },
        idempotencyKey: `render-${input.event["id"]}`,
        refs: [{ entity: "salesOrder", id: data.order_id }, ...(this.lineRefs ? data.lines.map((l, i) => ({ entity: "salesOrderLine", id: l.line_id, collection: "Lines", index: i })) : [])],
        lookups: this.lookups,
      },
    };
  }
  /** (mapping, eventId) -> legacy key, idempotent like migration.outbound_xref_event: another key is 409 MIG-XREF-EVENT-CONFLICT. */
  recorded = new Map<string, string>();
  async recordXref(input: XrefInput): Promise<XrefOutcome> {
    if (this.xrefStatus !== null) {
      return { ok: false, status: this.xrefStatus, code: null, detail: "xref store down", retryable: true };
    }
    const k = `${input.mappingId}/${input.eventId}`;
    const before = this.recorded.get(k);
    if (before !== undefined && before !== input.legacyKey) {
      return { ok: false, status: 409, code: "MIG-XREF-EVENT-CONFLICT", detail: `event ${input.eventId} already recorded a different legacy key or record for this mapping`, retryable: false };
    }
    this.recorded.set(k, input.legacyKey);
    this.xrefs.push(input);
    return { ok: true };
  }
}

function confirmed(tenantid: string, over: Record<string, unknown> = {}, data: Record<string, unknown> = {}): Record<string, unknown> {
  // Shaped by services/sales/events/asyncapi.yaml: dev.passage.sales.order.confirmed.v1 with OrderConfirmedData.
  return {
    specversion: "1.0",
    id: randomUUID(),
    source: "/passage/sales/sales",
    type: "dev.passage.sales.order.confirmed.v1",
    subject: "order-" + randomUUID().slice(0, 8),
    time: new Date(T0 * 1000).toISOString(),
    tenantid,
    legalentityid: "22222222-3333-4444-8555-666666666666",
    actor: "11111111-1111-4111-8111-111111111111",
    passagesequence: 1,
    datacontenttype: "application/json",
    data: {
      order_id: randomUUID(),
      customer_id: "c0ffee00-0000-4000-8000-000000000001",
      currency: "USD",
      lines: [{ line_id: randomUUID(), product_id: "ab12cd34-0000-4000-8000-000000000002", quantity: "12", site_id: randomUUID(), warehouse_id: randomUUID(), confirmed_ship_date: "2026-12-01" }],
      net_total: "1200.00",
      version: 3,
      ...data,
    },
    ...over,
  };
}
const message = (e: unknown) => ({ topic: "passage.sales.order", partition: 0, offset: "1", key: null, value: Buffer.from(JSON.stringify(e)) });

describe.skipIf(ownerUrl === undefined)("rest-push on Postgres", () => {
  let fx: Fixture;
  let now = T0;
  const clock = () => now;
  let hub: Api;
  let erp: FakeErp;
  const migration = new FakeMigration();
  const resolve = async () => [ERP_ADDRESS];
  const egressAllow = new EgressPolicy("10.9.0.0/16");
  const errors: unknown[] = [];

  // The real guard, with the operator allowlist, runs on every request; only the final connection is pointed at the fake.
  const exchange = httpExchange({
    validate: async (u) => ({ ...(await validateWebhookUrl(u, { allowHttp: true, resolve, egressAllow })), address: "127.0.0.1", family: 4 as const }),
  });
  const pusher = new RestPusher({ migration, exchange, nowMs: () => now * 1000 });

  beforeAll(async () => {
    fx = await fixture();
    erp = await fakeErp();
    hub = await api({ store: fx.store, guard: { allowHttp: true, resolve, egressAllow }, clock, rest: pusher });
  });
  afterAll(async () => {
    hub.server.close();
    erp.server.close();
    await fx.close();
  });
  afterEach(() => {
    erp.script = () => undefined;
    migration.lineRefs = false;
    migration.lookups = [];
    erp.records.clear();
    now = T0;
    migration.renderStatus = null;
    migration.xrefStatus = null;
    migration.unresolved.clear();
    migration.recorded.clear();
    vi.restoreAllMocks();
  });

  const poller = () => new Poller({ store: fx.store, transport: async () => null, rest: pusher, clock, leaseSeconds: 60, onError: (e) => errors.push(e) });
  const consume = (m: ReturnType<typeof message>) => createMessageHandler({ store: fx.store, dlqTopic: "hub.dlq", clock, deadLetter: async () => undefined })(m);
  const url = (path: string) => `http://${ERP_HOST}:${erp.port}${path}`;

  async function subscribe(tenant: string, over: Record<string, unknown> = {}) {
    const res = await hub.call("POST", "/v1/hub/subscriptions", { tenant }, {
      kind: "rest-push",
      name: "Legacy ERP orders",
      url: url("/api/salesorders"),
      patterns: ["dev.passage.sales.order.confirmed.v1"],
      outboundMappingId: MAPPING,
      method: "POST",
      responseKeyPath: "$.data.id",
      auth: { type: "basic", username: "passage", password: PASSWORD },
      ...over,
    });
    expect(res.status, JSON.stringify(res.body)).toBe(201);
    return res.body as { id: string; version: number };
  }
  const deliveries = async (tenant: string, q = "") => (await hub.call("GET", `/v1/hub/deliveries${q}`, { tenant })).body.items as any[];

  test("an order confirmed event becomes a POST of the rendered body; a duplicate event creates no second record; the legacy key is recorded", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    const sub = await subscribe(tenant);
    const e = confirmed(tenant);
    await consume(message(e));
    await consume(message(e)); // Kafka redelivery
    expect(await poller().pollOnce()).toBeGreaterThanOrEqual(1);

    expect(erp.hits).toHaveLength(1);
    const hit = erp.hits[0] as ErpHit;
    expect(hit.method).toBe("POST");
    expect(hit.url).toBe("/api/salesorders");
    expect(hit.headers["idempotency-key"]).toBe(e["id"]);
    expect(hit.headers["authorization"]).toBe(`Basic ${Buffer.from(`passage:${PASSWORD}`).toString("base64")}`);
    expect(JSON.parse(hit.body)).toMatchObject({ CustAccount: "C-c0ff", CurrencyCode: "USD", NetAmount: "1200.00", Lines: [{ LineNum: 1, ItemId: "ab12cd", Qty: "12" }] });

    // The render saw the CloudEvent and the event's legal entity.
    const rendered = migration.renders.at(-1) as RenderInput;
    expect(rendered).toMatchObject({ tenantId: tenant, mappingId: MAPPING, legalEntityId: "22222222-3333-4444-8555-666666666666" });
    // A background push never forwards the producing actor (the event's actor is a user id here, service:sales elsewhere).
    expect(rendered.actor).toBeUndefined();
    expect(migration.xrefs.at(-1)?.actor).toBeUndefined();
    expect(rendered.event).toMatchObject({ id: e["id"], type: "dev.passage.sales.order.confirmed.v1" });

    const x = migration.xrefs.at(-1) as XrefInput;
    expect(x).toMatchObject({ tenantId: tenant, mappingId: MAPPING, legacyKey: "SO-1001", eventId: e["id"] });
    expect(x.refs).toEqual([{ entity: "salesOrder", id: (e["data"] as any).order_id }]);

    const [d] = await deliveries(tenant);
    expect(d).toMatchObject({ subscriptionId: sub.id, status: "delivered", attempts: 1, lastStatus: 201, legacyKey: "SO-1001", responseExcerpt: expect.stringContaining("SO-1001") });

    // The same event arriving again (replay after a dedupe window, a second consumer) pushes with the same key: still one record.
    await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:replay`, { tenant }, { from: new Date(T0 * 1000).toISOString() });
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(2);
    expect(erp.hits[1]?.headers["idempotency-key"]).toBe(e["id"]);
    expect(erp.records.size).toBe(1);
  });

  test("a 503 is retried on the schedule with the same Idempotency-Key and then delivered", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    await subscribe(tenant);
    erp.script = (n) => (n === 1 ? { status: 503, body: "maintenance" } : undefined);
    const e = confirmed(tenant);
    await consume(message(e));

    now = T0;
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(1);
    let [d] = await deliveries(tenant);
    expect(d).toMatchObject({ status: "pending", attempts: 1, lastStatus: 503, nextRetryAt: T0 + 10, responseExcerpt: "maintenance" });

    now = T0 + 9;
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(1);

    now = T0 + 10;
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(2);
    expect(new Set(erp.hits.map((h) => h.headers["idempotency-key"]))).toEqual(new Set([e["id"]]));
    [d] = await deliveries(tenant);
    expect(d).toMatchObject({ status: "delivered", attempts: 2, lastStatus: 201, legacyKey: "SO-1001" });
    expect(erp.records.size).toBe(1);
    now = T0;
  });

  test("429 honours Retry-After; 410 pauses the subscription; a timeout-like silence retries", async () => {
    const tenant = randomUUID();
    const sub = await subscribe(tenant);
    erp.hits.length = 0;
    erp.script = (n) => (n === 1 ? { status: 429, headers: { "retry-after": "120" } } : n === 2 ? { status: 410, body: "gone" } : undefined);
    await consume(message(confirmed(tenant)));
    await poller().pollOnce();
    let [d] = await deliveries(tenant);
    expect(d).toMatchObject({ status: "pending", lastStatus: 429, nextRetryAt: T0 + 120 });
    now = T0 + 120;
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(2);
    const paused = (await hub.call("GET", `/v1/hub/subscriptions/${sub.id}`, { tenant })).body;
    expect(paused).toMatchObject({ status: "paused", pausedReason: "HUB-0114" });
    [d] = await deliveries(tenant);
    expect(d.status).toBe("pending");
    now = T0;
  });

  test("a 4xx dead-letters at once with the response body kept, truncated; replay after the fix delivers", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    await subscribe(tenant);
    erp.script = (n) => (n === 1 ? { status: 400, body: JSON.stringify({ error: "InvalidAccount", detail: "x".repeat(6000) }) } : undefined);
    await consume(message(confirmed(tenant)));
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(1);
    const dead = (await hub.call("GET", "/v1/hub/dead-letters", { tenant })).body.items as any[];
    expect(dead).toHaveLength(1);
    expect(dead[0]).toMatchObject({ status: "dead_letter", deadLetterCode: "HUB-0117", attempts: 1, lastStatus: 400 });
    expect(dead[0].responseExcerpt).toContain("InvalidAccount");
    expect(dead[0].responseExcerpt.length).toBeLessThanOrEqual(2001);

    // Nothing more is sent for it until an operator replays it.
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(1);
    const replay = await hub.call("POST", `/v1/hub/deliveries/${dead[0].id}:replay`, { tenant });
    expect(replay.status).toBe(202);
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(2);
    expect((await deliveries(tenant))[0]).toMatchObject({ status: "delivered", responseExcerpt: expect.stringContaining("SO-") });
    expect((await hub.call("POST", `/v1/hub/deliveries/${dead[0].id}:replay`, { tenant })).status).toBe(409);
  });

  test("an unresolved cross-reference (render 422) is dead-lettered with the reason, then replayed once the key exists", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    await subscribe(tenant);
    migration.unresolved.add("c0ffee00-0000-4000-8000-000000000001");
    await consume(message(confirmed(tenant)));
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(0);
    const [dead] = (await hub.call("GET", "/v1/hub/dead-letters", { tenant })).body.items as any[];
    expect(dead).toMatchObject({ status: "dead_letter", deadLetterCode: "HUB-0117", lastStatus: 422 });
    expect(dead.responseExcerpt).toContain("MIG-XREF-UNRESOLVED");

    migration.unresolved.clear(); // the customer was imported in the meantime
    expect((await hub.call("POST", `/v1/hub/deliveries/${dead.id}:replay`, { tenant })).status).toBe(202);
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(1);
    expect((await deliveries(tenant))[0]).toMatchObject({ status: "delivered", legacyKey: expect.stringMatching(/^SO-/) });
  });

  test("a render outage retries; an xref outage after the push retries the push (same key) and records the legacy key", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    await subscribe(tenant);
    migration.renderStatus = 503;
    await consume(message(confirmed(tenant)));
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(0);
    expect((await deliveries(tenant))[0]).toMatchObject({ status: "pending", nextRetryAt: T0 + 10 });
    migration.renderStatus = null;
    migration.xrefStatus = 503;
    now = T0 + 10;
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(1);
    expect((await deliveries(tenant))[0]).toMatchObject({ status: "pending", lastStatus: 503 });
    migration.xrefStatus = null;
    now = T0 + 10 + 60;
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(2);
    expect(erp.records.size).toBeGreaterThan(0);
    expect(new Set(erp.hits.map((h) => h.headers["idempotency-key"])).size).toBe(1);
    expect((await deliveries(tenant))[0]).toMatchObject({ status: "delivered", legacyKey: expect.stringMatching(/^SO-/) });
    now = T0;
  });

  test("the legacy keys of a pushed collection's lines are read per element, recorded with the header in one xref call, and the render's lookups are kept for audit", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    migration.lineRefs = true;
    migration.lookups = [{ name: "Lines.uom", table: "uom_conversion", version: 4 }];
    erp.script = (_n, hit) => {
      const lines = (JSON.parse(hit.body) as { Lines: { LineNum: number }[] }).Lines;
      // The ERP answers its lines in reverse order, so only the correlation field can tell them apart.
      return { status: 201, body: JSON.stringify({ data: { id: "SO-7", lines: lines.map((l) => ({ id: `SOL-${l.LineNum}`, lineNo: l.LineNum })).reverse() } }) };
    };
    await subscribe(tenant, { lineKeyPath: "$.data.lines[*].id", lineCorrelation: { bodyPath: "Lines[*].LineNum", responsePath: "$.data.lines[*].lineNo" } });
    const line = (n: number) => ({ line_id: randomUUID(), product_id: "ab12cd34-0000-4000-8000-000000000002", quantity: String(n), site_id: randomUUID(), warehouse_id: randomUUID(), confirmed_ship_date: "2026-12-01" });
    const e = confirmed(tenant, {}, { lines: [line(1), line(2), line(3)] });
    await consume(message(e));
    await poller().pollOnce();

    const [d] = await deliveries(tenant);
    expect(d).toMatchObject({
      status: "delivered", legacyKey: "SO-7",
      lineKeys: [{ index: 0, legacyKey: "SOL-1" }, { index: 1, legacyKey: "SOL-2" }, { index: 2, legacyKey: "SOL-3" }],
      lookups: [{ name: "Lines.uom", table: "uom_conversion", version: 4 }],
    });
    expect(migration.xrefs.filter((x) => x.eventId === e["id"])).toHaveLength(1);
    const call = migration.xrefs.find((x) => x.eventId === e["id"]) as XrefInput;
    expect(call.legacyKey).toBe("SO-7");
    expect(call.lines).toEqual(d.lineKeys);
    expect(call.refs).toHaveLength(4);
  });

  test("line keys that cannot be matched dead-letter the accepted push without recording anything; replay is allowed (HUB-0117)", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    migration.lineRefs = true;
    erp.script = () => ({ status: 201, body: JSON.stringify({ data: { id: "SO-8", lines: [{ id: "SOL-1" }] } }) });
    await subscribe(tenant, { lineKeyPath: "$.data.lines[*].id" });
    const before = migration.xrefs.length;
    await consume(message(confirmed(tenant, {}, { lines: [{ line_id: randomUUID(), product_id: "ab12cd34-0000-4000-8000-000000000002", quantity: "1" }, { line_id: randomUUID(), product_id: "ab12cd34-0000-4000-8000-000000000002", quantity: "2" }] })));
    await poller().pollOnce();
    const [d] = await deliveries(tenant);
    expect(d).toMatchObject({ status: "dead_letter", deadLetterCode: "HUB-0117", lastStatus: 201 });
    expect(d.responseExcerpt).toContain("1 line keys for 2 pushed elements");
    expect(migration.xrefs).toHaveLength(before);
  });

  test("lineKeyPath is validated on create: it needs responseKeyPath and one [*]", async () => {
    const tenant = randomUUID();
    const body = { kind: "rest-push", name: "x", url: url("/api/o"), patterns: ["dev.passage.sales.order.confirmed.v1"], outboundMappingId: MAPPING, auth: { type: "bearer", token: "tok-123456" } };
    const noKey = await hub.call("POST", "/v1/hub/subscriptions", { tenant }, { ...body, lineKeyPath: "lines[*].id" });
    expect(noKey.status).toBe(400);
    const noStar = await hub.call("POST", "/v1/hub/subscriptions", { tenant }, { ...body, responseKeyPath: "$.id", lineKeyPath: "lines.id" });
    expect(noStar.status).toBe(400);
    const ok = await hub.call("POST", "/v1/hub/subscriptions", { tenant }, { ...body, responseKeyPath: "$.id", lineKeyPath: "lines[*].id" });
    expect(ok.status).toBe(201);
    expect(ok.body.rest.lineKeyPath).toBe("lines[*].id");
  });

  test("an accepted record without a legacy key at responseKeyPath is dead-lettered for an operator", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    await subscribe(tenant, { responseKeyPath: "$.result.number" });
    await consume(message(confirmed(tenant)));
    const before = migration.xrefs.length;
    await poller().pollOnce();
    const [d] = await deliveries(tenant);
    expect(d).toMatchObject({ status: "dead_letter", deadLetterCode: "HUB-0117", lastStatus: 201 });
    expect(d.responseExcerpt).toContain("no legacy key");
    expect(migration.xrefs).toHaveLength(before);
  });

  test("a push whose legacy key conflicts with the one recorded for the event (MIG-XREF-EVENT-CONFLICT) is HUB-0118: never resent, never replayed", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    const sub = await subscribe(tenant);
    const e = confirmed(tenant);
    migration.recorded.set(`${MAPPING}/${e["id"]}`, "SO-EARLIER");
    await consume(message(e));
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(1);
    const [dead] = (await hub.call("GET", "/v1/hub/dead-letters", { tenant })).body.items as any[];
    expect(dead).toMatchObject({ status: "dead_letter", deadLetterCode: "HUB-0118", attempts: 1, lastStatus: 409, legacyKey: expect.stringMatching(/^SO-\d/) });
    expect(dead.responseExcerpt).toMatch(/^pushed \(receiver 201\); not resent\. xref:record 409 MIG-XREF-EVENT-CONFLICT/);
    expect(dead.responseExcerpt).not.toContain(PASSWORD);

    // Neither replay touches it: the ERP already has the record, a resend could only add a duplicate.
    const one = await hub.call("POST", `/v1/hub/deliveries/${dead.id}:replay`, { tenant });
    expect(one.status).toBe(409);
    expect(one.body).toMatchObject({ code: "HUB-0409", detail: expect.stringContaining("HUB-0118") });
    const all = await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:replay`, { tenant }, { from: new Date(T0 * 1000).toISOString() });
    expect(all.body).toMatchObject({ enqueued: 0, truncated: false });
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(1);
    expect((await hub.call("GET", "/v1/hub/dead-letters", { tenant })).body.items).toMatchObject([{ id: dead.id, deadLetterCode: "HUB-0118" }]);
  });

  test("an event with the nil legal entity is dead-lettered before the render (the verifier would answer 401, which retries)", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    await subscribe(tenant);
    const renders = migration.renders.length;
    await consume(message(confirmed(tenant, { legalentityid: "00000000-0000-0000-0000-000000000000" })));
    await poller().pollOnce();
    expect(migration.renders).toHaveLength(renders);
    const [dead] = (await hub.call("GET", "/v1/hub/dead-letters", { tenant })).body.items as any[];
    expect(dead).toMatchObject({ deadLetterCode: "HUB-0117", lastStatus: null, attempts: 1 });
    expect(dead.responseExcerpt).toContain("no legalentityid");
  });

  test("an event without a legal entity is dead-lettered with the reason, never pushed unmapped", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    await subscribe(tenant);
    const renders = migration.renders.length;
    const { legalentityid: _omit, ...bare } = confirmed(tenant);
    await consume(message(bare));
    await poller().pollOnce();
    expect(erp.hits).toHaveLength(0);
    expect(migration.renders).toHaveLength(renders);
    const [dead] = (await hub.call("GET", "/v1/hub/dead-letters", { tenant })).body.items as any[];
    expect(dead).toMatchObject({ deadLetterCode: "HUB-0117", lastStatus: null, attempts: 1 });
    expect(dead.responseExcerpt).toContain("no legalentityid");
  });

  test("echo suppression: an event caused by service:migration-loader is not pushed back to the ERP", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    await subscribe(tenant);
    await consume(message(confirmed(tenant, { actor: "service:migration-loader" })));
    await poller().pollOnce();
    expect(await deliveries(tenant)).toHaveLength(0);
    expect(erp.hits).toHaveLength(0);
    await consume(message(confirmed(tenant, { actor: "service:receiving-ers" })));
    await consume(message(confirmed(tenant)));
    expect(await deliveries(tenant)).toHaveLength(2);
  });

  test("placeholders in the URL are filled from the rendered body and escaped", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    await subscribe(tenant, { url: url("/api/customers/{CustAccount}/orders") });
    await consume(message(confirmed(tenant)));
    await poller().pollOnce();
    // Deliveries left pending by earlier tests (other tenants) are worked by the same pass.
    expect(erp.hits.map((h) => h.url)).toContain("/api/customers/C-c0ff/orders");
  });

  test("a request that would leave the allowlist is refused on every attempt", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    const sub = await subscribe(tenant);
    // The operator narrows the allowlist after the subscription exists: nothing is sent, the delivery retries.
    const narrow = new RestPusher({
      migration,
      exchange: httpExchange({ validate: async (u) => ({ ...(await validateWebhookUrl(u, { allowHttp: true, resolve, egressAllow: new EgressPolicy("10.8.0.0/16") })), address: "127.0.0.1", family: 4 as const }) }),
    });
    await consume(message(confirmed(tenant)));
    await new Poller({ store: fx.store, transport: async () => null, rest: narrow, clock, leaseSeconds: 60 }).pollOnce();
    expect(erp.hits).toHaveLength(0);
    expect((await deliveries(tenant))[0]).toMatchObject({ status: "pending", lastStatus: null, responseExcerpt: "no response from the receiver" });
    expect(sub.id).toBeTruthy();
  });

  test("create and update validation; credentials are write-only and never in a response, a log or a plain column", async () => {
    const tenant = randomUUID();
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const info = vi.spyOn(console, "log").mockImplementation(() => undefined);
    const base = { kind: "rest-push", name: "n", url: url("/api/x"), patterns: ["dev.passage.sales.**"], outboundMappingId: MAPPING, auth: { type: "bearer", token: "bearer-token-0123456789" } };
    const post = (over: Record<string, unknown>) => hub.call("POST", "/v1/hub/subscriptions", { tenant }, { ...base, ...over });

    expect((await post({ secret: "a-signing-secret-1234" })).status).toBe(400);
    expect((await post({ url: "http://{h}.corp.example/x" })).status).toBe(400);
    expect((await post({ url: "http://10.1.1.1/x" })).status).toBe(422); // private, not on the allowlist
    expect((await post({ url: "http://127.0.0.1/x" })).status).toBe(422);
    expect((await post({ auth: { type: "oauth2-client-credentials", tokenUrl: "http://169.254.169.254/token", clientId: "c", clientSecret: "shhhh-secret" } })).status).toBe(422);
    expect((await post({ method: "DELETE" })).status).toBe(400);
    expect((await post({ outboundMappingId: "x" })).status).toBe(400);
    expect((await hub.call("POST", "/v1/hub/subscriptions", { tenant }, { name: "w", url: url("/h"), patterns: ["dev.passage.**"], method: "PUT" })).status).toBe(400);
    expect((await hub.call("POST", "/v1/hub/subscriptions", { tenant, duties: ["hub.delivery.read"] }, base)).status).toBe(403);

    const created = await post({});
    expect(created.status).toBe(201);
    expect(created.body).toMatchObject({ kind: "rest-push", rest: { outboundMappingId: MAPPING, method: "POST", auth: { type: "bearer", hasCredentials: true } } });
    expect(created.body).not.toHaveProperty("secret");
    const id = created.body.id as string;

    const patched = await hub.call("PATCH", `/v1/hub/subscriptions/${id}`, { tenant }, { version: 1, method: "PUT", auth: { type: "api-key", headerName: "X-Api-Key", value: "api-key-value-777" } });
    expect(patched.status, JSON.stringify(patched.body)).toBe(200);
    expect(patched.body).toMatchObject({ version: 2, rest: { method: "PUT", auth: { type: "api-key", headerName: "X-Api-Key", hasCredentials: true } } });
    const patched2 = await hub.call("PATCH", `/v1/hub/subscriptions/${id}`, { tenant }, { version: 2, name: "renamed" });
    expect(patched2.body).toMatchObject({ name: "renamed", rest: { method: "PUT", auth: { type: "api-key" } } });
    expect((await hub.call("PATCH", `/v1/hub/subscriptions/${id}`, { tenant }, { version: 2, name: "stale" })).status).toBe(409);

    const responses = JSON.stringify([
      created.body, patched.body, patched2.body,
      (await hub.call("GET", `/v1/hub/subscriptions/${id}`, { tenant })).body,
      (await hub.call("GET", "/v1/hub/subscriptions", { tenant })).body,
    ]);
    const stored = JSON.stringify((await fx.owner.query("select * from hub_subscription where id = $1", [id])).rows[0]);
    const sealed = (await fx.owner.query("select credentials_sealed from hub_subscription where id = $1", [id])).rows[0].credentials_sealed as Buffer;
    for (const secret of ["bearer-token-0123456789", "api-key-value-777"]) {
      expect(responses).not.toContain(secret);
      expect(stored).not.toContain(secret);
      expect(sealed.includes(Buffer.from(secret))).toBe(false);
      expect(JSON.stringify([log.mock.calls, info.mock.calls])).not.toContain(secret);
    }
    expect(sealed[0]).toBe(1);

    // An ordinary webhook subscription is unaffected by rest settings on PATCH.
    const hook = await hub.call("POST", "/v1/hub/subscriptions", { tenant }, { name: "w", url: url("/h"), patterns: ["dev.passage.**"] });
    expect((await hub.call("PATCH", `/v1/hub/subscriptions/${hook.body.id}`, { tenant }, { version: 1, method: "PUT" })).status).toBe(400);
  });

  test("credentials never reach the log or a stored response even when the ERP echoes them", async () => {
    const tenant = randomUUID();
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    erp.hits.length = 0;
    await subscribe(tenant);
    erp.script = (n, hit) => (n === 1 ? { status: 401, body: `bad credentials: ${String(hit.headers["authorization"])} / ${PASSWORD}` } : undefined);
    await consume(message(confirmed(tenant)));
    await poller().pollOnce();
    const [d] = await deliveries(tenant);
    expect(d.status).toBe("dead_letter");
    expect(d.responseExcerpt).not.toContain(PASSWORD);
    // The echoed Basic header decodes to the password, so it is masked too.
    expect(d.responseExcerpt).not.toContain(Buffer.from(`passage:${PASSWORD}`).toString("base64"));
    expect(d.responseExcerpt).toBe("bad credentials: Basic *** / ***");
    expect(JSON.stringify(log.mock.calls)).not.toContain(PASSWORD);
    expect(errors.map((e) => String((e as Error).message)).join("\n")).not.toContain(PASSWORD);
  });

  test("test send renders the sample event and shows the request; with send it pushes once and records nothing", async () => {
    const tenant = randomUUID();
    erp.hits.length = 0;
    const sub = await subscribe(tenant);
    const sample = confirmed(tenant);
    delete sample["tenantid"];
    const xrefs = migration.xrefs.length;
    const le = { "x-passage-legal-entity": "22222222-3333-4444-8555-666666666666" };
    // The sample's legal entity must be the caller's: another one (or none in scope) is refused, not rendered under the hub's grant.
    expect((await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:test`, { tenant }, { event: sample })).status).toBe(403);
    expect((await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:test`, { tenant }, { event: sample }, { "x-passage-legal-entity": randomUUID() })).status).toBe(403);
    const dry = await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:test`, { tenant }, { event: sample }, le);
    // The render ran for the calling user, not as the hub.
    expect(migration.renders.at(-1)).toMatchObject({ legalEntityId: "22222222-3333-4444-8555-666666666666", actor: "11111111-1111-4111-8111-111111111111" });
    expect(dry.status, JSON.stringify(dry.body)).toBe(200);
    expect(dry.body).toMatchObject({ stage: "built", method: "POST", url: url("/api/salesorders"), body: { CustAccount: "C-c0ff" } });
    expect(erp.hits).toHaveLength(0);
    const sent = await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:test`, { tenant }, { event: sample, send: true }, le);
    expect(sent.body).toMatchObject({ stage: "sent", status: 201, legacyKey: expect.stringMatching(/^SO-/) });
    expect(erp.hits).toHaveLength(1);
    expect(migration.xrefs).toHaveLength(xrefs);
    expect(await deliveries(tenant)).toHaveLength(0);
    migration.unresolved.add("c0ffee00-0000-4000-8000-000000000001");
    const failed = await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:test`, { tenant }, { event: sample }, le);
    expect(failed.body).toMatchObject({ stage: "render", status: 422 });
    // Without a legal entity in the sample, the caller's is used.
    const { legalentityid: _omit, ...unscoped } = sample;
    migration.unresolved.clear();
    expect((await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:test`, { tenant }, { event: unscoped }, le)).body).toMatchObject({ stage: "built" });
    expect((await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:test`, { tenant, duties: ["hub.delivery.read"] }, { event: sample }, le)).status).toBe(403);
    expect((await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:test`, { tenant: randomUUID() }, { event: sample }, le)).status).toBe(404);
  });

  test("tenant isolation: another tenant cannot replay this tenant's dead letter", async () => {
    const [a, b] = [randomUUID(), randomUUID()];
    await subscribe(a);
    erp.script = (n) => (n === erp.hits.length ? { status: 400, body: "no" } : undefined);
    await consume(message(confirmed(a)));
    await poller().pollOnce();
    const [dead] = (await hub.call("GET", "/v1/hub/dead-letters", { tenant: a })).body.items as any[];
    expect((await hub.call("POST", `/v1/hub/deliveries/${dead.id}:replay`, { tenant: b })).status).toBe(404);
    expect((await hub.call("POST", `/v1/hub/deliveries/${randomUUID()}:replay`, { tenant: a })).status).toBe(404);
  });

  test("without a configured rest pusher, creating a rest-push subscription answers 501", async () => {
    const bare = await api({ store: fx.store, guard: { allowHttp: true, resolve, egressAllow }, clock });
    const res = await bare.call("POST", "/v1/hub/subscriptions", { tenant: randomUUID() }, {
      kind: "rest-push", name: "n", url: url("/api/x"), patterns: ["dev.passage.sales.**"], outboundMappingId: MAPPING, auth: { type: "bearer", token: "tok-123456789" },
    });
    expect(res.status).toBe(501);
    bare.server.close();
  });
});
