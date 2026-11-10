import { randomUUID } from "node:crypto";
import { afterAll, beforeAll, describe, expect, test } from "vitest";

import { createMessageHandler, parseEvent, type DeadLetterRecord, type KafkaMessageInput } from "../src/consumer.ts";
import { httpTransport } from "../src/dispatcher.ts";
import { Poller } from "../src/poller.ts";
import { SECRET_FIELD } from "../src/pgStore.ts";
import { verify } from "../src/webhook.ts";
import { api, fixture, guard, loopbackValidate, ownerUrl, receiver, testKeys, type Api, type Fixture, type Receiver } from "./pgHelper.ts";

const T0 = 1_800_000_000;

function event(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    specversion: "1.0",
    id: randomUUID(),
    source: "/passage/sales",
    type: "dev.passage.sales.order.released.v1",
    subject: "order-1",
    time: new Date(T0 * 1000).toISOString(),
    datacontenttype: "application/json",
    data: { orderNo: "SO-1" },
    ...over,
  };
}

function message(e: unknown, offset = "7"): KafkaMessageInput {
  return { topic: "passage.sales.order", partition: 0, offset, key: Buffer.from("order-1"), value: Buffer.from(typeof e === "string" ? e : JSON.stringify(e)) };
}

describe("parseEvent", () => {
  const tenantid = randomUUID();
  test("accepts a CloudEvent and falls back to the offset for the sequence", () => {
    const out = parseEvent(message(event({ tenantid })));
    expect(out).toMatchObject({ tenantId: tenantid, event: { type: "dev.passage.sales.order.released.v1", aggregateId: "order-1", sequence: 7, occurredAt: T0 } });
  });
  test.each([
    ["not json", "{nope"],
    ["array", "[]"],
    ["wrong specversion", event({ tenantid, specversion: "0.3" })],
    ["no id", event({ tenantid, id: "" })],
    ["bad type", event({ tenantid, type: "Order Released" })],
    ["no tenant", event()],
    ["nil tenant", event({ tenantid: "00000000-0000-0000-0000-000000000000" })],
    ["bad time", event({ tenantid, time: "yesterday" })],
    ["bad sequence", event({ tenantid, passagesequence: -1 })],
  ])("rejects %s", (_name, bad) => {
    expect(parseEvent(message(bad))).toHaveProperty("reason");
  });
  test("rejects an empty value", () => {
    expect(parseEvent({ ...message("x"), value: null })).toHaveProperty("reason");
  });
});

