import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { handleKillSwitchCommand, readKillSwitchStatus } from "../src/api/killswitch.js";
import type { Principal } from "../src/api/principal.js";
import { KILL_PROPAGATION_MS, KillSwitchRegistry } from "../src/domain/killswitch.js";
import { KillSwitchPoller } from "../src/runtime/killswitch-poller.js";
import { PgKillSwitchLog } from "../src/store/killswitch.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";

const OPERATOR = "00000000-0000-7000-8000-0000000000ff";
const PLATFORM = "00000000-0000-7000-8000-0000000000aa";
const NOW = () => new Date("2026-11-13T10:00:00.000Z");

let tenantSeq = 0;
const nextTenant = (): string => `00000000-0000-7000-8000-${(0x500 + ++tenantSeq).toString(16).padStart(12, "0")}`;
const admin = (tenantId: string): Principal => ({ userId: OPERATOR, tenantId, duties: new Set(["agents.admin"]) });
const platformAdmin: Principal = { userId: OPERATOR, tenantId: PLATFORM, duties: new Set(["platform.global.admin"]) };

function clock() {
  let t = 0;
  return { now: () => t, advance: (ms: number) => void (t += ms) };
}

/** One pod: its own registry and poller over the shared table. */
function pod(log: PgKillSwitchLog, nowMs: () => number = () => Date.now()) {
  const registry = new KillSwitchRegistry(nowMs);
  return { registry, poller: new KillSwitchPoller(log, registry) };
}

