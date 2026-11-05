import { describe, expect, it } from "vitest";
import { type ApprovalRequest, type ApprovalStore, handleApprovalDecision, type RecordedDecision } from "../src/api/decision.js";
import { handleKillSwitchCommand, parseKillSwitchCommand, readKillSwitchStatus } from "../src/api/killswitch.js";
import { ApiError, type Principal, principalFromHeaders } from "../src/api/principal.js";
import type { AgentEvent } from "../src/domain/events.js";
import { KILL_PROPAGATION_MS, KillSwitchRegistry } from "../src/domain/killswitch.js";
import { KillSwitchFeed } from "../src/runtime/killswitch-feed.js";

const HOUR_MS = 3_600_000;
const T1 = "00000000-0000-7000-8000-000000000001";
const T2 = "00000000-0000-7000-8000-000000000002";
const ADMIN = "00000000-0000-7000-8000-0000000000a1";
const CLERK = "00000000-0000-7000-8000-0000000000c1";
const SUP = "00000000-0000-7000-8000-0000000000b1";
const SUP2 = "00000000-0000-7000-8000-0000000000b2";
const RUN = "00000000-0000-7000-8000-00000000f001";
const ACT = "00000000-0000-7000-8000-00000000f002";
const NOW = new Date("2026-10-22T10:00:00Z");

function principal(userId: string, tenantId: string, duties: string[]): Principal {
  return { userId, tenantId, duties: new Set(duties) };
}

function seq() {
  let v = 0;
  return { next: () => ++v };
}

/** Awaits fn and returns what it threw, so tests can assert status and code. */
async function thrown(fn: () => unknown): Promise<{ status?: number; code?: string; message?: string }> {
  try {
    await fn();
  } catch (e) {
    const err = e as { status?: number; code?: string; message?: string };
    return { status: err.status, code: err.code, message: err.message };
  }
  throw new Error("expected a throw");
}

function recorder() {
  const events: AgentEvent<unknown>[] = [];
  return { events, publish: async (e: AgentEvent<unknown>) => void events.push(e) };
}

describe("principalFromHeaders", () => {
  it("reads tenant, user and comma-separated duties, lowercasing the ids", () => {
    const p = principalFromHeaders({ "x-passage-tenant": T1.toUpperCase(), "x-passage-user": ADMIN, "x-passage-duties": "agents.admin, agents.view," });
    expect(p.tenantId).toBe(T1);
    expect(p.userId).toBe(ADMIN);
    expect([...p.duties].sort()).toEqual(["agents.admin", "agents.view"]);
  });

  it.each([
    ["no headers", {}],
    ["tenant not a uuid", { "x-passage-tenant": "t1", "x-passage-user": ADMIN }],
    ["the nil platform tenant", { "x-passage-tenant": "00000000-0000-0000-0000-000000000000", "x-passage-user": ADMIN }],
    ["no user", { "x-passage-tenant": T1 }],
  ])("refuses %s with 401 AGT-0401", async (_name, headers) => {
    expect(await thrown(() => principalFromHeaders(headers))).toMatchObject({ status: 401, code: "AGT-0401" });
  });
});

describe("parseKillSwitchCommand", () => {
  it.each([
    ["unknown scope", { scope: "LOCAL", active: true }],
    ["active not boolean", { scope: "GLOBAL", active: "yes" }],
    ["tenant scope without tenant", { scope: "TENANT", active: true }],
    ["tenant not a uuid", { scope: "TENANT", tenantId: "t1", active: true }],
    ["global with tenant", { scope: "GLOBAL", tenantId: T1, active: true }],
    ["an unknown field", { scope: "GLOBAL", active: true, reason: "x" }],
    ["array body", []],
  ])("rejects %s with 400 AGT-0400", async (_name, body) => {
    expect(await thrown(() => parseKillSwitchCommand(body))).toMatchObject({ status: 400, code: "AGT-0400" });
    expect(() => parseKillSwitchCommand(body)).toThrow(ApiError);
  });
});