describe.skipIf(ownerUrl === undefined)("integration hub on Postgres", () => {
  let fx: Fixture;
  let now = T0;
  const clock = () => now;
  let hub: Api;
  const recv: Receiver[] = [];
  const transport = httpTransport({ validate: loopbackValidate });

  beforeAll(async () => {
    fx = await fixture();
    hub = await api({ store: fx.store, guard, clock });
  });
  afterAll(async () => {
    hub.server.close();
    for (const r of recv) r.server.close();
    await fx.close();
  });

  async function subscribe(tenant: string, r: Receiver, patterns = ["dev.passage.sales.**"]) {
    const res = await hub.call("POST", "/v1/hub/subscriptions", { tenant }, { name: "ERP sync", url: r.url, patterns });
    expect(res.status).toBe(201);
    return res.body as { id: string; secret: string; version: number };
  }
  async function newReceiver(respond?: (hit: number) => number) {
    const r = await receiver(respond);
    recv.push(r);
    return r;
  }
  const poller = (over: Partial<ConstructorParameters<typeof Poller>[0]> = {}) => new Poller({ store: fx.store, transport, clock, leaseSeconds: 60, ...over });
  const consumerFor = (dead: DeadLetterRecord[] = []) =>
    createMessageHandler({ store: fx.store, dlqTopic: "hub.dlq", clock, deadLetter: async (r) => void dead.push(r) });

  test("tenant A cannot list, read, change or delete tenant B's subscriptions", async () => {
    const [a, b] = [randomUUID(), randomUUID()];
    const r = await newReceiver();
    const subA = await subscribe(a, r);
    expect((await hub.call("GET", "/v1/hub/subscriptions", { tenant: b })).body.items).toEqual([]);
    expect((await hub.call("GET", "/v1/hub/subscriptions", { tenant: a })).body.items).toHaveLength(1);
    expect((await hub.call("GET", `/v1/hub/subscriptions/${subA.id}`, { tenant: b })).status).toBe(404);
    expect((await hub.call("PATCH", `/v1/hub/subscriptions/${subA.id}`, { tenant: b }, { version: 1, name: "x" })).status).toBe(404);
    expect((await hub.call("DELETE", `/v1/hub/subscriptions/${subA.id}`, { tenant: b })).status).toBe(404);
    expect((await hub.call("POST", `/v1/hub/subscriptions/${subA.id}:resume`, { tenant: b })).status).toBe(404);
    expect((await hub.call("GET", "/v1/hub/deliveries", { tenant: b })).body.items).toEqual([]);
  });

  test("the application role sees nothing without a tenant", async () => {
    for (const table of ["hub_subscription", "hub_delivery", "hub_attempt"]) {
      // Fails closed: either no rows, or an error because no tenant is set.
      const rows = await fx.store.pool.query(`select count(*)::int as n from ${table}`).then((r) => r.rows[0].n, () => 0);
      expect(rows, table).toBe(0);
    }
  });

  test("identity and duties are enforced", async () => {
    const t = randomUUID();
    expect((await fetch(`${hub.base}/v1/hub/subscriptions`)).status).toBe(401);
    expect((await hub.call("GET", "/v1/hub/subscriptions", { tenant: "00000000-0000-0000-0000-000000000000" })).status).toBe(401);
    expect((await hub.call("GET", "/v1/hub/subscriptions", { tenant: t, duties: ["hub.subscription.write"] })).status).toBe(403);
    expect((await hub.call("POST", "/v1/hub/subscriptions", { tenant: t, duties: ["hub.delivery.read"] }, {})).status).toBe(403);
    expect((await hub.call("GET", "/v1/hub/deliveries", { tenant: t, duties: [] })).status).toBe(403);
    expect((await hub.call("GET", "/v1/hub/dead-letters", { tenant: t, duties: ["hub.subscription.write"] })).status).toBe(403);
    expect((await hub.call("POST", "/v1/hub/subscriptions/" + randomUUID() + ":replay", { tenant: t, duties: ["hub.delivery.read"] }, {})).status).toBe(403);
  });

  test("a private or metadata URL is refused at create and update", async () => {
    const t = randomUUID();
    const strict = await api({ store: fx.store }); // production guard: real resolver, https only
    try {
      for (const url of ["https://169.254.169.254/latest/meta-data", "https://10.0.0.5/hook", "https://127.0.0.1/x", "https://[::1]/x", "http://example.com/x", "https://user:pw@example.com/x"]) {
        const res = await strict.call("POST", "/v1/hub/subscriptions", { tenant: t }, { name: "bad", url, patterns: ["dev.passage.sales.**"] });
        expect(res.status, url).toBe(422);
        expect(res.body.code).toBe("HUB-0422");
      }
    } finally {
      strict.server.close();
    }
    const r = await newReceiver();
    const sub = await subscribe(t, r);
    const patch = await hub.call("PATCH", `/v1/hub/subscriptions/${sub.id}`, { tenant: t }, { version: 1, url: "https://169.254.169.254/" });
    expect(patch.status).toBe(422);
    expect((await hub.call("GET", "/v1/hub/subscriptions", { tenant: t })).body.items).toHaveLength(1);
  });

  test("create validates input, is idempotent, hides the secret afterwards, and patch is versioned", async () => {
    const t = randomUUID();
    const r = await newReceiver();
    const bad = (b: unknown) => hub.call("POST", "/v1/hub/subscriptions", { tenant: t }, b);
    expect((await bad({ name: "x", url: r.url, patterns: ["**.order"] })).status).toBe(400);
    expect((await bad({ name: "x", url: r.url, patterns: [] })).status).toBe(400);
    expect((await bad({ name: "x", url: r.url, patterns: ["a.b"], extra: 1 })).status).toBe(400);
    expect((await bad({ name: "x", url: r.url, patterns: ["a.b"], secret: "short" })).status).toBe(400);
    const body = { name: "ERP", url: r.url, patterns: ["dev.passage.sales.**"] };
    const first = await hub.call("POST", "/v1/hub/subscriptions", { tenant: t }, body, { "idempotency-key": "k1" });
    const again = await hub.call("POST", "/v1/hub/subscriptions", { tenant: t }, body, { "idempotency-key": "k1" });
    expect(first.status).toBe(201);
    expect(first.body.secret).toMatch(/^whsec_/);
    expect(again.status).toBe(200);
    expect(again.body.id).toBe(first.body.id);
    expect(again.body.secret).toBeUndefined();
    const got = await hub.call("GET", `/v1/hub/subscriptions/${first.body.id}`, { tenant: t });
    expect(got.body.secret).toBeUndefined();
    const ok = await hub.call("PATCH", `/v1/hub/subscriptions/${first.body.id}`, { tenant: t }, { version: 1, name: "ERP 2", patterns: ["dev.passage.billing.**"] });
    expect(ok.body).toMatchObject({ name: "ERP 2", version: 2, patterns: ["dev.passage.billing.**"] });
    const stale = await hub.call("PATCH", `/v1/hub/subscriptions/${first.body.id}`, { tenant: t }, { version: 1, name: "ERP 3" });
    expect(stale.status).toBe(409);
    expect(stale.body.code).toBe("HUB-0409");
  });

  test("an event from a fake source is delivered to a localhost receiver, signed, once, even when the message repeats", async () => {
    const t = randomUUID();
    const r = await newReceiver();
    const sub = await subscribe(t, r);
    const other = await newReceiver();
    await subscribe(t, other, ["dev.passage.billing.**"]);
    const handle = consumerFor();
    const e = event({ tenantid: t });
    await handle(message(e));
    await handle(message(e)); // Kafka redelivery after a crash before the commit
    await poller().pollOnce();
    expect(r.hits).toHaveLength(1);
    expect(other.hits).toHaveLength(0);
    const hit = r.hits[0]!;
    expect(JSON.parse(hit.body)).toMatchObject({ id: e["id"], type: e["type"], subject: "order-1", data: { orderNo: "SO-1" } });
    expect(verify(hit.headers["passage-signature"] as string, sub.secret, hit.body, now)).toBe(true);
    await poller().pollOnce();
    expect(r.hits).toHaveLength(1);
    const list = await hub.call("GET", "/v1/hub/deliveries?status=delivered", { tenant: t });
    expect(list.body.items).toHaveLength(1);
    expect(list.body.items[0]).toMatchObject({ subscriptionId: sub.id, status: "delivered", attempts: 1, lastStatus: 200 });
    expect((await hub.call("GET", "/v1/hub/deliveries?status=pending", { tenant: t })).body.items).toEqual([]); // the billing subscription never matched
  });

  test("a malformed event goes to the DLQ topic and nothing is enqueued; a failing DLQ publish is retried, not lost", async () => {
    const t = randomUUID();
    const r = await newReceiver();
    await subscribe(t, r);
    const dead: DeadLetterRecord[] = [];
    await consumerFor(dead)(message("{nope", "12"));
    expect(dead).toHaveLength(1);
    expect(dead[0]).toMatchObject({ topic: "hub.dlq", headers: { "x-dlq-reason": "not JSON", "x-dlq-source-offset": "12", "x-dlq-source-topic": "passage.sales.order" } });
    await poller().pollOnce();
    expect(r.hits).toHaveLength(0);
    const failing = createMessageHandler({ store: fx.store, dlqTopic: "hub.dlq", clock, deadLetter: async () => { throw new Error("broker down"); } });
    await expect(failing(message("{nope"))).rejects.toThrow("broker down");
  });

  test("a kill between send and record, then a restart, redelivers the event", async () => {
    const t = randomUUID();
    const r = await newReceiver();
    await subscribe(t, r);
    await consumerFor()(message(event({ tenantid: t })));
    const killed = poller({
      transport: async (url, headers, body) => {
        await transport(url, headers, body); // the receiver got it and answered 200 ...
        throw new Error("SIGKILL"); // ... but the hub died before recording it
      },
    });
    await expect(killed.pollOnce()).resolves.toBe(0); // the run failed, nothing was recorded
    expect(r.hits).toHaveLength(1);
    expect((await hub.call("GET", "/v1/hub/deliveries?status=pending", { tenant: t })).body.items).toHaveLength(1);
    now += 200; // the dead replica's lease has expired
    await poller().pollOnce(); // restart
    expect(r.hits).toHaveLength(2);
    expect(r.hits[1]!.body).toBe(r.hits[0]!.body);
    expect((await hub.call("GET", "/v1/hub/deliveries?status=delivered", { tenant: t })).body.items).toHaveLength(1);
  });

  test("failures retry on the schedule and 9 failed attempts dead-letter (HUB-0115)", async () => {
    const t = randomUUID();
    const r = await newReceiver(() => 500);
    await subscribe(t, r);
    await consumerFor()(message(event({ tenantid: t })));
    const start = now;
    const times: number[] = [];
    for (let i = 0; i < 12; i += 1) {
      await poller().pollOnce();
      times.push(r.hits.length);
      now += 86_400;
    }
    expect(r.hits).toHaveLength(9);
    const dead = await hub.call("GET", "/v1/hub/dead-letters", { tenant: t });
    expect(dead.body.items).toHaveLength(1);
    expect(dead.body.items[0]).toMatchObject({ status: "dead_letter", deadLetterCode: "HUB-0115", attempts: 9, lastStatus: 500 });
    expect((await hub.call("GET", "/v1/hub/deliveries?status=pending", { tenant: t })).body.items).toHaveLength(0);
    expect(now).toBeGreaterThan(start);
  });

  test("retries are not attempted before next_retry_at", async () => {
    const t = randomUUID();
    const r = await newReceiver((hit) => (hit === 1 ? 503 : 200));
    await subscribe(t, r);
    await consumerFor()(message(event({ tenantid: t })));
    await poller().pollOnce();
    expect(r.hits).toHaveLength(1);
    now += 5;
    await poller().pollOnce();
    expect(r.hits).toHaveLength(1);
    now += 6; // 10 s schedule step
    await poller().pollOnce();
    expect(r.hits).toHaveLength(2);
    const list = await hub.call("GET", "/v1/hub/deliveries", { tenant: t });
    expect(list.body.items[0]).toMatchObject({ status: "delivered", attempts: 2 });
  });

  test("410 pauses the subscription, :resume reactivates it and the event is then delivered", async () => {
    const t = randomUUID();
    const r = await newReceiver((hit) => (hit === 1 ? 410 : 200));
    const sub = await subscribe(t, r);
    await consumerFor()(message(event({ tenantid: t })));
    await poller().pollOnce();
    expect(r.hits).toHaveLength(1);
    const paused = await hub.call("GET", `/v1/hub/subscriptions/${sub.id}`, { tenant: t });
    expect(paused.body).toMatchObject({ status: "paused", pausedReason: "HUB-0114" });
    // A paused subscription keeps collecting events; they wait for the resume.
    await consumerFor()(message(event({ tenantid: t, id: randomUUID() }), "8"));
    now += 1000;
    await poller().pollOnce();
    expect(r.hits).toHaveLength(1);
    const resumed = await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:resume`, { tenant: t });
    expect(resumed.status).toBe(200);
    expect(resumed.body).toMatchObject({ status: "active", pausedReason: null });
    await poller().pollOnce();
    expect(r.hits).toHaveLength(3);
    expect((await hub.call("GET", "/v1/hub/deliveries?status=delivered", { tenant: t })).body.items).toHaveLength(2);
    expect((await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:resume`, { tenant: t })).body.status).toBe("active");
  });

  test(":replay re-enqueues recorded events from a timestamp with the replay header", async () => {
    const t = randomUUID();
    const r = await newReceiver();
    const sub = await subscribe(t, r);
    const handle = consumerFor();
    await handle(message(event({ tenantid: t, id: "old", time: new Date((T0 - 1000) * 1000).toISOString(), passagesequence: 1 })));
    await handle(message(event({ tenantid: t, id: "new", time: new Date((T0 + 1000) * 1000).toISOString(), passagesequence: 2 })));
    await poller().pollOnce();
    expect(r.hits).toHaveLength(2);
    const bad = await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:replay`, { tenant: t }, { from: "soon" });
    expect(bad.status).toBe(400);
    const res = await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:replay`, { tenant: t }, { from: new Date(T0 * 1000).toISOString() });
    expect(res).toMatchObject({ status: 202, body: { enqueued: 1 } });
    await poller().pollOnce();
    expect(r.hits).toHaveLength(3);
    expect(r.hits[2]!.headers["ce-passagereplay"]).toBe("true");
    expect(JSON.parse(r.hits[2]!.body).id).toBe("new");
    expect(r.hits[0]!.headers["ce-passagereplay"]).toBeUndefined();
  });

  test("deleting a subscription removes its deliveries; lists page by cursor and validate filters", async () => {
    const t = randomUUID();
    const r = await newReceiver();
    const sub = await subscribe(t, r);
    const handle = consumerFor();
    for (let i = 0; i < 3; i += 1) await handle(message(event({ tenantid: t }), String(i)));
    const p1 = await hub.call("GET", "/v1/hub/deliveries?limit=2", { tenant: t });
    expect(p1.body.items).toHaveLength(2);
    const p2 = await hub.call("GET", `/v1/hub/deliveries?limit=2&cursor=${p1.body.nextCursor}`, { tenant: t });
    expect(p2.body.items).toHaveLength(1);
    expect(p2.body.nextCursor).toBeNull();
    expect((await hub.call("GET", "/v1/hub/deliveries?status=bogus", { tenant: t })).status).toBe(400);
    expect((await hub.call("GET", "/v1/hub/deliveries?cursor=zzz", { tenant: t })).status).toBe(400);
    expect((await hub.call("GET", "/v1/hub/deliveries?limit=0", { tenant: t })).status).toBe(400);
    expect((await hub.call("DELETE", `/v1/hub/subscriptions/${sub.id}`, { tenant: t })).status).toBe(204);
    expect((await hub.call("GET", "/v1/hub/deliveries", { tenant: t })).body.items).toEqual([]);
    expect((await hub.call("GET", `/v1/hub/subscriptions/${sub.id}`, { tenant: t })).status).toBe(404);
  });

  test("only one replica works a subscription at a time", async () => {
    const t = randomUUID();
    const r = await newReceiver();
    const sub = await subscribe(t, r);
    const first = await fx.store.claim(t, sub.id, now, 60);
    expect(first).toBe(now + 60);
    expect(await fx.store.claim(t, sub.id, now, 60)).toBeNull();
    const second = await fx.store.claim(t, sub.id, now + 61, 60);
    expect(second).toBe(now + 121);
    // The first replica's late release does not free the lease the second one now holds.
    await fx.store.release(t, sub.id, first as number);
    expect(await fx.store.claim(t, sub.id, now + 62, 60)).toBeNull();
    await fx.store.release(t, sub.id, second as number);
    expect(await fx.store.claim(t, sub.id, now + 62, 60)).toBe(now + 122);
  });

  test("a replica stops sending before its lease lapses; the rest waits for the next pass", async () => {
    const t = randomUUID();
    const r = await newReceiver();
    await subscribe(t, r);
    const handle = consumerFor();
    await handle(message(event({ tenantid: t, subject: "order-a" }), "1"));
    await handle(message(event({ tenantid: t, subject: "order-b" }), "2"));
    const slow = poller({
      leaseSeconds: 20,
      transport: async (url, headers, body) => {
        const status = await transport(url, headers, body);
        now += 10; // a slow receiver: after this send, a second one could outlive the lease
        return status;
      },
    });
    await slow.pollOnce();
    expect(r.hits).toHaveLength(1);
    expect((await hub.call("GET", "/v1/hub/deliveries?status=pending", { tenant: t })).body.items).toHaveLength(1);
    await poller().pollOnce();
    expect(r.hits).toHaveLength(2);
  });

  test("the signing secret is sealed at rest, bound to its tenant, and opened only to sign", async () => {
    const t = randomUUID();
    const r = await newReceiver();
    const sub = await subscribe(t, r);
    const row = await fx.owner.query("select * from hub_subscription where id = $1", [sub.id]);
    expect(row.rows[0].secret).toBeUndefined();
    const sealed = row.rows[0].secret_sealed as Buffer;
    expect(sealed.includes(Buffer.from(sub.secret))).toBe(false);
    expect(testKeys.open(t, SECRET_FIELD, sealed).toString()).toBe(sub.secret);
    expect(() => testKeys.open(randomUUID(), SECRET_FIELD, sealed)).toThrow();
    // A plain-text value cannot be written in its place.
    await expect(
      fx.store.withTenant(t, (c) => c.query("update hub_subscription set secret_sealed = convert_to('whsec_plain', 'utf8') where id = $1", [sub.id])),
    ).rejects.toThrow(/check constraint/);
  });

  test("without field encryption keys, creating a subscription answers 501 and nothing is stored", async () => {
    const t = randomUUID();
    const keyless = await api({ store: fx.keyless, guard, clock });
    try {
      const r = await newReceiver();
      const res = await keyless.call("POST", "/v1/hub/subscriptions", { tenant: t }, { name: "ERP", url: r.url, patterns: ["dev.passage.sales.**"] });
      expect(res.status).toBe(501);
      expect(res.body.code).toBe("HUB-0501");
      expect((await keyless.call("GET", "/v1/hub/subscriptions", { tenant: t })).body.items).toEqual([]);
      // Duties are still checked first.
      expect((await keyless.call("POST", "/v1/hub/subscriptions", { tenant: t, duties: ["hub.delivery.read"] }, {})).status).toBe(403);
    } finally {
      keyless.server.close();
    }
  });

  test("hub_due_subscriptions: definer rights, pinned search_path, ids only, executable by passage_app alone", async () => {
    const fn = await fx.owner.query(
      `select p.prosecdef, p.proconfig, pg_get_function_result(p.oid) as result,
              array(select coalesce(r.rolname, 'PUBLIC') from aclexplode(p.proacl) a left join pg_roles r on r.oid = a.grantee
                     where a.privilege_type = 'EXECUTE' order by 1)::text[] as executors,
              pg_get_userbyid(p.proowner) as owner
         from pg_proc p join pg_namespace n on n.oid = p.pronamespace
        where p.proname = 'hub_due_subscriptions' and n.nspname = $1`,
      [fx.schema],
    );
    const f = fn.rows[0];
    expect(f.prosecdef).toBe(true);
    expect(f.proconfig).toEqual([`search_path=${fx.schema}, pg_temp`]);
    expect(f.result).toBe("TABLE(tenant_id uuid, subscription_id uuid)");
    expect(f.executors).not.toContain("PUBLIC");
    expect([...f.executors].sort()).toEqual([...new Set([f.owner, "passage_app"])].sort());
    // The owner-only scan policies never apply to the application role.
    const policies = await fx.owner.query(
      `select policyname, roles::text[] as roles from pg_policies where schemaname = $1 and policyname like '%owner_scan'`,
      [fx.schema],
    );
    // hub_delivery and hub_subscription (0001), hub_client_usage for the retention purge (0006), hub_coexistence_adapter
    // for the coexistence sweep (0007).
    expect(policies.rows).toHaveLength(4);
    for (const p of policies.rows) expect(p.roles).not.toContain("passage_app");
  });

  test("a temporary table cannot stand in for hub_delivery inside hub_due_subscriptions", async () => {
    const [a, b] = [randomUUID(), randomUUID()];
    const r = await newReceiver();
    const subA = await subscribe(a, r);
    await consumerFor()(message(event({ tenantid: a })));
    const c = await fx.app.connect();
    try {
      await c.query("create temp table hub_delivery (tenant_id uuid, subscription_id uuid, status text, next_retry_at double precision)");
      await c.query("create temp table hub_subscription (tenant_id uuid, id uuid, status text, lease_until double precision)");
      await c.query("insert into pg_temp.hub_delivery values ($1, $2, 'pending', 0)", [b, subA.id]);
      await c.query("insert into pg_temp.hub_subscription values ($1, $2, 'active', null)", [b, subA.id]);
      const due = await c.query(`select tenant_id, subscription_id from ${fx.schema}.hub_due_subscriptions($1)`, [now]);
      expect(due.rows).toContainEqual({ tenant_id: a, subscription_id: subA.id });
      expect(due.rows).not.toContainEqual({ tenant_id: b, subscription_id: subA.id });
      // Through the function the application role learns ids, never rows: the tables stay tenant-scoped.
      // (no rows, or an error because no tenant is set: either way it fails closed).
      const seen = await c.query(`select count(*)::int as n from ${fx.schema}.hub_delivery`).then((x) => x.rows[0].n, () => 0);
      expect(seen).toBe(0);
    } finally {
      await c.query("discard temp").catch(() => undefined);
      c.release();
    }
  });

  test("deliveries carry the event id as Idempotency-Key on the first send, every retry and a replay", async () => {
    const t = randomUUID();
    const r = await newReceiver((hit) => (hit === 1 ? 500 : 200));
    const sub = await subscribe(t, r);
    const e = event({ tenantid: t });
    await consumerFor()(message(e));
    await poller().pollOnce();
    now += 11;
    await poller().pollOnce();
    expect((await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:replay`, { tenant: t }, { from: new Date((T0 - 10) * 1000).toISOString() })).status).toBe(202);
    await poller().pollOnce();
    expect(r.hits.map((h) => h.headers["idempotency-key"])).toEqual([e["id"], e["id"], e["id"]]);
    expect(r.hits.map((h) => h.headers["ce-passagereplay"])).toEqual([undefined, undefined, "true"]);
  });

  test(":replay stays inside the tenant and the current filter, and honours the cap", async () => {
    const [t, other] = [randomUUID(), randomUUID()];
    const r = await newReceiver();
    const sub = await subscribe(t, r, ["dev.passage.sales.**", "dev.passage.billing.**"]);
    const handle = consumerFor();
    const from = new Date((T0 - 5) * 1000).toISOString();
    for (let i = 0; i < 4; i += 1) {
      await handle(message(event({ tenantid: t, subject: `order-${i}`, passagesequence: i, time: new Date((T0 + i) * 1000).toISOString() }), String(i)));
    }
    await handle(message(event({ tenantid: t, type: "dev.passage.billing.invoice.posted.v1", subject: "inv-1" }), "9"));
    await poller().pollOnce();
    expect(r.hits).toHaveLength(5);

    // Another tenant cannot replay it, even knowing the id.
    expect((await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:replay`, { tenant: other }, { from })).status).toBe(404);
    expect((await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:replay`, { tenant: t }, { from, cap: 0 })).status).toBe(400);
    expect((await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:replay`, { tenant: t }, { from: "2026-13-45" })).status).toBe(400);

    // Narrow the filter: billing events are no longer replayed.
    const patched = await hub.call("PATCH", `/v1/hub/subscriptions/${sub.id}`, { tenant: t }, { version: 1, patterns: ["dev.passage.sales.**"] });
    expect(patched.status).toBe(200);
    const capped = await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:replay`, { tenant: t }, { from, cap: 3 });
    expect(capped).toMatchObject({ status: 202, body: { enqueued: 3, truncated: true } });
    await poller().pollOnce();
    const replayed = r.hits.slice(5).map((h) => JSON.parse(h.body));
    expect(replayed.map((b) => b.subject)).toEqual(["order-0", "order-1", "order-2"]);
    const rest = await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:replay`, { tenant: t }, { from: new Date((T0 + 3) * 1000).toISOString() });
    expect(rest).toMatchObject({ status: 202, body: { enqueued: 1, truncated: false } });
    expect((await hub.call("POST", `/v1/hub/subscriptions/${randomUUID()}:replay`, { tenant: t }, { from })).status).toBe(404);
  });
});
