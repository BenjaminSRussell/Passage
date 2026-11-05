import { randomUUID } from "node:crypto";
import { afterAll, beforeAll, describe, expect, test } from "vitest";

import { quotaDefaults, validateClientId, validateOwner, validateScopes } from "../src/clients.ts";
import { createMessageHandler } from "../src/consumer.ts";
import { httpTransport } from "../src/dispatcher.ts";
import { aggregateTypeOf, envelope, EVENT_TYPES } from "../src/events.ts";
import { Poller } from "../src/poller.ts";
import { api, fixture, guard, loopbackValidate, ownerUrl, receiver, type Api, type Fixture } from "./pgHelper.ts";

const T0 = 1_800_000_000;
// The administrator holds the duties the tests grant to clients: a client can be granted only what its granter holds.
const ADMIN = ["hub.client.write", "hub.delivery.read", "hub.subscription.write", "sales.order.read", "sales.order.write", "receiving.vendor-invoice.post", "identity.user.maintain"];
const EDGE = ["hub.client.consume"];

describe("API client rules", () => {
  test("client ids follow the Keycloak id rule", () => {
    expect(() => validateClientId("acme-erp.prod_1")).not.toThrow();
    for (const bad of ["ab", "-lead", "Upper", "has space", "x".repeat(65)]) {
      expect(() => validateClientId(bad), bad).toThrow(RangeError);
    }
  });
  test("scopes are duty-shaped names, 1 to 50", () => {
    expect(() => validateScopes(["sales.order.read", "receiving.vendor-invoice.post", "identity.user.maintain", "receivables.view", "transport.tracking-event.ingest", "inventory.ownership-change.read"])).not.toThrow();
    expect(() => validateScopes([])).toThrow(RangeError);
    for (const bad of ["a,b", "1abc", " lead", "trail ", ".dot", "x".repeat(101), "semi;colon"]) {
      expect(() => validateScopes([bad]), bad).toThrow(RangeError);
    }
    expect(() => validateScopes(Array.from({ length: 51 }, (_, i) => `a.b${i}`))).toThrow(RangeError);
  });
  test("the owner contact is an e-mail address", () => {
    expect(() => validateOwner("it@acme.example")).not.toThrow();
    expect(() => validateOwner("not an address")).toThrow(RangeError);
  });
  test("the defaults are the FR-35.8 numbers", () => {
    expect(quotaDefaults).toEqual({ quotaPerMin: 600, burst: 100 });
  });
  test("event envelopes are CloudEvents 1.0 and every type follows the naming rule", () => {
    for (const type of EVENT_TYPES) {
      const e = envelope("11111111-1111-4111-8111-111111111111", { type, aggregateId: "y", actor: null, data: {} });
      expect(e).toMatchObject({ specversion: "1.0", type, subject: "y", datacontenttype: "application/json", source: "passage/integration-hub" });
    }
    expect(() => envelope("t", { type: "dev.passage.hub.Bad.v1" as never, aggregateId: "y", actor: null, data: {} })).toThrow();
  });
  test("the outbox aggregate type is the type's entity, so the relay topic is events.Topic's passage.hub.<entity>", () => {
    expect(aggregateTypeOf("dev.passage.hub.webhook.delivered.v1")).toBe("webhook");
    expect(aggregateTypeOf("dev.passage.hub.subscription.paused.v1")).toBe("subscription");
    expect(aggregateTypeOf("dev.passage.hub.apiclient.revoked.v1")).toBe("apiclient");
  });
});