describe("handleKillSwitchCommand (AC-47.6)", () => {
  it("a tenant admin halts their own tenant: the event is published, then the switch applies", async () => {
    const registry = new KillSwitchRegistry(() => 0);
    const out$ = recorder();
    const out = await handleKillSwitchCommand({
      principal: principal(ADMIN, T1, ["agents.admin"]),
      body: { scope: "TENANT", tenantId: T1, active: true },
      registry,
      versions: seq(),
      publish: out$.publish,
      now: () => NOW,
    });
    expect(registry.isHalted(T1)).toBe(true);
    expect(registry.isHalted(T2)).toBe(false);
    expect(out.body).toEqual({ scope: "TENANT", tenantId: T1, active: true, version: 1, changedAt: NOW.toISOString(), byUser: ADMIN });
    expect(out$.events).toEqual([
      {
        type: "dev.passage.agent.killswitch.changed.v1",
        tenantId: T1,
        data: { scope: "TENANT", tenant_id: T1, active: true, by_user: ADMIN, version: 1, changed_at: NOW.toISOString() },
      },
    ]);
  });

  it("is idempotent by scope: the same state again returns the current toggle and publishes nothing", async () => {
    const registry = new KillSwitchRegistry(() => 0);
    const out$ = recorder();
    const versions = seq();
    const call = (active: boolean) =>
      handleKillSwitchCommand({
        principal: principal(ADMIN, T1, ["agents.admin"]),
        body: { scope: "TENANT", tenantId: T1, active },
        registry,
        versions,
        publish: out$.publish,
        now: () => NOW,
      });
    const first = await call(true);
    const again = await call(true);
    expect(again.body).toEqual(first.body);
    expect(again.event).toBeUndefined();
    const resumed = await call(false);
    expect(resumed.body).toMatchObject({ active: false, version: 2 });
    expect(out$.events).toHaveLength(2);
    expect(registry.isHalted(T1)).toBe(false);
  });

  it("does not apply the switch locally when publishing fails", async () => {
    const registry = new KillSwitchRegistry(() => 0);
    const err = await thrown(() =>
      handleKillSwitchCommand({
        principal: principal(ADMIN, T1, ["agents.admin"]),
        body: { scope: "TENANT", tenantId: T1, active: true },
        registry,
        versions: seq(),
        publish: async () => {
          throw new Error("outbox unavailable");
        },
        now: () => NOW,
      }),
    );
    expect(err.message).toBe("outbox unavailable");
    expect(registry.isHalted(T1)).toBe(false);
    expect(registry.version).toBe(0);
  });

  it("a tenant admin cannot toggle another tenant (403 AGT-0403)", async () => {
    const registry = new KillSwitchRegistry(() => 0);
    const out$ = recorder();
    const err = await thrown(() =>
      handleKillSwitchCommand({
        principal: principal(ADMIN, T1, ["agents.admin"]),
        body: { scope: "TENANT", tenantId: T2, active: true },
        registry,
        versions: seq(),
        publish: out$.publish,
        now: () => NOW,
      }),
    );
    expect(err).toMatchObject({ status: 403, code: "AGT-0403" });
    expect(registry.isHalted(T2)).toBe(false);
    expect(out$.events).toEqual([]);
  });

  it("the global switch needs platform.global.admin, not agents.admin (403 AGT-0203)", async () => {
    const err = await thrown(() =>
      handleKillSwitchCommand({
        principal: principal(ADMIN, T1, ["agents.admin"]),
        body: { scope: "GLOBAL", active: true },
        registry: new KillSwitchRegistry(() => 0),
        versions: seq(),
        publish: recorder().publish,
        now: () => NOW,
      }),
    );
    expect(err).toMatchObject({ status: 403, code: "AGT-0203" });
  });

  it("a caller with no admin duty is refused before a version is taken", async () => {
    const registry = new KillSwitchRegistry(() => 0);
    const versions = seq();
    const err = await thrown(() =>
      handleKillSwitchCommand({
        principal: principal(CLERK, T1, []),
        body: { scope: "TENANT", tenantId: T1, active: true },
        registry,
        versions,
        publish: recorder().publish,
        now: () => NOW,
      }),
    );
    expect(err).toMatchObject({ status: 403, code: "AGT-0203" });
    expect(registry.version).toBe(0);
    expect(versions.next()).toBe(1);
  });

  it("a global kill reaches a second worker through the feed within the propagation window", async () => {
    let t = 0;
    const operator = new KillSwitchRegistry(() => t);
    const worker = new KillSwitchRegistry(() => t);
    const out$ = recorder();
    await handleKillSwitchCommand({
      principal: principal(ADMIN, T1, ["platform.global.admin"]),
      body: { scope: "GLOBAL", active: true },
      registry: operator,
      versions: seq(),
      publish: out$.publish,
      now: () => NOW,
    });
    const feed = new KillSwitchFeed(worker);
    t += 4_000;
    const event = out$.events[0]!;
    expect(feed.handle({ type: event.type, tenantid: event.tenantId, data: event.data })).toBe("applied");
    t += KILL_PROPAGATION_MS - 1;
    expect(worker.isHalted(T2)).toBe(true);
  });
});