describe.runIf(ownerUrl)("kill switch shared through Postgres (AC-47.6)", () => {
  let fx: Fixture;
  let log: PgKillSwitchLog;

  beforeAll(async () => {
    fx = await fixture();
    log = new PgKillSwitchLog(fx.store, fx.app);
  });
  afterAll(async () => {
    await fx.close();
  });

  const toggle = (principal: Principal, body: unknown, registry: KillSwitchRegistry) =>
    handleKillSwitchCommand({ principal, body, registry, now: NOW, log });

  it("a toggle taken on one pod halts runs on another pod at its next poll, and releasing it resumes them", async () => {
    const t = nextTenant();
    const a = pod(log);
    const b = pod(log);
    await a.poller.poll();
    await b.poller.poll();
    expect(b.registry.isHalted(t)).toBe(false);

    const out = await toggle(admin(t), { scope: "TENANT", tenantId: t, active: true }, a.registry);
    expect(out.event?.type).toBe("dev.passage.agent.killswitch.changed.v1");
    expect(a.registry.isHalted(t)).toBe(true);
    expect(b.registry.isHalted(t)).toBe(false);

    expect(await b.poller.poll()).toBe(1);
    expect(b.registry.isHalted(t)).toBe(true);
    expect(b.registry.isHalted(nextTenant())).toBe(false);
    expect(readKillSwitchStatus(admin(t), b.registry)).toMatchObject({ halted: true, tenant: { active: true, byUser: OPERATOR } });

    await toggle(admin(t), { scope: "TENANT", tenantId: t, active: false }, b.registry);
    await a.poller.poll();
    expect(a.registry.isHalted(t)).toBe(false);
  });

  it("a pod that takes a toggle also applies the earlier toggles it had not polled yet, so its next poll skips none", async () => {
    const t1 = nextTenant();
    const t2 = nextTenant();
    const a = pod(log);
    const b = pod(log);
    await a.poller.poll();
    await b.poller.poll();
    // Pod a halts t1; before pod b polls, pod b takes a later toggle for t2.
    await toggle(admin(t1), { scope: "TENANT", tenantId: t1, active: true }, a.registry);
    await toggle(admin(t2), { scope: "TENANT", tenantId: t2, active: true }, b.registry);
    expect(b.registry.isHalted(t2)).toBe(true);
    expect(b.registry.isHalted(t1)).toBe(true);
    await b.poller.poll();
    expect(b.registry.isHalted(t1)).toBe(true);
    // The same holds when the later toggle is a no-op on the stored state.
    const t3 = nextTenant();
    const c = pod(log);
    await c.poller.poll();
    await toggle(admin(t3), { scope: "TENANT", tenantId: t3, active: true }, a.registry);
    await toggle(admin(t2), { scope: "TENANT", tenantId: t2, active: true }, c.registry);
    expect(c.registry.isHalted(t3)).toBe(true);
  });

  it("the global switch reaches every pod and halts every tenant", async () => {
    const a = pod(log);
    const b = pod(log);
    await a.poller.poll();
    await b.poller.poll();
    await toggle(platformAdmin, { scope: "GLOBAL", active: true }, a.registry);
    await b.poller.poll();
    expect(b.registry.isHalted(nextTenant())).toBe(true);
    await toggle(platformAdmin, { scope: "GLOBAL", active: false }, b.registry);
    await a.poller.poll();
    expect(a.registry.isHalted(nextTenant())).toBe(false);
  });

  it("is idempotent against the stored state: the same toggle from two pods stores one row and one outbox event", async () => {
    const t = nextTenant();
    const a = pod(log);
    const b = pod(log);
    const first = await toggle(admin(t), { scope: "TENANT", tenantId: t, active: true }, a.registry);
    // Pod b has not polled yet, so only the stored state can tell it the switch is already on.
    const second = await toggle(admin(t), { scope: "TENANT", tenantId: t, active: true }, b.registry);
    expect(second.event).toBeUndefined();
    expect(second.body.version).toBe(first.body.version);
    const rows = await fx.owner.query("select count(*)::int as n from kill_switch_change where target_tenant_id = $1", [t]);
    expect(rows.rows[0].n).toBe(1);
    const events = await fx.owner.query("select type, payload from outbox where tenant_id = $1 and aggregate_type = 'kill_switch'", [t]);
    expect(events.rows).toHaveLength(1);
    expect(events.rows[0].type).toBe("dev.passage.agent.killswitch.changed.v1");
    expect(events.rows[0].payload).toMatchObject({ scope: "TENANT", tenant_id: t, active: true, version: first.body.version });
  });

  it("concurrent toggles from many pods commit in version order, so a poll never skips one", async () => {
    const tenants = Array.from({ length: 12 }, () => nextTenant());
    const watcher = pod(log);
    await watcher.poller.poll();
    const start = watcher.registry.version;
    const seen: number[] = [];
    const polling = (async () => {
      for (let i = 0; i < 40; i++) {
        const before = watcher.registry.version;
        const changes = await log.changesSince(before);
        for (const c of changes) {
          seen.push(c.version);
          watcher.registry.apply(c);
        }
      }
    })();
    await Promise.all(tenants.map((t) => toggle(admin(t), { scope: "TENANT", tenantId: t, active: true }, new KillSwitchRegistry(() => Date.now()))));
    await polling;
    await watcher.poller.poll();
    for (const t of tenants) expect(watcher.registry.isHalted(t)).toBe(true);
    // Versions arrive strictly increasing and every toggle is seen exactly once.
    for (let i = 1; i < seen.length; i++) expect(seen[i]!).toBeGreaterThan(seen[i - 1]!);
    const stored = await fx.owner.query<{ version: string }>("select version from kill_switch_change where version > $1 order by version", [start]);
    expect(stored.rows).toHaveLength(tenants.length);
  });

  it("a starting pod reads only the latest toggle of each switch, not the whole history", async () => {
    const t = nextTenant();
    const a = pod(log);
    for (const active of [true, false, true]) await toggle(admin(t), { scope: "TENANT", tenantId: t, active }, a.registry);
    const fresh = await log.changesSince(0);
    expect(fresh.filter((c) => c.tenantId === t)).toHaveLength(1);
    const versions = fresh.map((c) => c.version);
    expect([...versions].sort((x, y) => x - y)).toEqual(versions);
    const b = pod(log);
    await b.poller.poll();
    expect(b.registry.isHalted(t)).toBe(true);
    expect(b.registry.version).toBe(Math.max(...versions));
  });

  it("a pod that cannot read the table fails closed after the propagation window", async () => {
    const c = clock();
    const registry = new KillSwitchRegistry(c.now);
    let broken = false;
    const errors: unknown[] = [];
    const poller = new KillSwitchPoller(
      { changesSince: (v) => (broken ? Promise.reject(new Error("connection refused")) : log.changesSince(v)) },
      registry,
      (err) => errors.push(err),
    );
    const t = nextTenant();
    await poller.poll();
    c.advance(KILL_PROPAGATION_MS);
    await poller.poll();
    c.advance(KILL_PROPAGATION_MS);
    expect(registry.isHalted(t)).toBe(false);
    broken = true;
    await expect(poller.poll()).rejects.toThrow("connection refused");
    c.advance(1);
    expect(registry.isHalted(t)).toBe(true);
    broken = false;
    await poller.poll();
    expect(registry.isHalted(t)).toBe(false);
  });

  it("start polls at once and keeps polling until stopped", async () => {
    const t = nextTenant();
    const registry = new KillSwitchRegistry(() => Date.now());
    const poller = new KillSwitchPoller(log, registry);
    await poller.start(100);
    try {
      await toggle(admin(t), { scope: "TENANT", tenantId: t, active: true }, new KillSwitchRegistry(() => Date.now()));
      const deadline = Date.now() + 5_000;
      while (!registry.isHalted(t) && Date.now() < deadline) await new Promise((r) => setTimeout(r, 50));
      expect(registry.isHalted(t)).toBe(true);
    } finally {
      poller.stop();
    }
  });

  it("polls work on pooled connections an earlier tenant transaction left with an empty tenant setting", async () => {
    const t = nextTenant();
    await toggle(admin(t), { scope: "TENANT", tenantId: t, active: true }, new KillSwitchRegistry(() => Date.now()));
    // Dirty every connection of the pool (max 16) with a transaction-scoped tenant setting.
    await Promise.all(Array.from({ length: 32 }, () => fx.store.withTenant(nextTenant(), (c) => c.query("select pg_sleep(0.01)"))));
    for (let i = 0; i < 32; i++) expect((await log.changesSince(0)).some((c) => c.tenantId === t)).toBe(true);
  });

  it("toggles are append-only for the application role, and a row can only switch the writer's own tenant", async () => {
    const t = nextTenant();
    await toggle(admin(t), { scope: "TENANT", tenantId: t, active: true }, new KillSwitchRegistry(() => Date.now()));
    await expect(fx.app.query("update kill_switch_change set active = false where target_tenant_id = $1", [t])).rejects.toThrow(/permission denied/);
    await expect(fx.app.query("delete from kill_switch_change where target_tenant_id = $1", [t])).rejects.toThrow(/permission denied/);
    const other = nextTenant();
    await expect(
      fx.store.withTenant(t, (c) =>
        c.query("insert into kill_switch_change (tenant_id, scope, target_tenant_id, active, by_user) values ($1, 'TENANT', $2, true, $3)", [t, other, OPERATOR]),
      ),
    ).rejects.toThrow(/check constraint/);
    await expect(
      fx.store.withTenant(t, (c) =>
        c.query("insert into kill_switch_change (tenant_id, scope, target_tenant_id, active, by_user) values ($1, 'TENANT', $1, true, $2)", [other, OPERATOR]),
      ),
    ).rejects.toThrow(/row-level security/);
  });
});