describe.skipIf(ownerUrl === undefined)("API clients on Postgres", () => {
  let fx: Fixture;
  let now = T0;
  let hub: Api;
  const clock = () => now;

  beforeAll(async () => {
    fx = await fixture();
    hub = await api({ store: fx.store, guard, clock });
  });
  afterAll(async () => {
    hub.server.close();
    await fx.close();
  });

  const register = (tenant: string, over: Record<string, unknown> = {}, extra: Record<string, string> = {}) =>
    hub.call("POST", "/v1/hub/clients", { tenant, duties: ADMIN }, {
      clientId: `acme-${randomUUID().slice(0, 8)}`, name: "Acme ERP", scopes: ["sales.order.read"], ownerContact: "it@acme.example", ...over,
    }, extra);
  const consume = (tenant: string, clientId: string, cost?: number) =>
    hub.call("POST", "/v1/hub/clients:consume", { tenant, duties: EDGE }, cost === undefined ? { clientId } : { clientId, cost });
  const events = async (tenant: string) =>
    (await fx.owner.query("select type, aggregate_type, aggregate_id, payload from outbox where tenant_id = $1 order by created_at, id", [tenant])).rows as {
      type: string; aggregate_type: string; aggregate_id: string; payload: { actor?: string; data: Record<string, unknown>; tenantid: string };
    }[];

  test("registers a client with the default quota, lists and reads it", async () => {
    const t = randomUUID();
    const res = await register(t);
    expect(res.status).toBe(201);
    expect(res.body).toMatchObject({ quotaPerMin: 600, burst: 100, status: "active", version: 1, scopes: ["sales.order.read"], sandboxTenantId: null });
    expect((await hub.call("GET", "/v1/hub/clients", { tenant: t })).body.items).toHaveLength(1);
    expect((await hub.call("GET", `/v1/hub/clients/${res.body.id}`, { tenant: t })).body.clientId).toBe(res.body.clientId);
    const [e] = await events(t);
    expect(e).toMatchObject({ type: "dev.passage.hub.apiclient.registered.v1", aggregate_type: "apiclient", aggregate_id: res.body.id });
    expect(e!.payload.data).toMatchObject({ client_id: res.body.clientId, quota_per_min: 600, burst: 100, status: "active" });
    expect(e!.payload.tenantid).toBe(t);
  });

  test("an idempotency key returns the first client and writes one event; a taken client id is 409", async () => {
    const t = randomUUID();
    const first = await register(t, { clientId: "same-client" }, { "idempotency-key": "k1" });
    const again = await register(t, { clientId: "other-client" }, { "idempotency-key": "k1" });
    expect(first.status).toBe(201);
    expect(again.status).toBe(200);
    expect(again.body.id).toBe(first.body.id);
    expect(await events(t)).toHaveLength(1);
    const dup = await register(t, { clientId: "same-client" });
    expect(dup.status).toBe(409);
    expect(dup.body.code).toBe("HUB-0409");
    // The same client id in another tenant is a different client.
    expect((await register(randomUUID(), { clientId: "same-client" })).status).toBe(201);
  });

  test("validation: bad id, scope, owner, quota range and own tenant as sandbox are 400", async () => {
    const t = randomUUID();
    for (const bad of [
      { clientId: "A" }, { scopes: [] }, { scopes: ["a,b"] }, { ownerContact: "nobody" }, { quotaPerMin: 5 }, { quotaPerMin: 100_001 },
      { burst: 0 }, { burst: 1.5 }, { sandboxTenantId: t }, { sandboxTenantId: "nope" }, { extra: 1 },
    ]) {
      const res = await register(t, bad);
      expect(res.status, JSON.stringify(bad)).toBe(400);
    }
    expect((await register(t, { sandboxTenantId: randomUUID() })).status).toBe(201);
    expect((await register(t, { scopes: ["receiving.vendor-invoice.post", "identity.user.maintain"] })).body.scopes).toEqual(["receiving.vendor-invoice.post", "identity.user.maintain"]);
  });

  test("PATCH is versioned, rewrites the quota and writes an updated event; a stale version is 409", async () => {
    const t = randomUUID();
    const c = (await register(t)).body;
    const up = await hub.call("PATCH", `/v1/hub/clients/${c.id}`, { tenant: t, duties: ADMIN }, { version: 1, quotaPerMin: 1200, burst: 200, scopes: ["sales.order.read", "sales.order.write"] });
    expect(up.status).toBe(200);
    expect(up.body).toMatchObject({ version: 2, quotaPerMin: 1200, burst: 200, scopes: ["sales.order.read", "sales.order.write"] });
    expect((await hub.call("PATCH", `/v1/hub/clients/${c.id}`, { tenant: t, duties: ADMIN }, { version: 1, name: "late" })).status).toBe(409);
    expect((await hub.call("PATCH", `/v1/hub/clients/${c.id}`, { tenant: t, duties: ADMIN }, { version: 2, burst: 0 })).status).toBe(400);
    expect((await events(t)).map((e) => e.type)).toEqual(["dev.passage.hub.apiclient.registered.v1", "dev.passage.hub.apiclient.updated.v1"]);
    expect((await hub.call("PATCH", `/v1/hub/clients/${randomUUID()}`, { tenant: t, duties: ADMIN }, { version: 1 })).status).toBe(404);
  });

  test("a caller grants only duties they hold: on register, and for the scopes a PATCH adds", async () => {
    const t = randomUUID();
    const clerk = ["hub.client.write", "hub.delivery.read", "sales.order.read"];
    const denied = await hub.call("POST", "/v1/hub/clients", { tenant: t, duties: clerk }, {
      clientId: `acme-${randomUUID().slice(0, 8)}`, name: "Acme", scopes: ["sales.order.read", "ledger.journal.post", "identity.user.maintain"], ownerContact: "it@acme.example",
    });
    expect(denied.status).toBe(403);
    expect(denied.body.detail).toContain("ledger.journal.post, identity.user.maintain");
    expect(denied.body.detail).not.toContain("sales.order.read,");
    expect((await events(t)).length).toBe(0);
    // Registered by an administrator with more duties; the clerk may keep and drop scopes, not add one.
    const c = (await register(t, { scopes: ["sales.order.read", "sales.order.write"] })).body;
    const patch = (scopes: string[], version: number) => hub.call("PATCH", `/v1/hub/clients/${c.id}`, { tenant: t, duties: clerk }, { version, scopes });
    expect((await patch(["sales.order.read", "sales.order.write", "receiving.vendor-invoice.post"], 1)).status).toBe(403);
    expect((await patch(["sales.order.read", "sales.order.write"], 1)).status).toBe(200);
    expect((await patch(["sales.order.write"], 2)).body.scopes).toEqual(["sales.order.write"]);
    // Other fields never need the scopes: a clerk without them can still change the quota.
    expect((await hub.call("PATCH", `/v1/hub/clients/${c.id}`, { tenant: t, duties: ["hub.client.write"] }, { version: 3, quotaPerMin: 700 })).status).toBe(200);
    expect((await hub.call("PATCH", `/v1/hub/clients/${randomUUID()}`, { tenant: t, duties: clerk }, { version: 1, scopes: ["sales.order.read"] })).status).toBe(404);
  });

  test("revoke is idempotent, writes one event, blocks changes and quota", async () => {
    const t = randomUUID();
    const c = (await register(t)).body;
    expect((await consume(t, c.clientId)).status).toBe(200);
    const first = await hub.call("POST", `/v1/hub/clients/${c.id}:revoke`, { tenant: t, duties: ADMIN });
    expect(first.body).toMatchObject({ status: "revoked", version: 2 });
    expect(first.body.revokedAt).not.toBeNull();
    const second = await hub.call("POST", `/v1/hub/clients/${c.id}:revoke`, { tenant: t, duties: ADMIN });
    expect(second.status).toBe(200);
    expect(second.body.version).toBe(2);
    expect((await events(t)).filter((e) => e.type.endsWith("revoked.v1"))).toHaveLength(1);
    expect((await hub.call("PATCH", `/v1/hub/clients/${c.id}`, { tenant: t, duties: ADMIN }, { version: 2, name: "x" })).status).toBe(409);
    const spent = await consume(t, c.clientId);
    expect(spent.status).toBe(403);
    expect((await hub.call("POST", `/v1/hub/clients/${randomUUID()}:revoke`, { tenant: t, duties: ADMIN })).status).toBe(404);
  });

  test("AC-35.6: 700 requests at once against 600/min, burst 100 give at least 100 429s with Retry-After", async () => {
    const t = randomUUID();
    const c = (await register(t)).body;
    let allowed = 0;
    let throttled = 0;
    for (let i = 0; i < 700; i += 1) {
      const res = await fetch(`${hub.base}/v1/hub/clients:consume`, {
        method: "POST",
        headers: { "x-passage-tenant": t, "x-passage-user": "11111111-1111-4111-8111-111111111111", "x-passage-duties": EDGE.join(","), "content-type": "application/json" },
        body: JSON.stringify({ clientId: c.clientId }),
      });
      if (res.status === 200) {
        allowed += 1;
      } else {
        expect(res.status).toBe(429);
        expect(Number(res.headers.get("retry-after"))).toBeGreaterThanOrEqual(1);
        expect(((await res.json()) as { code: string }).code).toBe("HUB-0113");
        throttled += 1;
      }
    }
    expect(allowed).toBe(100);
    expect(throttled).toBe(600);
    const usage = await hub.call("GET", `/v1/hub/clients/${c.id}/usage?minutes=5`, { tenant: t });
    const total = (usage.body.minutes as { allowed: number; throttled: number }[]).reduce((a, m) => ({ allowed: a.allowed + m.allowed, throttled: a.throttled + m.throttled }), { allowed: 0, throttled: 0 });
    expect(total).toEqual({ allowed: 100, throttled: 600 });
    // Ten seconds refill 100 tokens (600/min), capped at the burst.
    now += 10;
    expect((await consume(t, c.clientId, 100)).status).toBe(200);
    expect((await consume(t, c.clientId)).status).toBe(429);
    now += 3600;
  });

  test("concurrent requests of one client never overspend the bucket", async () => {
    const t = randomUUID();
    const c = (await register(t, { quotaPerMin: 10, burst: 20 })).body;
    const results = await Promise.all(Array.from({ length: 60 }, () => consume(t, c.clientId)));
    expect(results.filter((r) => r.status === 200)).toHaveLength(20);
    expect(results.filter((r) => r.status === 429)).toHaveLength(40);
  });

  test("cost above the burst is 422, a cost of 0 or a fraction is 400, an unknown client is 404", async () => {
    const t = randomUUID();
    const c = (await register(t, { burst: 5 })).body;
    expect((await consume(t, c.clientId, 6)).status).toBe(422);
    expect((await consume(t, c.clientId, 0)).status).toBe(400);
    expect((await consume(t, c.clientId, 1.5)).status).toBe(400);
    expect((await consume(t, "no-such-client")).status).toBe(404);
    const ok = await consume(t, c.clientId, 5);
    expect(ok.body).toMatchObject({ allowed: true, remaining: 0, limitPerMin: 600, burst: 5 });
  });

  test("tenant A cannot read, change, revoke, spend or see the usage of tenant B's client", async () => {
    const [a, b] = [randomUUID(), randomUUID()];
    const c = (await register(a)).body;
    expect((await hub.call("GET", "/v1/hub/clients", { tenant: b })).body.items).toEqual([]);
    expect((await hub.call("GET", `/v1/hub/clients/${c.id}`, { tenant: b })).status).toBe(404);
    expect((await hub.call("PATCH", `/v1/hub/clients/${c.id}`, { tenant: b, duties: ADMIN }, { version: 1, name: "x" })).status).toBe(404);
    expect((await hub.call("POST", `/v1/hub/clients/${c.id}:revoke`, { tenant: b, duties: ADMIN })).status).toBe(404);
    expect((await hub.call("GET", `/v1/hub/clients/${c.id}/usage`, { tenant: b })).status).toBe(404);
    expect((await consume(b, c.clientId)).status).toBe(404);
    expect(await events(b)).toEqual([]);
  });

  test("the application role sees nothing without a tenant", async () => {
    for (const table of ["hub_api_client", "hub_client_bucket", "hub_client_usage", "outbox"]) {
      const rows = await fx.store.pool.query(`select count(*)::int as n from ${table}`).then((r) => r.rows[0].n, () => 0);
      expect(rows, table).toBe(0);
    }
  });

  test("duties and identity are enforced on every client route", async () => {
    const t = randomUUID();
    const c = (await register(t)).body;
    expect((await fetch(`${hub.base}/v1/hub/clients`)).status).toBe(401);
    expect((await hub.call("POST", "/v1/hub/clients", { tenant: t, duties: ["hub.delivery.read"] }, {})).status).toBe(403);
    expect((await hub.call("GET", "/v1/hub/clients", { tenant: t, duties: ["hub.client.write"] })).status).toBe(403);
    expect((await hub.call("GET", `/v1/hub/clients/${c.id}`, { tenant: t, duties: [] })).status).toBe(403);
    expect((await hub.call("PATCH", `/v1/hub/clients/${c.id}`, { tenant: t, duties: ["hub.delivery.read"] }, { version: 1 })).status).toBe(403);
    expect((await hub.call("POST", `/v1/hub/clients/${c.id}:revoke`, { tenant: t, duties: ["hub.delivery.read"] })).status).toBe(403);
    expect((await hub.call("GET", `/v1/hub/clients/${c.id}/usage`, { tenant: t, duties: [] })).status).toBe(403);
    // Spending quota is the edge's duty, not the administrator's.
    expect((await hub.call("POST", "/v1/hub/clients:consume", { tenant: t, duties: ADMIN }, { clientId: c.clientId })).status).toBe(403);
    expect((await hub.call("DELETE", "/v1/hub/clients", { tenant: t })).status).toBe(405);
  });

  test("subscription lifecycle and delivery outcomes write outbox events without the URL or secret", async () => {
    const t = randomUUID();
    const recv = await receiver((hit) => (hit === 1 ? 410 : 200));
    try {
      const sub = (await hub.call("POST", "/v1/hub/subscriptions", { tenant: t, duties: ADMIN }, { name: "ERP sync", url: recv.url, patterns: ["dev.passage.sales.**"] })).body;
      await hub.call("PATCH", `/v1/hub/subscriptions/${sub.id}`, { tenant: t, duties: ADMIN }, { version: 1, name: "ERP sync 2" });
      const handler = createMessageHandler({ store: fx.store, dlqTopic: "hub.dlq", clock, deadLetter: async () => undefined });
      const send = (id: string) =>
        handler({
          topic: "t", partition: 0, offset: "1", key: Buffer.from("o1"),
          value: Buffer.from(JSON.stringify({ specversion: "1.0", id, source: "/s", type: "dev.passage.sales.order.released.v1", subject: "o1", time: new Date(now * 1000).toISOString(), datacontenttype: "application/json", data: {}, tenantid: t })),
        });
      const poller = new Poller({ store: fx.store, transport: httpTransport({ validate: loopbackValidate }), clock, leaseSeconds: 60 });
      await send("ev-1");
      await poller.pollOnce(); // 410: the subscription pauses
      await hub.call("POST", `/v1/hub/subscriptions/${sub.id}:resume`, { tenant: t, duties: ADMIN });
      now += 100_000;
      await poller.pollOnce(); // delivered
      await hub.call("DELETE", `/v1/hub/subscriptions/${sub.id}`, { tenant: t, duties: ADMIN });
      const types = (await events(t)).map((e) => e.type);
      expect(types).toEqual([
        "dev.passage.hub.subscription.created.v1",
        "dev.passage.hub.subscription.updated.v1",
        "dev.passage.hub.subscription.paused.v1",
        "dev.passage.hub.subscription.resumed.v1",
        "dev.passage.hub.webhook.delivered.v1",
        "dev.passage.hub.subscription.deleted.v1",
      ]);
      const all = JSON.stringify(await events(t));
      expect(all).not.toContain(recv.url);
      expect(all).not.toContain(sub.secret);
      expect((await events(t))[4]!.payload.data).toMatchObject({ subscription_id: sub.id, event_id: "ev-1", attempts: 2 });
      expect((await events(t)).map((e) => e.aggregate_type)).toEqual(["subscription", "subscription", "subscription", "subscription", "webhook", "subscription"]);
    } finally {
      recv.server.close();
    }
  });

  test("a dead-lettered delivery writes webhook.deadlettered", async () => {
    const t = randomUUID();
    const recv = await receiver(() => 500);
    try {
      const sub = (await hub.call("POST", "/v1/hub/subscriptions", { tenant: t, duties: ADMIN }, { name: "down", url: recv.url, patterns: ["dev.passage.sales.**"] })).body;
      const handler = createMessageHandler({ store: fx.store, dlqTopic: "hub.dlq", clock, deadLetter: async () => undefined });
      await handler({
        topic: "t", partition: 0, offset: "1", key: Buffer.from("o1"),
        value: Buffer.from(JSON.stringify({ specversion: "1.0", id: "ev-dead", source: "/s", type: "dev.passage.sales.order.released.v1", subject: "o1", time: new Date(now * 1000).toISOString(), datacontenttype: "application/json", data: {}, tenantid: t })),
      });
      const poller = new Poller({ store: fx.store, transport: httpTransport({ validate: loopbackValidate }), clock, leaseSeconds: 60 });
      for (let i = 0; i < 12; i += 1) {
        await poller.pollOnce();
        now += 86_400;
      }
      const dead = (await events(t)).filter((e) => e.type === "dev.passage.hub.webhook.deadlettered.v1");
      expect(dead).toHaveLength(1);
      expect(dead[0]!.payload.data).toMatchObject({ subscription_id: sub.id, event_id: "ev-dead", attempts: 9, code: "HUB-0115", last_status: 500 });
    } finally {
      recv.server.close();
    }
  });
  test("delivering the hub's own outcome event writes no further outcome event (no feedback loop)", async () => {
    const t = randomUUID();
    const recv = await receiver(() => 200);
    try {
      await hub.call("POST", "/v1/hub/subscriptions", { tenant: t, duties: ADMIN }, { name: "hub outcomes", url: recv.url, patterns: ["dev.passage.hub.**"] });
      const handler = createMessageHandler({ store: fx.store, dlqTopic: "hub.dlq", clock, deadLetter: async () => undefined });
      await handler({
        topic: "passage.hub.webhook", partition: 0, offset: "1", key: Buffer.from("s1"),
        value: Buffer.from(JSON.stringify({ specversion: "1.0", id: "outcome-1", source: "passage/integration-hub", type: "dev.passage.hub.webhook.delivered.v1", subject: "s1", time: new Date(now * 1000).toISOString(), datacontenttype: "application/json", data: { subscription_id: randomUUID(), event_id: "x", attempts: 1 }, tenantid: t })),
      });
      const poller = new Poller({ store: fx.store, transport: httpTransport({ validate: loopbackValidate }), clock, leaseSeconds: 60 });
      await poller.pollOnce();
      expect(recv.hits).toHaveLength(1);
      expect((await events(t)).map((e) => e.type)).toEqual(["dev.passage.hub.subscription.created.v1"]);
    } finally {
      recv.server.close();
    }
  });
});