describe("readKillSwitchStatus", () => {
  it("shows the caller's tenant switch with its author and the global switch without one", async () => {
    const registry = new KillSwitchRegistry(() => 0);
    await handleKillSwitchCommand({
      principal: principal(ADMIN, T1, ["agents.admin"]),
      body: { scope: "TENANT", tenantId: T1, active: true },
      registry,
      versions: seq(),
      publish: recorder().publish,
      now: () => NOW,
    });
    expect(readKillSwitchStatus(principal(ADMIN, T1, ["agents.admin"]), registry)).toEqual({
      tenantId: T1,
      halted: true,
      stale: false,
      tenant: { active: true, version: 1, changedAt: NOW.toISOString(), byUser: ADMIN },
      global: { active: false, version: null, changedAt: null },
    });
    expect(readKillSwitchStatus(principal(ADMIN, T2, ["platform.global.admin"]), registry)).toMatchObject({ tenantId: T2, halted: false, tenant: { active: false, byUser: null } });
  });

  it("reports a worker that lost the stream as halted and stale (fail-closed)", () => {
    let t = 0;
    const registry = new KillSwitchRegistry(() => t);
    t += KILL_PROPAGATION_MS + 1;
    expect(readKillSwitchStatus(principal(ADMIN, T1, ["agents.admin"]), registry)).toMatchObject({ halted: true, stale: true });
  });

  it("needs agents.admin or platform.global.admin", async () => {
    expect(await thrown(() => readKillSwitchStatus(principal(CLERK, T1, ["agents.view"]), new KillSwitchRegistry(() => 0)))).toMatchObject({
      status: 403,
      code: "AGT-0203",
    });
  });
});

/** An in-memory approval store with the contract's first-writer-wins `record`. */
function memoryStore(rows: Record<string, ApprovalRequest>): ApprovalStore & { rows: Record<string, ApprovalRequest> } {
  const key = (tenantId: string, runId: string, actionId: string) => `${tenantId}/${runId}/${actionId}`;
  return {
    rows,
    async find(tenantId, runId, actionId) {
      return rows[key(tenantId, runId, actionId)];
    },
    async record(tenantId, runId, actionId, decision: RecordedDecision) {
      const row = rows[key(tenantId, runId, actionId)]!;
      row.decided ??= decision;
      return row.decided;
    },
  };
}

describe("handleApprovalDecision (FR-47.8, AGT-0209)", () => {
  const request = (over: Partial<ApprovalRequest> = {}): ApprovalRequest => ({
    invokerUserId: CLERK,
    requestedAtMs: NOW.getTime(),
    approvalDuty: "APInvoicePost",
    amount: { amount: "1200.00", currency: "USD" },
    selfApprovalLimit: { amount: "5000.00", currency: "USD" },
    ...over,
  });
  const store = (over: Partial<ApprovalRequest> = {}) => memoryStore({ [`${T1}/${RUN}/${ACT}`]: request(over) });
  const decide = (s: ApprovalStore, who: Principal, decision: unknown, at: Date = NOW) =>
    handleApprovalDecision({ principal: who, runId: RUN, actionId: ACT, body: { decision }, store: s, now: () => at });
  const sup = principal(SUP, T1, ["APInvoicePost"]);

  it("an approver with the duty approves; the decision is recorded with approver and time", async () => {
    const s = store();
    const out = await decide(s, sup, "APPROVED");
    expect(out).toEqual({ runId: RUN, actionId: ACT, decision: "APPROVED", approverUserId: SUP, decidedAt: NOW.toISOString() });
    expect(s.rows[`${T1}/${RUN}/${ACT}`]?.decided).toEqual({ decision: "APPROVED", approverUserId: SUP, decidedAt: NOW.toISOString() });
  });

  it("is idempotent by action id: the same decision again returns the recorded one", async () => {
    const s = store();
    const first = await decide(s, sup, "APPROVED");
    const again = await decide(s, principal(SUP2, T1, ["APInvoicePost"]), "APPROVED", new Date(NOW.getTime() + 60_000));
    expect(again).toEqual(first);
  });

  it("refuses a different decision on a decided action (409 AGT-0409)", async () => {
    const s = store();
    await decide(s, sup, "REJECTED");
    expect(await thrown(() => decide(s, principal(SUP2, T1, ["APInvoicePost"]), "APPROVED"))).toMatchObject({ status: 409, code: "AGT-0409" });
    expect(s.rows[`${T1}/${RUN}/${ACT}`]?.decided?.decision).toBe("REJECTED");
  });

  it("keeps the first decision when another approver recorded first", async () => {
    const s = store();
    const racing: ApprovalStore = {
      find: s.find,
      async record(tenantId, runId, actionId) {
        return s.record(tenantId, runId, actionId, { decision: "REJECTED", approverUserId: SUP2, decidedAt: NOW.toISOString() });
      },
    };
    expect(await thrown(() => decide(racing, sup, "APPROVED"))).toMatchObject({ status: 409, code: "AGT-0409" });
  });

  it("an expired request is recorded and answered as REJECTED with reason EXPIRED (BR-47.3)", async () => {
    const s = store();
    const late = new Date(NOW.getTime() + 72 * HOUR_MS);
    const out = await decide(s, sup, "APPROVED", late);
    expect(out).toMatchObject({ decision: "REJECTED", reason: "EXPIRED", code: "AGT-0208" });
    expect(s.rows[`${T1}/${RUN}/${ACT}`]?.decided).toMatchObject({ decision: "REJECTED", reason: "EXPIRED" });
    expect(await decide(s, sup, "APPROVED", late)).toEqual(out);
  });

  it("an approver without the duty gets 403 AGT-0203, even for a decided action", async () => {
    const s = store();
    expect(await thrown(() => decide(s, principal(SUP, T1, []), "APPROVED"))).toMatchObject({ status: 403, code: "AGT-0203" });
    await decide(s, sup, "APPROVED");
    expect(await thrown(() => decide(s, principal(SUP, T1, []), "APPROVED"))).toMatchObject({ status: 403, code: "AGT-0203" });
  });

  it("the invoker cannot approve their own run above the self-approval limit (AGT-0209)", async () => {
    const s = store({ amount: { amount: "9000.00", currency: "USD" } });
    const clerk = principal(CLERK, T1, ["APInvoicePost"]);
    expect(await thrown(() => decide(s, clerk, "APPROVED"))).toMatchObject({ status: 403, code: "AGT-0209" });
    expect(s.rows[`${T1}/${RUN}/${ACT}`]?.decided).toBeUndefined();
    expect((await decide(s, clerk, "REJECTED")).decision).toBe("REJECTED");
  });

  it("never finds another tenant's action, whatever ids are sent (404 AGT-0404)", async () => {
    const s = store();
    expect(await thrown(() => decide(s, principal(SUP, T2, ["APInvoicePost"]), "APPROVED"))).toMatchObject({ status: 404, code: "AGT-0404" });
    expect(s.rows[`${T1}/${RUN}/${ACT}`]?.decided).toBeUndefined();
  });

  it("a bad decision value is 400 AGT-0400", async () => {
    expect(await thrown(() => decide(store(), sup, "MAYBE"))).toMatchObject({ status: 400, code: "AGT-0400" });
  });
});

