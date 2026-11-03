import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { AgentError } from "../src/errors.js";
import { runAgent, RUN_TIMEOUT_MS, type AuditPort, type BudgetPort, type BudgetRequest, type RunDeps } from "../src/runtime/engine.js";
import { openAgentStore, PgAgentStore } from "../src/store/pg.js";
import { APPROVER, FakeApprovals, FakeTools, makeDeps, makeRequest, postDescriptor, proposal, ScriptedProvider, testAudit, testBudgets } from "./fakes.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";

const tenant = (n: number): string => `00000000-0000-7000-8000-${String(n).padStart(12, "0")}`;
const NOW = Date.UTC(2026, 9, 21);
const ask = (tenantId: string, tokens: number, over: Partial<BudgetRequest> = {}): BudgetRequest => ({
  tenantId,
  agentId: "ap_coding",
  runId: "run-x",
  month: "2026-10",
  tokensIn: tokens,
  tokensOut: tokens,
  nowMs: NOW,
  ttlMs: RUN_TIMEOUT_MS,
  ...over,
});

const committingRun = (overrides: Partial<RunDeps>) => {
  const tools = new FakeTools({ "passage.ap.invoice.post": postDescriptor });
  const provider = new ScriptedProvider([proposal(0.8)]);
  const deps = {
    ...makeDeps({ provider, tools, approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }),
    ...overrides,
  };
  return { deps, tools, provider };
};

describe("run start validation and post-commit failures (no database)", () => {
  it.each([0, -5, Number.NaN, 1.5, Number.POSITIVE_INFINITY])("a runTokenCap of %s is refused before any hold or provider call", async (cap) => {
    const reserved: BudgetRequest[] = [];
    const { deps, provider } = committingRun({ budgets: { ...testBudgets, reserve: async (r) => (reserved.push(r), testBudgets.reserve(r)) } });
    await expect(runAgent(deps, makeRequest({ runTokenCap: cap }))).rejects.toThrow(/runTokenCap must be a positive safe integer/);
    expect(reserved).toHaveLength(0);
    expect(provider.seen).toHaveLength(0);
  });

  it("a budget that cannot cover the run stops it before the audit row opens and any provider call", async () => {
    const opened: string[] = [];
    const { deps, provider } = committingRun({
      budgets: { ...testBudgets, reserve: async () => Promise.reject(new AgentError("AGT-0202", "exhausted")) },
      audit: { ...testAudit, startRun: async (r) => void opened.push(r.runId) },
    });
    await expect(runAgent(deps, makeRequest())).rejects.toMatchObject({ code: "AGT-0202" });
    expect(opened).toHaveLength(0);
    expect(provider.seen).toHaveLength(0);
  });

  it("an audit row that cannot be opened (reused run id) stops the run before any tool call and releases the hold", async () => {
    const settled: Array<[number, number]> = [];
    const { deps, tools, provider } = committingRun({
      audit: { ...testAudit, startRun: async () => Promise.reject(new Error("duplicate key agent_run_pkey")) },
      budgets: { ...testBudgets, settle: async (_h, u) => void settled.push([u.tokensIn, u.tokensOut]) },
    });
    await expect(runAgent(deps, makeRequest())).rejects.toThrow("agent_run_pkey");
    expect(tools.calls).toHaveLength(0);
    expect(provider.seen).toHaveLength(0);
    expect(settled).toEqual([[0, 0]]);
  });

  it("a settle that fails after the commit keeps the COMMITTED result and audits it FAILED_AFTER_COMMIT", async () => {
    const finished: Array<string | undefined> = [];
    const { deps, tools } = committingRun({
      budgets: { ...testBudgets, settle: async () => Promise.reject(new Error("budget db down")) },
      audit: { ...testAudit, finishRun: async (_r, _res, after) => void finished.push(after) },
    });
    const result = await runAgent(deps, makeRequest());
    expect(tools.calls).toHaveLength(1);
    expect(result).toMatchObject({ status: "COMMITTED", postCommitError: "budget db down" });
    expect(result.actions).toHaveLength(1);
    expect(finished).toEqual(["budget db down"]);
  });

  it("an audit write that fails after the commit keeps the COMMITTED result and reports the failure", async () => {
    const { deps, tools } = committingRun({ audit: { ...testAudit, finishRun: async () => Promise.reject(new Error("audit db down")) } });
    const result = await runAgent(deps, makeRequest());
    expect(tools.calls).toHaveLength(1);
    expect(result).toMatchObject({ status: "COMMITTED", postCommitError: "audit db down" });
  });

  it("an audit write that fails on a run that committed nothing is thrown, not swallowed", async () => {
    const { deps } = committingRun({
      approvals: new FakeApprovals({ decision: "REJECTED", approverUserId: APPROVER, reason: "no" }),
      audit: { ...testAudit, finishRun: async () => Promise.reject(new Error("audit db down")) },
    });
    await expect(runAgent(deps, makeRequest())).rejects.toThrow("audit db down");
  });

  it("the store cannot be opened without DATABASE_URL", async () => {
    await expect(openAgentStore({})).rejects.toThrow("DATABASE_URL is required");
  });
});

