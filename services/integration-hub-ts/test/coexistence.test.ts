import { randomUUID } from "node:crypto";
import { afterAll, beforeAll, describe, expect, test } from "vitest";

import {
  backoffSeconds,
  COEXISTENCE_CHANGED_TYPE,
  CODE_FAILED,
  CODE_NO_ADAPTER,
  CODE_REFUSED,
  CoexistenceRefusedError,
  CoexistenceStore,
  CoexistenceSweeper,
  coexistenceChanges,
  coexistenceRecorder,
  MAX_ATTEMPTS,
  ROLLED_BACK_TYPE,
  type CoexistenceAdapter,
  type SourceSystem,
} from "../src/coexistence.ts";
import { createMessageHandler, type KafkaMessageInput } from "../src/consumer.ts";
import type { HubEvent } from "../src/dispatcher.ts";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.ts";

const planId = "01a1c232-bdf8-7494-ba3b-d67787e3c644";

function hubEvent(type: string, data: unknown): HubEvent {
  return { eventId: randomUUID(), type, aggregateId: "x", sequence: 1, occurredAt: 0, data };
}

const flag = (entity: string, mode: string, version: number, company = "USMF", system = "AX2012") => ({
  source_system: system,
  source_company: company,
  entity,
  mode,
  version,
});

describe("coexistenceChanges", () => {
  test("a rollback lists the flags handed back to LEGACY, with its plan", () => {
    const { changes, invalid } = coexistenceChanges(
      hubEvent(ROLLED_BACK_TYPE, { plan_id: planId, coexistence_resumed: [flag("customer", "LEGACY", 2), flag("vendor", "LEGACY", 4)] }),
    );
    expect(invalid).toBe(0);
    expect(changes).toEqual([
      { sourceSystem: "AX2012", sourceCompany: "USMF", entity: "customer", mode: "LEGACY", version: 2, planId },
      { sourceSystem: "AX2012", sourceCompany: "USMF", entity: "vendor", mode: "LEGACY", version: 4, planId },
    ]);
  });

  test("a flag change is one change; entries that are not a flag are skipped and counted", () => {
    expect(coexistenceChanges(hubEvent(COEXISTENCE_CHANGED_TYPE, flag("customer", "PASSAGE", 1))).changes).toHaveLength(1);
    const bad = coexistenceChanges(
      hubEvent(ROLLED_BACK_TYPE, {
        plan_id: "not-a-uuid",
        coexistence_resumed: [flag("customer", "PASSAGE", 2), flag("vendor", "LEGACY", 0), flag("x", "LEGACY", 1, "", "AX2012"), { mode: "LEGACY" }, flag("y", "LEGACY", 1, "C", "NAV")],
      }),
    );
    expect(bad).toEqual({ changes: [], invalid: 5 });
    expect(coexistenceChanges(hubEvent(ROLLED_BACK_TYPE, { plan_id: planId })).changes).toEqual([]);
    expect(coexistenceChanges(hubEvent("dev.passage.cutover.step.completed.v1", flag("customer", "LEGACY", 1))).changes).toEqual([]);
  });

  test("backoff doubles from a minute and stops at an hour", () => {
    expect([1, 2, 3, 4, 5, 6, 7, 8].map(backoffSeconds)).toEqual([60, 120, 240, 480, 960, 1920, 3600, 3600]);
  });
});

/** An adapter that records its calls and fails as told. */
class FakeAdapter implements CoexistenceAdapter {
  readonly system: SourceSystem;
  calls: string[] = [];
  suspended = new Set<string>();
  fail: Error | null = null;
  constructor(system: SourceSystem) {
    this.system = system;
  }
  async suspend(_tenant: string, company: string, entity: string): Promise<void> {
    this.calls.push(`suspend ${company}/${entity}`);
    if (this.fail) throw this.fail;
    this.suspended.add(`${company}/${entity}`);
  }
  async resume(_tenant: string, company: string, entity: string): Promise<void> {
    this.calls.push(`resume ${company}/${entity}`);
    if (this.fail) throw this.fail;
    this.suspended.delete(`${company}/${entity}`);
  }
}