describe("KillSwitchFeed (FR-47.12)", () => {
  const data = (over: Record<string, unknown> = {}) => ({
    scope: "TENANT",
    tenant_id: T1,
    active: true,
    by_user: ADMIN,
    version: 1,
    changed_at: NOW.toISOString(),
    ...over,
  });
  const TYPE = "dev.passage.agent.killswitch.changed.v1";

  it("applies, reports duplicates and ignores other event types", () => {
    const reg = new KillSwitchRegistry(() => 0);
    const feed = new KillSwitchFeed(reg);
    expect(feed.handle({ type: TYPE, tenantid: T1, data: data() })).toBe("applied");
    expect(feed.handle({ type: TYPE, tenantid: T1, data: data() })).toBe("duplicate");
    expect(feed.handle({ type: "dev.passage.agent.run.completed.v1", tenantid: T1, data: {} })).toBe("ignored");
    expect(reg.isHalted(T1)).toBe(true);
    expect(reg.isHalted(T2)).toBe(false);
  });

  it("keeps the window open on heartbeats while no toggle arrives", () => {
    let t = 0;
    const reg = new KillSwitchRegistry(() => t);
    const feed = new KillSwitchFeed(reg);
    t += KILL_PROPAGATION_MS - 1000;
    feed.heartbeat();
    t += KILL_PROPAGATION_MS - 1000;
    expect(reg.isStale()).toBe(false);
  });

  it.each([
    ["no envelope tenant", { type: TYPE, data: data() }],
    ["scope not an enum", { type: TYPE, tenantid: T1, data: data({ scope: "LOCAL" }) }],
    ["tenant scope with null tenant", { type: TYPE, tenantid: T1, data: data({ tenant_id: null }) }],
    ["global scope with a tenant", { type: TYPE, tenantid: T1, data: data({ scope: "GLOBAL" }) }],
    ["a tenant toggle addressed to another tenant", { type: TYPE, tenantid: T2, data: data() }],
    ["camelCase fields instead of the event's snake_case", { type: TYPE, tenantid: T1, data: { scope: "TENANT", tenantId: T1, active: true, byUser: ADMIN, version: 1, changedAt: NOW.toISOString() } }],
    ["version not a positive integer", { type: TYPE, tenantid: T1, data: data({ version: 0 }) }],
    ["data not an object", { type: TYPE, tenantid: T1, data: "x" }],
  ])("throws for %s so the consumer sends it to the DLQ", (_name, message) => {
    const reg = new KillSwitchRegistry(() => 0);
    expect(() => new KillSwitchFeed(reg).handle(message)).toThrow();
    expect(reg.version).toBe(0);
  });
});