describe.runIf(ownerUrl)("persisted audit and token budget in Postgres", () => {
  let db: Fixture;
  let store: PgAgentStore;
  beforeAll(async () => {
    db = await fixture();
    store = db.store;
  });
  afterAll(() => db.close());

  const budgetRow = async (t: string) =>
    (await db.owner.query("select used_in::int, used_out::int, limit_in::int, limit_out::int, alert_state from token_budget where tenant_id = $1 and month = '2026-10'", [t])).rows[0];
  const holds = async (t: string) => (await db.owner.query("select count(*)::int as n from budget_hold where tenant_id = $1", [t])).rows[0].n as number;
  const events = async (t: string) => (await db.owner.query("select type, payload from outbox where tenant_id = $1 order by created_at, id", [t])).rows;
  const runRow = async (t: string, runId: string) => (await db.owner.query("select status, post_commit_error from agent_run where tenant_id = $1 and run_id = $2", [t, runId])).rows[0];

  it.each([0, -1, Number.NaN, 1.5])("reserve refuses a hold of %s and leaves other runs' holds alone", async (tokens) => {
    const t = tenant(100 + [0, -1, Number.NaN, 1.5].findIndex((n) => Object.is(n, tokens)));
    await store.setBudget(t, "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    await store.reserve(ask(t, 100));
    await expect(store.reserve(ask(t, tokens))).rejects.toThrow(/safe integer/);
    expect(await holds(t)).toBe(1);
  });

  it("an exhausted budget refuses a new hold with AGT-0202, even a hold of 1", async () => {
    const t = tenant(11);
    await store.setBudget(t, "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    const hold = await store.reserve(ask(t, 1_000));
    await store.settle(hold, { tokensIn: 1_000, tokensOut: 1_000 });
    await expect(store.reserve(ask(t, 1))).rejects.toMatchObject({ code: "AGT-0202" });
    const { deps, provider } = committingRun({ budgets: store, audit: store });
    await expect(runAgent(deps, makeRequest({ tenantId: t, runTokenCap: 1 }))).rejects.toMatchObject({ code: "AGT-0202" });
    expect(provider.seen).toHaveLength(0);
    expect(await budgetRow(t)).toMatchObject({ used_in: 1_000, used_out: 1_000 });
  });

  it("40 parallel runs against a 1,000 token budget: exactly 10 hold 100, and settled usage never exceeds the limit", async () => {
    const t = tenant(12);
    await store.setBudget(t, "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    const outcomes = await Promise.allSettled(
      Array.from({ length: 40 }, async (_, i) => {
        const hold = await store.reserve(ask(t, 100, { runId: `r${i}` }));
        await store.settle(hold, { tokensIn: 100, tokensOut: 100 });
      }),
    );
    const rejected = outcomes.filter((o): o is PromiseRejectedResult => o.status === "rejected");
    expect(outcomes.filter((o) => o.status === "fulfilled")).toHaveLength(10);
    for (const o of rejected) expect(o.reason).toMatchObject({ code: "AGT-0202" });
    expect(await budgetRow(t)).toMatchObject({ used_in: 1_000, used_out: 1_000 });
    expect(await holds(t)).toBe(0);
  });

  it("live holds count against the limit, and a settle by hold id frees only its own hold", async () => {
    const t = tenant(13);
    await store.setBudget(t, "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    const held = await Promise.all(Array.from({ length: 10 }, (_, i) => store.reserve(ask(t, 100, { runId: `r${i}` }))));
    await expect(store.reserve(ask(t, 1))).rejects.toMatchObject({ code: "AGT-0202" });
    await store.settle(held[0]!, { tokensIn: 0, tokensOut: 0 });
    expect(await holds(t)).toBe(9);
    await store.reserve(ask(t, 100));
    await expect(store.reserve(ask(t, 1))).rejects.toMatchObject({ code: "AGT-0202" });
  });

  it("a hold left by a crashed worker is swept once its run timeout has passed", async () => {
    const t = tenant(14);
    await store.setBudget(t, "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    await store.reserve(ask(t, 1_000, { runId: "crashed" }));
    await expect(store.reserve(ask(t, 1, { nowMs: NOW + RUN_TIMEOUT_MS - 1 }))).rejects.toMatchObject({ code: "AGT-0202" });
    await store.reserve(ask(t, 1_000, { runId: "after", nowMs: NOW + RUN_TIMEOUT_MS + 1 }));
    expect(await holds(t)).toBe(1);
    expect((await db.owner.query("select run_id from budget_hold where tenant_id = $1", [t])).rows).toEqual([{ run_id: "after" }]);
  });

  it("the budget is per tenant per month: two agents share it, and the month's row is created from the 50M/10M default", async () => {
    const t = tenant(15);
    await store.reserve(ask(t, 1, { agentId: "ap_coding" }));
    await store.reserve(ask(t, 1, { agentId: "cash_application" }));
    expect(await budgetRow(t)).toMatchObject({ limit_in: 50_000_000, limit_out: 10_000_000 });
    expect((await db.owner.query("select count(*)::int as n from token_budget where tenant_id = $1", [t])).rows[0].n).toBe(1);
    const snapshot = await store.budgetSnapshot(t, "2026-11");
    expect(snapshot).toMatchObject({ limitIn: 50_000_000, limitOut: 10_000_000, usedIn: 0, softPct: 80 });
    await expect(store.reserve(ask(t, 10_000_001))).rejects.toMatchObject({ code: "AGT-0202" });
  });

  it("with defaults disabled a month with no row is not configured, a different failure from exhausted", async () => {
    const strict = new PgAgentStore(db.app, null);
    const failure = await strict.reserve(ask(tenant(16), 1)).catch((e: unknown) => e);
    expect(failure).toBeInstanceOf(Error);
    expect(failure).not.toBeInstanceOf(AgentError);
    expect((failure as Error).message).toMatch(/not configured|no token budget is configured/);
  });

  it("one tenant's budget and holds are invisible to another under row-level security", async () => {
    const t = tenant(17);
    await store.setBudget(t, "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    await store.reserve(ask(t, 100));
    for (const table of ["token_budget", "budget_hold", "agent_run", "agent_action", "outbox"]) {
      const seen = await store.withTenant(tenant(18), (c) => c.query(`select 1 from ${table} where tenant_id = $1`, [t]));
      expect(seen.rowCount, table).toBe(0);
    }
  });

  it("writes budget.threshold to the outbox once per crossing, even when settles race", async () => {
    const t = tenant(19);
    await store.setBudget(t, "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    const held = await Promise.all(Array.from({ length: 9 }, (_, i) => store.reserve(ask(t, 100, { runId: `r${i}` }))));
    await Promise.all(held.map((h) => store.settle(h, { tokensIn: 100, tokensOut: 0 })));
    expect((await events(t)).map((e) => e.payload)).toEqual([{ tenant_id: t, month: "2026-10", pct: 80, state: "SOFT" }]);
    await store.settle(await store.reserve(ask(t, 100)), { tokensIn: 100, tokensOut: 0 });
    const all = await events(t);
    expect(all.map((e) => e.type)).toEqual(["dev.passage.agent.budget.threshold.v1", "dev.passage.agent.budget.threshold.v1"]);
    expect(all[1]?.payload).toEqual({ tenant_id: t, month: "2026-10", pct: 100, state: "HARD" });
  });

  it("raising the limit after HARD lets the next crossing alert again", async () => {
    const t = tenant(20);
    await store.setBudget(t, "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    await store.settle(await store.reserve(ask(t, 1_000)), { tokensIn: 1_000, tokensOut: 0 });
    expect(await budgetRow(t)).toMatchObject({ alert_state: "HARD" });
    await store.setBudget(t, "2026-10", { limitIn: 10_000, limitOut: 10_000, softPct: 80 });
    expect(await budgetRow(t)).toMatchObject({ alert_state: "OK" });
    await store.settle(await store.reserve(ask(t, 7_500)), { tokensIn: 7_500, tokensOut: 0 });
    await store.settle(await store.reserve(ask(t, 1_500)), { tokensIn: 1_500, tokensOut: 0 });
    expect((await events(t)).map((e) => (e.payload as { state: string }).state)).toEqual(["HARD", "SOFT", "HARD"]);
  });

  it("runAgent audits RUNNING at the start, stores the actions at the end, and refuses a reused run id before any tool call", async () => {
    const t = tenant(21);
    const seenAtStart: unknown[] = [];
    const audit: AuditPort = {
      startRun: async (r) => {
        await store.startRun(r);
        seenAtStart.push((await runRow(t, r.runId))?.status);
      },
      finishRun: (r, res, after) => store.finishRun(r, res, after),
    };
    const first = committingRun({ budgets: store, audit });
    const result = await runAgent(first.deps, makeRequest({ runId: "audited", tenantId: t, runTokenCap: 2_000 }));
    expect(result.status).toBe("COMMITTED");
    expect(seenAtStart).toEqual(["RUNNING"]);
    expect(await runRow(t, "audited")).toMatchObject({ status: "COMMITTED" });
    expect(await store.loadActions(t, "audited")).toEqual(result.actions);
    expect(await budgetRow(t)).toMatchObject({ used_in: 1_200, used_out: 180 });
    expect(await holds(t)).toBe(0);

    const again = committingRun({ budgets: store, audit: store });
    await expect(runAgent(again.deps, makeRequest({ runId: "audited", tenantId: t, runTokenCap: 2_000 }))).rejects.toThrow();
    expect(again.tools.calls).toHaveLength(0);
    expect(again.provider.seen).toHaveLength(0);
    expect(await holds(t)).toBe(0);
    expect(await budgetRow(t)).toMatchObject({ used_in: 1_200 });
  });

  it("a start rejection leaves a FAILED audit row and no hold", async () => {
    const t = tenant(22);
    const run = committingRun({ budgets: store, audit: store, killSwitch: { isHalted: () => true } });
    await expect(runAgent(run.deps, makeRequest({ runId: "halted", tenantId: t, runTokenCap: 2_000 }))).rejects.toMatchObject({ code: "AGT-0201" });
    expect(await runRow(t, "halted")).toMatchObject({ status: "FAILED" });
    expect(await holds(t)).toBe(0);
  });

  it("a failing settle after the commit leaves the run FAILED_AFTER_COMMIT with its action rows, and the hold expires later", async () => {
    const t = tenant(23);
    const budgets: BudgetPort = { reserve: (r) => store.reserve(r), settle: async () => Promise.reject(new Error("budget db down")) };
    const run = committingRun({ budgets, audit: store });
    const result = await runAgent(run.deps, makeRequest({ runId: "unsettled", tenantId: t, runTokenCap: 2_000 }));
    expect(result).toMatchObject({ status: "COMMITTED", postCommitError: "budget db down" });
    expect(run.tools.calls).toHaveLength(1);
    expect(await runRow(t, "unsettled")).toMatchObject({ status: "FAILED_AFTER_COMMIT", post_commit_error: "budget db down" });
    expect(await store.loadActions(t, "unsettled")).toEqual(result.actions);
    expect(await holds(t)).toBe(1);
  });

  it("a failing audit write after the commit still returns the COMMITTED result and settles the budget", async () => {
    const t = tenant(24);
    const audit: AuditPort = { startRun: (r) => store.startRun(r), finishRun: async () => Promise.reject(new Error("audit db down")) };
    const run = committingRun({ budgets: store, audit });
    const result = await runAgent(run.deps, makeRequest({ runId: "unaudited", tenantId: t, runTokenCap: 2_000 }));
    expect(result).toMatchObject({ status: "COMMITTED", postCommitError: "audit db down" });
    expect(await budgetRow(t)).toMatchObject({ used_in: 1_200, used_out: 180 });
    expect(await runRow(t, "unaudited")).toMatchObject({ status: "RUNNING" });
  });
});