describe.skipIf(ownerUrl === undefined)("coexistence adapters on Postgres", () => {
  let f: Fixture;
  let cs: CoexistenceStore;
  const tenant = randomUUID();
  let now = new Date("2027-01-01T08:00:00Z");
  const clock = () => now;
  const advance = (seconds: number) => {
    now = new Date(now.getTime() + seconds * 1000);
  };

  beforeAll(async () => {
    f = await fixture();
    cs = new CoexistenceStore(f.app);
  });
  afterAll(async () => {
    await f?.close();
  });

  let offset = 0;
  function message(type: string, data: unknown, tenantId = tenant): KafkaMessageInput {
    offset += 1;
    const event = { specversion: "1.0", id: randomUUID(), source: "passage/migration-factory", type, subject: "s", time: now.toISOString(), tenantid: tenantId, data };
    return { topic: "passage.cutover.plan", partition: 0, offset: String(offset), key: null, value: Buffer.from(JSON.stringify(event)) };
  }

  test("AC-49.7: cutover suspends the adapters, the rollback event resumes them, and an older event changes nothing", async () => {
    const handle = createMessageHandler({
      store: f.store,
      dlqTopic: "dlq",
      deadLetter: async () => {
        throw new Error("no message may be dead-lettered");
      },
      clock: () => now.getTime() / 1000,
      coexistence: coexistenceRecorder(cs),
    });
    const ax = new FakeAdapter("AX2012");
    const sweep = new CoexistenceSweeper({ store: cs, adapters: [ax], clock });

    // Go-live: the factory cuts customers and vendors of USMF over.
    const cutCustomer = message(COEXISTENCE_CHANGED_TYPE, flag("customer", "PASSAGE", 1));
    await handle(cutCustomer);
    await handle(message(COEXISTENCE_CHANGED_TYPE, flag("vendor", "PASSAGE", 1)));
    expect(await sweep.runOnce()).toEqual({ applied: 2, retried: 0, parked: 0 });
    expect([...ax.suspended].sort()).toEqual(["USMF/customer", "USMF/vendor"]);

    // The rollback: the factory hands both back (version 2) and the hub resumes them.
    await handle(message(ROLLED_BACK_TYPE, { plan_id: planId, coexistence_resumed: [flag("customer", "LEGACY", 2), flag("vendor", "LEGACY", 2)] }));
    // The factory's per-flag coexistence.changed of the same flips arrives too: nothing new.
    await handle(message(COEXISTENCE_CHANGED_TYPE, { ...flag("customer", "LEGACY", 2), plan_id: planId }));
    expect(await sweep.runOnce()).toEqual({ applied: 2, retried: 0, parked: 0 });
    expect(ax.suspended.size).toBe(0);
    expect(ax.calls).toEqual(["suspend USMF/customer", "suspend USMF/vendor", "resume USMF/customer", "resume USMF/vendor"]);
    const state = await cs.list(tenant, "AX2012", "USMF");
    expect(state.map((s) => [s.entity, s.status, s.appliedMode, s.appliedVersion, s.planId])).toEqual([
      ["customer", "applied", "LEGACY", 2, planId],
      ["vendor", "applied", "LEGACY", 2, planId],
    ]);

    // A replay of the go-live message (Kafka at-least-once) is older than the rollback: ignored.
    await handle(cutCustomer);
    expect(await sweep.runOnce()).toEqual({ applied: 0, retried: 0, parked: 0 });
    expect(ax.suspended.size).toBe(0);
    // Another tenant sees none of it.
    expect(await cs.list(randomUUID(), "AX2012", "USMF")).toEqual([]);
  });

  test("a failing adapter backs off and is parked after five attempts; a retry of parked rows resumes it", async () => {
    const sap = new FakeAdapter("SAPECC");
    sap.fail = new Error("IDoc port unreachable");
    const sweep = new CoexistenceSweeper({ store: cs, adapters: [sap], clock });
    await cs.record(tenant, [{ sourceSystem: "SAPECC", sourceCompany: "1000", entity: "open_items", mode: "LEGACY", version: 3, planId }], "e1", now);
    expect(await sweep.runOnce()).toEqual({ applied: 0, retried: 1, parked: 0 });
    // Not due yet: the next sweep leaves it alone.
    expect(await sweep.runOnce()).toEqual({ applied: 0, retried: 0, parked: 0 });
    for (let attempt = 2; attempt <= MAX_ATTEMPTS; attempt += 1) {
      advance(backoffSeconds(attempt - 1));
      const r = await sweep.runOnce();
      expect(r).toEqual(attempt < MAX_ATTEMPTS ? { applied: 0, retried: 1, parked: 0 } : { applied: 0, retried: 0, parked: 1 });
    }
    let [row] = await cs.list(tenant, "SAPECC", "1000");
    expect([row?.status, row?.attempts, row?.lastCode]).toEqual(["parked", MAX_ATTEMPTS, CODE_FAILED]);
    advance(7200);
    expect(await sweep.runOnce()).toEqual({ applied: 0, retried: 0, parked: 0 });

    sap.fail = null;
    expect(await cs.retryParked(tenant, now)).toBe(1);
    expect(await sweep.runOnce()).toEqual({ applied: 1, retried: 0, parked: 0 });
    [row] = await cs.list(tenant, "SAPECC", "1000");
    expect([row?.status, row?.appliedMode, row?.attempts]).toEqual(["applied", "LEGACY", 0]);
  });

  test("hub_coexistence_due: definer rights, pinned search_path, keys only, executable by passage_app alone", async () => {
    const fn = await f.owner.query(
      `select p.prosecdef, p.proconfig, pg_get_function_result(p.oid) as result,
              array(select coalesce(r.rolname, 'PUBLIC') from aclexplode(p.proacl) a left join pg_roles r on r.oid = a.grantee
                     where a.privilege_type = 'EXECUTE' order by 1)::text[] as executors,
              pg_get_userbyid(p.proowner) as owner
         from pg_proc p join pg_namespace n on n.oid = p.pronamespace
        where p.proname = 'hub_coexistence_due' and n.nspname = $1`,
      [f.schema],
    );
    const row = fn.rows[0];
    expect(row.prosecdef).toBe(true);
    expect(row.proconfig).toEqual([`search_path=${f.schema}, pg_temp`]);
    expect(row.result).toBe("TABLE(tenant_id uuid, source_system text, source_company text, entity text)");
    expect(row.executors).not.toContain("PUBLIC");
    expect([...row.executors].sort()).toEqual([...new Set([row.owner, "passage_app"])].sort());
    // The application role reads another tenant's rows only through the function, which returns keys.
    const other = randomUUID();
    await cs.record(other, [{ sourceSystem: "AX2012", sourceCompany: "GBSI", entity: "product", mode: "LEGACY", version: 1, planId: null }], "e0", now);
    expect(await cs.list(tenant, "AX2012", "GBSI")).toEqual([]);
    expect((await cs.due(now, 10)).filter((k) => k.tenantId === other)).toEqual([{ tenantId: other, sourceSystem: "AX2012", sourceCompany: "GBSI", entity: "product" }]);
    expect(await new CoexistenceSweeper({ store: cs, adapters: [new FakeAdapter("AX2012")], clock }).runOnce()).toEqual({ applied: 1, retried: 0, parked: 0 });
  });

  test("a refusal, or no adapter for the system, parks the row at once; other rows go on", async () => {
    const sap = new FakeAdapter("SAPECC");
    sap.fail = new CoexistenceRefusedError("company 2000 is unknown to the IDoc partner profile");
    const ax = new FakeAdapter("AX2012");
    const sweep = new CoexistenceSweeper({ store: cs, adapters: [sap, ax], clock });
    await cs.record(
      tenant,
      [
        { sourceSystem: "SAPECC", sourceCompany: "2000", entity: "customer", mode: "LEGACY", version: 2, planId },
        { sourceSystem: "S4HANA", sourceCompany: "1710", entity: "customer", mode: "LEGACY", version: 2, planId },
        { sourceSystem: "AX2012", sourceCompany: "DEMF", entity: "worker", mode: "LEGACY", version: 2, planId },
      ],
      "e2",
      now,
    );
    expect(await sweep.runOnce()).toEqual({ applied: 1, retried: 0, parked: 2 });
    const [refused] = await cs.list(tenant, "SAPECC", "2000");
    const [missing] = await cs.list(tenant, "S4HANA", "1710");
    expect([refused?.status, refused?.lastCode, missing?.status, missing?.lastCode]).toEqual(["parked", CODE_REFUSED, "parked", CODE_NO_ADAPTER]);
    expect(ax.calls).toEqual(["resume DEMF/worker"]);
    // A newer change for a parked row queues it again.
    await cs.record(tenant, [{ sourceSystem: "SAPECC", sourceCompany: "2000", entity: "customer", mode: "PASSAGE", version: 3, planId: null }], "e3", now);
    sap.fail = null;
    expect(await sweep.runOnce()).toEqual({ applied: 1, retried: 0, parked: 0 });
    expect(sap.suspended.has("2000/customer")).toBe(true);
  });
});
