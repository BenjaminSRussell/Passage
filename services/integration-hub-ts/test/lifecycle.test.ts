// Slice 12 on Postgres: CESQL filters, signing-secret rotation with a grace period, the re-seal job and usage retention.

import { randomBytes, randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { afterAll, beforeAll, describe, expect, test } from "vitest";

import { createMessageHandler } from "../src/consumer.ts";
import { envelopeKeyId, FieldKeys } from "../src/crypt.ts";
import { httpTransport } from "../src/dispatcher.ts";
import { Maintenance, usageRetentionDaysFromEnv } from "../src/maintenance.ts";
import { HubStore } from "../src/pgStore.ts";
import { Poller } from "../src/poller.ts";
import { verify } from "../src/webhook.ts";
import { api, fixture, guard, loopbackValidate, ownerUrl, receiver, testKeys, type Api, type Fixture, type Receiver } from "./pgHelper.ts";

const vectors = JSON.parse(readFileSync(new URL("../testdata/fieldcrypt-vectors.json", import.meta.url), "utf8")) as { keys: string };
/** Sealing under the rotated-out key k2024 (testKeys seals under k2025). */
const oldKeys = FieldKeys.fromEnv({ FIELDCRYPT_KEYS: vectors.keys, FIELDCRYPT_CURRENT_KEY_ID: "k2024" }) as FieldKeys;
/** A key the hub no longer holds at all. */
const lostKeys = new FieldKeys("k2099", new Map([["k2099", randomBytes(32)]]));
const ADMIN = ["hub.delivery.read", "hub.subscription.write"];

describe("usage retention setting", () => {
  test("HUB_USAGE_RETENTION_DAYS defaults to 90 and refuses values the usage page could not live with", () => {
    expect(usageRetentionDaysFromEnv({})).toBe(90);
    expect(usageRetentionDaysFromEnv({ HUB_USAGE_RETENTION_DAYS: "30" })).toBe(30);
    for (const bad of ["1", "0", "-5", "abc", "1.5", "4000"]) {
      expect(() => usageRetentionDaysFromEnv({ HUB_USAGE_RETENTION_DAYS: bad }), bad).toThrow(/HUB_USAGE_RETENTION_DAYS/);
    }
  });
});

describe.skipIf(ownerUrl === undefined)("filters, rotation, re-seal and retention on Postgres", () => {
  let fx: Fixture;
  let hub: Api;
  const recv: Receiver[] = [];
  const realNow = () => Math.floor(Date.now() / 1000);
  const transport = httpTransport({ validate: loopbackValidate });

  beforeAll(async () => {
    fx = await fixture();
    hub = await api({ store: fx.store, guard, clock: realNow });
  });
  afterAll(async () => {
    hub.server.close();
    for (const r of recv) r.server.close();
    await fx.close();
  });

  async function newReceiver() {
    const r = await receiver();
    recv.push(r);
    return r;
  }
  // Read as the application role inside the tenant: the owner role has no policy on outbox (a non-superuser owner sees nothing).
  const events = async (tenant: string) =>
    (await fx.store.withTenant(tenant, (c) => c.query("select type, payload from outbox order by created_at, id"))).rows as { type: string; payload: { data: Record<string, unknown> } }[];
  const handler = (clock: () => number = realNow) => createMessageHandler({ store: fx.store, dlqTopic: "hub.dlq", clock, deadLetter: async () => undefined });
  const publish = (tenant: string, data: unknown, over: Record<string, unknown> = {}) =>
    handler()({
      topic: "t", partition: 0, offset: "1", key: Buffer.from("o1"),
      value: Buffer.from(JSON.stringify({
        specversion: "1.0", id: randomUUID(), source: "passage/sales", type: "dev.passage.sales.order.confirmed.v1", subject: "o1",
        time: new Date().toISOString(), datacontenttype: "application/json", data, tenantid: tenant, ...over,
      })),
    });
  const deliveries = async (tenant: string, subscriptionId: string) =>
    (await hub.call("GET", `/v1/hub/deliveries?subscriptionId=${subscriptionId}`, { tenant, duties: ADMIN })).body.items as { eventId: string; status: string }[];
  const subscribe = async (tenant: string, r: Receiver, extra: Record<string, unknown> = {}) => {
    const res = await hub.call("POST", "/v1/hub/subscriptions", { tenant, duties: ADMIN }, { name: "ERP", url: r.url, patterns: ["dev.passage.sales.**"], ...extra });
    expect(res.status, JSON.stringify(res.body)).toBe(201);
    return res.body as { id: string; secret: string; version: number; filter: string | null };
  };

  describe("CESQL filter", () => {
    test("a subscription takes only the events its filter holds for, and only the ones its patterns match", async () => {
      const t = randomUUID();
      const r = await newReceiver();
      const big = await subscribe(t, r, { filter: "data.currency = 'USD' AND data.qty >= 10" });
      const all = await subscribe(t, r);
      expect(big.filter).toBe("data.currency = 'USD' AND data.qty >= 10");
      expect(all.filter).toBeNull();
      await publish(t, { currency: "USD", qty: 12 });
      await publish(t, { currency: "USD", qty: 3 });
      await publish(t, { currency: "EUR", qty: 99 });
      await publish(t, { qty: 99 }); // currency missing: the filter cannot hold
      await publish(t, { currency: "USD", qty: 50 }, { type: "dev.passage.billing.invoice.posted.v1" }); // filter holds, pattern does not
      expect(await deliveries(t, big.id)).toHaveLength(1);
      expect(await deliveries(t, all.id)).toHaveLength(4);
    });

    test("the filter sees the context attributes, including actor and legal entity", async () => {
      const t = randomUUID();
      const r = await newReceiver();
      const le = randomUUID();
      const sub = await subscribe(t, r, { filter: `legalentityid = '${le}' AND EXISTS actor AND source LIKE 'passage/%' AND subject = 'o1'` });
      await publish(t, {}, { legalentityid: le, actor: "user-1" });
      await publish(t, {}, { legalentityid: le });
      await publish(t, {}, { legalentityid: randomUUID(), actor: "user-1" });
      expect(await deliveries(t, sub.id)).toHaveLength(1);
    });

    test("a stored filter that no longer compiles matches nothing for its subscription and does not fail the event for the others", async () => {
      const t = randomUUID();
      const r = await newReceiver();
      const broken = await subscribe(t, r, { filter: "data.qty > 1" });
      const other = await subscribe(t, r);
      await fx.store.withTenant(t, (c) => c.query("update hub_subscription set event_filter = 'data.qty >' where id = $1", [broken.id]));
      await publish(t, { qty: 5 });
      expect(await deliveries(t, broken.id)).toHaveLength(0);
      expect(await deliveries(t, other.id)).toHaveLength(1);
    });

    test("create and update refuse a filter that does not parse; update sets, changes and clears it", async () => {
      const t = randomUUID();
      const r = await newReceiver();
      for (const bad of ["data.qty =", "1 + 2", 5, "", "x".repeat(1025)]) {
        const res = await hub.call("POST", "/v1/hub/subscriptions", { tenant: t, duties: ADMIN }, { name: "bad", url: r.url, patterns: ["dev.passage.sales.**"], filter: bad });
        expect(res.status, JSON.stringify(bad).slice(0, 40)).toBe(400);
        expect(res.body.code).toBe("HUB-0400");
      }
      expect((await hub.call("POST", "/v1/hub/subscriptions", { tenant: t, duties: ADMIN }, { name: "null", url: r.url, patterns: ["dev.passage.sales.**"], filter: null })).status).toBe(400);
      const sub = await subscribe(t, r);
      const set = await hub.call("PATCH", `/v1/hub/subscriptions/${sub.id}`, { tenant: t, duties: ADMIN }, { version: 1, filter: "data.qty > 5" });
      expect(set.body).toMatchObject({ version: 2, filter: "data.qty > 5" });
      expect((await hub.call("PATCH", `/v1/hub/subscriptions/${sub.id}`, { tenant: t, duties: ADMIN }, { version: 2, filter: "nonsense (" })).status).toBe(400);
      const kept = await hub.call("PATCH", `/v1/hub/subscriptions/${sub.id}`, { tenant: t, duties: ADMIN }, { version: 2, name: "renamed" });
      expect(kept.body).toMatchObject({ version: 3, filter: "data.qty > 5", name: "renamed" });
      const cleared = await hub.call("PATCH", `/v1/hub/subscriptions/${sub.id}`, { tenant: t, duties: ADMIN }, { version: 3, filter: null });
      expect(cleared.body).toMatchObject({ version: 4, filter: null });
      expect((await hub.call("GET", `/v1/hub/subscriptions/${sub.id}`, { tenant: t, duties: ADMIN })).body.filter).toBeNull();
      const types = await events(t);
      expect(types.at(-1)?.payload.data).toMatchObject({ filter: null, version: 4 });
      expect(types.find((e) => e.type.endsWith("created.v1"))?.payload.data).toMatchObject({ filter: null });
    });
  });

  describe("signing secret rotation", () => {
    const rotate = (t: string, id: string, body: unknown, duties = ADMIN) => hub.call("POST", `/v1/hub/subscriptions/${id}:rotate`, { tenant: t, duties }, body);
    const signatureOf = async (t: string, r: Receiver, id: string, at: number) => {
      const before = r.hits.length;
      await publish(t, { n: r.hits.length });
      await new Poller({ store: fx.store, transport, clock: () => at, leaseSeconds: 60 }).pollOnce();
      expect(r.hits.length).toBe(before + 1);
      const hit = r.hits[before]!;
      return { header: hit.headers["passage-signature"] as string, body: hit.body, id };
    };

    test("the new secret is shown once; during the grace period deliveries verify under both secrets, afterwards under the new one only", async () => {
      const t = randomUUID();
      const r = await newReceiver();
      const sub = await subscribe(t, r);
      const res = await rotate(t, sub.id, { version: 1, graceSeconds: 3600 });
      expect(res.status).toBe(200);
      const next = res.body.secret as string;
      expect(next).toMatch(/^whsec_/);
      expect(next).not.toBe(sub.secret);
      expect(res.body).toMatchObject({ version: 2, kind: "webhook" });
      expect(res.body.secretRotatedAt).toEqual(expect.any(String));
      const graceEnd = Math.ceil(Date.parse(res.body.previousSecretExpiresAt as string) / 1000);
      expect(graceEnd - realNow()).toBeGreaterThan(3500);
      expect(graceEnd - realNow()).toBeLessThan(3700);
      // Never returned again.
      const read = (await hub.call("GET", `/v1/hub/subscriptions/${sub.id}`, { tenant: t, duties: ADMIN })).body;
      expect(JSON.stringify(read)).not.toContain(next);
      expect(read.secret).toBeUndefined();

      const during = await signatureOf(t, r, sub.id, realNow());
      expect(during.header.match(/v1=/g)).toHaveLength(2);
      expect(verify(during.header, next, during.body, realNow())).toBe(true);
      expect(verify(during.header, sub.secret, during.body, realNow())).toBe(true);

      const after = await signatureOf(t, r, sub.id, graceEnd + 10);
      expect(after.header.match(/v1=/g)).toHaveLength(1);
      expect(verify(after.header, next, after.body, graceEnd + 10)).toBe(true);
      expect(verify(after.header, sub.secret, after.body, graceEnd + 10)).toBe(false);
    });

    test("a grace period of 0 ends the old secret at once; a supplied secret is used; a second rotation drops the first old secret", async () => {
      const t = randomUUID();
      const r = await newReceiver();
      const sub = await subscribe(t, r);
      const chosen = "whsec_chosen_by_the_caller_1";
      const first = await rotate(t, sub.id, { version: 1, secret: chosen, graceSeconds: 0 });
      expect(first.body.secret).toBe(chosen);
      expect(first.body.previousSecretExpiresAt).toBeNull();
      const one = await signatureOf(t, r, sub.id, realNow());
      expect(one.header.match(/v1=/g)).toHaveLength(1);
      expect(verify(one.header, chosen, one.body, realNow())).toBe(true);

      const second = await rotate(t, sub.id, { version: 2 }); // default grace: a day
      const third = await rotate(t, sub.id, { version: 3 });
      const sig = await signatureOf(t, r, sub.id, realNow());
      expect(sig.header.match(/v1=/g)).toHaveLength(2);
      expect(verify(sig.header, third.body.secret, sig.body, realNow())).toBe(true);
      expect(verify(sig.header, second.body.secret, sig.body, realNow())).toBe(true); // the one just before
      expect(verify(sig.header, chosen, sig.body, realNow())).toBe(false); // two rotations back is gone
    });

    test("a rotation is versioned, needs the write duty, and writes secretrotated without any secret in it", async () => {
      const t = randomUUID();
      const r = await newReceiver();
      const sub = await subscribe(t, r);
      expect((await rotate(t, sub.id, { version: 5 })).status).toBe(409);
      expect((await rotate(t, sub.id, { version: 1 }, ["hub.delivery.read"])).status).toBe(403);
      expect((await rotate(t, sub.id, {})).status).toBe(400);
      for (const bad of [{ graceSeconds: -1 }, { graceSeconds: 604_801 }, { graceSeconds: 1.5 }, { secret: "short" }, { secret: 5 }, { extra: 1 }]) {
        expect((await rotate(t, sub.id, { version: 1, ...bad })).status, JSON.stringify(bad)).toBe(400);
      }
      expect((await rotate(t, randomUUID(), { version: 1 })).status).toBe(404);
      expect((await rotate(randomUUID(), sub.id, { version: 1 })).status).toBe(404); // another tenant's subscription
      const ok = await rotate(t, sub.id, { version: 1 });
      expect(ok.status).toBe(200);
      const log = await events(t);
      const rotated = log.filter((e) => e.type === "dev.passage.hub.subscription.secretrotated.v1");
      expect(rotated).toHaveLength(1);
      expect(rotated[0]!.payload.data).toMatchObject({ subscription_id: sub.id, version: 2 });
      expect(rotated[0]!.payload.data["secret_rotated_at"]).toEqual(expect.any(String));
      const all = JSON.stringify(log);
      expect(all).not.toContain(ok.body.secret);
      expect(all).not.toContain(sub.secret);
    });

    test("a keyless hub answers 501, and rotation does not apply to a rest-push subscription", async () => {
      const t = randomUUID();
      const r = await newReceiver();
      const sub = await subscribe(t, r);
      const keyless = await api({ store: fx.keyless, guard, clock: realNow });
      try {
        expect((await keyless.call("POST", `/v1/hub/subscriptions/${sub.id}:rotate`, { tenant: t, duties: ADMIN }, { version: 1 })).status).toBe(501);
      } finally {
        keyless.server.close();
      }
      const restSub = await fx.store.createSubscription(t, {
        name: "rest", url: "https://erp.example/api", secret: "", patterns: ["dev.passage.sales.**"], createdBy: null, idempotencyKey: null,
        rest: {
          config: { outboundMappingId: randomUUID(), method: "POST", responseKeyPath: "$.id", lineKeyPath: "", onClientError: "dead_letter", auth: { type: "bearer" } },
          credentials: { token: "tok" },
        },
      });
      const res = await hub.call("POST", `/v1/hub/subscriptions/${restSub.subscription.id}:rotate`, { tenant: t, duties: ADMIN }, { version: 1 });
      expect(res.status).toBe(409);
    });
  });

  describe("re-seal after a master key rotation", () => {
    const oldStore = () => new HubStore(fx.app, oldKeys);
    const rawRow = async (id: string) =>
      (await fx.owner.query("select secret_sealed, previous_secret_sealed, previous_secret_expires_at, credentials_sealed, version, updated_at from hub_subscription where id = $1", [id])).rows[0] as {
        secret_sealed: Buffer | null; previous_secret_sealed: Buffer | null; previous_secret_expires_at: Date | null; credentials_sealed: Buffer | null; version: number; updated_at: Date;
      };
    async function legacySubscription(tenant: string, withRest = false) {
      const created = await oldStore().createSubscription(tenant, withRest
        ? {
            name: "rest", url: "https://erp.example/api", secret: "", patterns: ["dev.passage.sales.**"], createdBy: null, idempotencyKey: null,
            rest: { config: { outboundMappingId: randomUUID(), method: "POST", responseKeyPath: "$.id", lineKeyPath: "", onClientError: "dead_letter", auth: { type: "bearer" } }, credentials: { token: "tok-old" } },
          }
        : { name: "old", url: "https://hook.example/x", secret: "whsec_sealed_under_old_key", patterns: ["dev.passage.sales.**"], createdBy: null, idempotencyKey: null });
      return created.subscription;
    }

    test("secrets, previous secrets and rest-push credentials sealed under an old key are sealed again under the current key and still open", async () => {
      const [a, b] = [randomUUID(), randomUUID()];
      const hook = await legacySubscription(a);
      const rest = await legacySubscription(b, true);
      const rotated = await oldStore().rotateSecret(a, hook.id, { version: 1, secret: "whsec_second_secret_old_key", graceSeconds: 3600 });
      expect(rotated).not.toBeNull();
      const before = await rawRow(hook.id);
      expect(envelopeKeyId(before.secret_sealed!)).toBe("k2024");
      expect(envelopeKeyId(before.previous_secret_sealed!)).toBe("k2024");
      expect(envelopeKeyId((await rawRow(rest.id)).credentials_sealed!)).toBe("k2024");

      const out = await fx.store.resealDue(100);
      expect(out.failed).toBe(0);
      expect(out.resealed).toBeGreaterThanOrEqual(2);
      const after = await rawRow(hook.id);
      expect(envelopeKeyId(after.secret_sealed!)).toBe("k2025");
      expect(envelopeKeyId(after.previous_secret_sealed!)).toBe("k2025");
      expect(after.previous_secret_expires_at).toEqual(before.previous_secret_expires_at);
      expect(after.version).toBe(before.version); // nothing a client can see changed
      expect(after.updated_at).toEqual(before.updated_at);
      expect(envelopeKeyId((await rawRow(rest.id)).credentials_sealed!)).toBe("k2025");

      // Same plain text, now under the current key; the store holding only the current key opens it.
      const target = (await fx.store.getTarget(a, hook.id))!;
      expect(fx.store.openSecret(a, target)).toBe("whsec_second_secret_old_key");
      expect(fx.store.openPreviousSecret(a, target, realNow())).toBe("whsec_sealed_under_old_key");
      const restTarget = (await fx.store.getTarget(b, rest.id))!;
      expect(fx.store.openRest(b, restTarget).credentials).toEqual({ token: "tok-old" });
      // Nothing left to do: a second pass touches nothing of these two.
      const again = await fx.owner.query("select count(*)::int as n from hub_reseal_due('k2025', now(), 1000) where subscription_id = any($1)", [[hook.id, rest.id]]);
      expect(again.rows[0].n).toBe(0);
    });

    test("a previous secret past its grace period is removed, a running one is kept", async () => {
      const t = randomUUID();
      const sub = await legacySubscription(t);
      const a = await fx.store.rotateSecret(t, sub.id, { version: 1, secret: "whsec_current_secret_value", graceSeconds: 60 });
      expect(a?.previousSecretExpiresAt).not.toBeNull();
      const early = await fx.store.resealDue(100, new Date());
      expect(early.expired).toBe(0);
      expect((await rawRow(sub.id)).previous_secret_sealed).not.toBeNull();
      const late = await fx.store.resealDue(100, new Date(Date.now() + 120_000));
      expect(late.expired).toBeGreaterThanOrEqual(1);
      const row = await rawRow(sub.id);
      expect(row.previous_secret_sealed).toBeNull();
      expect(row.previous_secret_expires_at).toBeNull();
      expect(envelopeKeyId(row.secret_sealed!)).toBe("k2025");
      const target = (await fx.store.getTarget(t, sub.id))!;
      expect(fx.store.openSecret(t, target)).toBe("whsec_current_secret_value");
    });

    test("a value whose key the hub no longer holds is counted as failed and left as it is; the others are still done", async () => {
      const [lost, fine] = [randomUUID(), randomUUID()];
      const gone = (await new HubStore(fx.app, lostKeys).createSubscription(lost, { name: "lost", url: "https://hook.example/x", secret: "whsec_lost_key_secret_1", patterns: ["dev.passage.sales.**"], createdBy: null, idempotencyKey: null })).subscription;
      const ok = await legacySubscription(fine);
      const out = await fx.store.resealDue(1000);
      expect(out.failed).toBeGreaterThanOrEqual(1);
      expect(envelopeKeyId((await rawRow(gone.id)).secret_sealed!)).toBe("k2099");
      expect(envelopeKeyId((await rawRow(ok.id)).secret_sealed!)).toBe("k2025");
      await fx.store.withTenant(lost, (c) => c.query("delete from hub_subscription where id = $1", [gone.id]));
    });

    test("without keys there is nothing to re-seal with", async () => {
      await expect(fx.keyless.resealDue(10)).rejects.toThrow(/field encryption keys/);
    });

    test("a concurrent rotation is not lost to a re-seal", async () => {
      const t = randomUUID();
      const sub = await legacySubscription(t);
      const [rotation] = await Promise.all([
        fx.store.rotateSecret(t, sub.id, { version: 1, secret: "whsec_raced_secret_value_1", graceSeconds: 0 }),
        fx.store.resealDue(100),
        fx.store.resealDue(100),
      ]);
      expect(rotation).not.toBeNull();
      const target = (await fx.store.getTarget(t, sub.id))!;
      expect(fx.store.openSecret(t, target)).toBe("whsec_raced_secret_value_1");
      expect(testKeys.currentId).toBe("k2025");
      expect(envelopeKeyId(target.sealedSecret!)).toBe("k2025");
    });
  });

  test("hub_reseal_due and hub_purge_client_usage: definer rights, pinned search_path, executable by passage_app alone; owner policies only", async () => {
    const fns = await fx.owner.query(
      `select p.proname, p.prosecdef, p.proconfig, pg_get_userbyid(p.proowner) as owner,
              array(select coalesce(r.rolname, 'PUBLIC') from aclexplode(p.proacl) a left join pg_roles r on r.oid = a.grantee
                     where a.privilege_type = 'EXECUTE' order by 1)::text[] as executors
         from pg_proc p join pg_namespace n on n.oid = p.pronamespace
        where p.proname in ('hub_reseal_due', 'hub_purge_client_usage') and n.nspname = $1 order by p.proname`,
      [fx.schema],
    );
    expect(fns.rows.map((f) => f.proname)).toEqual(["hub_purge_client_usage", "hub_reseal_due"]);
    for (const f of fns.rows) {
      expect(f.prosecdef, f.proname).toBe(true);
      expect(f.proconfig, f.proname).toEqual([`search_path=${fx.schema}, pg_temp`]);
      expect(f.executors, f.proname).not.toContain("PUBLIC");
      expect([...f.executors].sort(), f.proname).toEqual([...new Set([f.owner, "passage_app"])].sort());
    }
    const policies = await fx.owner.query(
      "select policyname, roles::text[] as roles from pg_policies where schemaname = $1 and tablename = 'hub_client_usage' and policyname like '%owner%'",
      [fx.schema],
    );
    expect(policies.rows.map((p) => p.policyname).sort()).toEqual(["hub_client_usage_owner_purge", "hub_client_usage_owner_scan"]);
    for (const p of policies.rows) expect(p.roles).not.toContain("passage_app");
  });

  describe("client usage retention", () => {
    async function clientWithUsage(tenant: string, ages: number[]) {
      const res = await hub.call("POST", "/v1/hub/clients", { tenant, duties: ["hub.client.write", "hub.delivery.read", "sales.order.read"] }, {
        clientId: `acme-${randomUUID().slice(0, 8)}`, name: "Acme", scopes: ["sales.order.read"], ownerContact: "it@acme.example",
      });
      expect(res.status).toBe(201);
      await fx.store.withTenant(tenant, async (c) => {
        for (const days of ages) {
          await c.query(
            "insert into hub_client_usage (tenant_id, api_client_id, minute, allowed, throttled) values ($1, $2, date_trunc('minute', now() - make_interval(days => $3)), 5, 1)",
            [tenant, res.body.id, days],
          );
        }
      });
      return res.body.id as string;
    }
    const remaining = async (tenant: string) =>
      (await fx.owner.query("select count(*)::int as n from hub_client_usage where tenant_id = $1", [tenant])).rows[0].n as number;

    test("rows older than the cutoff are deleted across tenants, newer ones stay, and the batch limit is honoured", async () => {
      const [a, b] = [randomUUID(), randomUUID()];
      await clientWithUsage(a, [0, 1, 10, 40, 200]);
      await clientWithUsage(b, [0, 100, 101, 102]);
      const cutoff = new Date(Date.now() - 30 * 86_400_000);
      expect(await fx.store.purgeClientUsage(cutoff, 2)).toBe(2);
      expect(await fx.store.purgeClientUsage(cutoff, 1000)).toBe(3); // 5 old rows in all
      expect(await fx.store.purgeClientUsage(cutoff, 1000)).toBe(0);
      expect(await remaining(a)).toBe(3);
      expect(await remaining(b)).toBe(1);
    });

    test("the purge function never deletes the last two days, whatever cutoff or batch the caller passes", async () => {
      const t = randomUUID();
      await clientWithUsage(t, [0, 1, 3]);
      expect(await fx.store.purgeClientUsage(new Date(Date.now() + 365 * 86_400_000), 1_000_000)).toBeGreaterThanOrEqual(1);
      expect(await remaining(t)).toBe(2);
    });

    test("Maintenance runs retention with the configured days and the re-seal pass together, and reports both", async () => {
      const t = randomUUID();
      await clientWithUsage(t, [0, 5, 20]);
      const sub = await new HubStore(fx.app, oldKeys).createSubscription(t, { name: "old", url: "https://hook.example/x", secret: "whsec_maintenance_secret", patterns: ["dev.passage.sales.**"], createdBy: null, idempotencyKey: null });
      const result = await new Maintenance({ store: fx.store, usageRetentionDays: 7 }).runOnce();
      expect(result.usagePurged).toBeGreaterThanOrEqual(1); // also the 10 day old rows other tests left
      expect(result.resealed).toBeGreaterThanOrEqual(1);
      expect(await remaining(t)).toBe(2);
      expect(envelopeKeyId((await fx.owner.query("select secret_sealed from hub_subscription where id = $1", [sub.subscription.id])).rows[0].secret_sealed)).toBe("k2025");
      // A hub without keys still purges usage.
      const keyless = await new Maintenance({ store: fx.keyless, usageRetentionDays: 2 }).runOnce();
      expect(keyless.resealed).toBe(0);
      expect(keyless.usagePurged).toBe(1); // the 5 day old row, now past a 2 day retention
      expect(await remaining(t)).toBe(1);
    });

    test("the application role cannot delete usage itself; only the purge function can", async () => {
      const t = randomUUID();
      await clientWithUsage(t, [0, 400]);
      if (process.env["PASSAGE_TEST_APP_DATABASE_URL"] !== undefined) {
        await expect(fx.store.withTenant(t, (c) => c.query("delete from hub_client_usage"))).rejects.toThrow(/permission denied/);
        expect(await remaining(t)).toBe(2);
      }
      expect(await fx.store.purgeClientUsage(new Date(Date.now() - 86_400_000 * 30), 1000)).toBeGreaterThanOrEqual(1);
      expect(await remaining(t)).toBe(1);
    });
  });
});
