import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";
import type { BudgetHold } from "../src/runtime/engine.js";
import { AgentError } from "../src/errors.js";
import { addUsage, budgetStatus, type TokenBudget } from "../src/domain/budget.js";
import { budgetThresholdEvent, type BudgetThresholdData } from "../src/domain/events.js";
import { runAgent } from "../src/runtime/engine.js";
import { APPROVER, FakeApprovals, FakeTools, makeDeps, makeRequest, postDescriptor, proposal, ScriptedProvider } from "./fakes.js";

// Wave 47 proof: "Cost guard: per-tenant token budget enforced and alerted"
// (FR-47.11, AC-47.4, AC-47.7). A per-tenant ledger drives the engine run
// after run; the alert is budgetThresholdEvent on each charge. Every expected
// number below is hand-derived from the fixture usage (1,200 in / 180 out per
// run, from fakes.proposal) and the budget limits, not read from the engine.

const A = "00000000-0000-7000-8000-0000000000aa";
const B = "00000000-0000-7000-8000-0000000000bb";

interface Outcome {
  started: boolean;
  code?: string;
  providerCalls: number;
  alert?: BudgetThresholdData;
}

/** The per-tenant ledger a run worker keeps: read the budget, run, charge, alert. */
class Ledger {
  readonly budgets = new Map<string, TokenBudget>();

  async run(tenantId: string): Promise<Outcome> {
    const before = this.budgets.get(tenantId)!;
    const provider = new ScriptedProvider([proposal(0.8)]);
    const deps = makeDeps({ provider, tools: new FakeTools({ "passage.ap.invoice.post": postDescriptor }), approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) });
    try {
      const res = await runAgent(deps, makeRequest({ tenantId, budget: before }));
      const after = addUsage(before, { tokensIn: res.tokensIn, tokensOut: res.tokensOut, estimated: res.estimated });
      this.budgets.set(tenantId, after);
      // The engine's own view of the budget after the run matches the ledger's.
      expect(res.budgetState).toBe(budgetStatus(after).state);
      const evt = budgetThresholdEvent({ tenantId, month: "2026-10", previous: budgetStatus(before).state, current: budgetStatus(after) });
      return { started: true, providerCalls: provider.seen.length, ...(evt ? { alert: evt.data } : {}) };
    } catch (err) {
      return { started: false, code: (err as Error).message.slice(0, 8), providerCalls: provider.seen.length };
    }
  }
}

describe("per-tenant cost guard (AC4)", () => {
  it("alerts once at the soft threshold, once at the hard stop, then rejects new runs without a provider call", async () => {
    const ledger = new Ledger();
    // 10,000 input tokens: each run uses 12% of input and 9% of output (180 / 2,000).
    ledger.budgets.set(A, { limitIn: 10_000, limitOut: 2_000, usedIn: 0, usedOut: 0, softPct: 80 });
    const outcomes: Outcome[] = [];
    for (let i = 0; i < 10; i++) outcomes.push(await ledger.run(A));

    // Runs 1-6: 12%..72% of input, no alert.
    for (const o of outcomes.slice(0, 6)) expect(o).toEqual({ started: true, providerCalls: 1 });
    // Run 7: 84% -> SOFT, alerted once (AGT-0205 warning).
    expect(outcomes[6]).toEqual({ started: true, providerCalls: 1, alert: { tenant_id: A, month: "2026-10", pct: 84, state: "SOFT" } });
    // Run 8: 96%, still SOFT, no second alert.
    expect(outcomes[7]).toEqual({ started: true, providerCalls: 1 });
    // Run 9 starts below 100% (96%) and ends at 108% -> HARD, alerted once.
    expect(outcomes[8]).toEqual({ started: true, providerCalls: 1, alert: { tenant_id: A, month: "2026-10", pct: 108, state: "HARD" } });
    // Run 10: rejected at start with BUDGET_EXHAUSTED, and no provider call (no spend).
    expect(outcomes[9]).toEqual({ started: false, code: "AGT-0202", providerCalls: 0 });
    // 9 runs charged: 10,800 in, 1,620 out.
    expect(ledger.budgets.get(A)).toMatchObject({ usedIn: 10_800, usedOut: 1_620 });
  });

  it("one tenant's exhausted budget never blocks or charges another tenant", async () => {
    const ledger = new Ledger();
    ledger.budgets.set(A, { limitIn: 1_000, limitOut: 1_000, usedIn: 1_000, usedOut: 0, softPct: 80 });
    ledger.budgets.set(B, { limitIn: 100_000, limitOut: 100_000, usedIn: 0, usedOut: 0, softPct: 80 });
    expect(await ledger.run(A)).toEqual({ started: false, code: "AGT-0202", providerCalls: 0 });
    expect(await ledger.run(B)).toEqual({ started: true, providerCalls: 1 });
    expect(ledger.budgets.get(A)).toMatchObject({ usedIn: 1_000, usedOut: 0 });
    expect(ledger.budgets.get(B)).toMatchObject({ usedIn: 1_200, usedOut: 180 });
  });

  it("the output limit trips the guard on its own when input is far from its limit", async () => {
    const ledger = new Ledger();
    // Output: 180 of 200 = 90% -> SOFT on the first run; the second reaches 180% -> HARD.
    ledger.budgets.set(A, { limitIn: 1_000_000, limitOut: 200, usedIn: 0, usedOut: 0, softPct: 80 });
    expect((await ledger.run(A)).alert).toEqual({ tenant_id: A, month: "2026-10", pct: 90, state: "SOFT" });
    expect((await ledger.run(A)).alert).toEqual({ tenant_id: A, month: "2026-10", pct: 180, state: "HARD" });
    expect(await ledger.run(A)).toEqual({ started: false, code: "AGT-0202", providerCalls: 0 });
  });

  it("a run that breaks the per-run cap is still charged, and the charge can raise the alert", async () => {
    const before: TokenBudget = { limitIn: 1_000_000, limitOut: 1_000_000, usedIn: 500_000, usedOut: 0, softPct: 80 };
    const big = { ...proposal(0.8), usage: { tokensIn: 300_000, tokensOut: 150_000 } };
    const t = new FakeTools({ "passage.ap.invoice.post": postDescriptor });
    const res = await runAgent(
      makeDeps({ provider: new ScriptedProvider([big]), tools: t, approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }),
      makeRequest({ tenantId: A, budget: before }),
    );
    expect(res).toMatchObject({ status: "FAILED", code: "AGT-0202", tokensIn: 300_000, tokensOut: 150_000 });
    expect(t.calls).toHaveLength(0);
    const after = addUsage(before, { tokensIn: res.tokensIn, tokensOut: res.tokensOut, estimated: false });
    // 800,000 / 1,000,000 = 80% -> SOFT.
    expect(budgetThresholdEvent({ tenantId: A, month: "2026-10", previous: "OK", current: budgetStatus(after) })?.data).toEqual({
      tenant_id: A,
      month: "2026-10",
      pct: 80,
      state: "SOFT",
    });
  });
});

describe.runIf(ownerUrl)("persisted token budget in Postgres (AC4)", () => {
  let db: Fixture;
  beforeAll(async () => {
    db = await fixture();
  });
  afterAll(() => db.close());

  const hold = (agentId: string, tokens: number, tenantId = A): BudgetHold => ({ tenantId, agentId, month: "2026-10", tokensIn: tokens, tokensOut: tokens });
  const budgetRow = async (agentId: string, tenantId = A) =>
    (await db.owner.query("select used_in::int, used_out::int, reserved_in::int, reserved_out::int, alert_state from token_budget where tenant_id = $1 and agent_id = $2", [tenantId, agentId])).rows[0];
  const events = async (agentId: string) =>
    (await db.owner.query("select type, payload from outbox where aggregate_id = $1 order by created_at, id", [`${agentId}/2026-10`])).rows;

  it("40 parallel runs against a 1,000 token budget: exactly 10 reserve 100, and settled usage never exceeds the limit", async () => {
    await db.store.setBudget(A, "race", "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    const outcomes = await Promise.allSettled(
      Array.from({ length: 40 }, async () => {
        await db.store.reserve(hold("race", 100));
        await db.store.settle(hold("race", 100), { tokensIn: 100, tokensOut: 100 });
      }),
    );
    const rejected = outcomes.filter((o): o is PromiseRejectedResult => o.status === "rejected");
    expect(outcomes.filter((o) => o.status === "fulfilled")).toHaveLength(10);
    expect(rejected).toHaveLength(30);
    for (const o of rejected) expect(o.reason).toMatchObject({ code: "AGT-0202" });
    expect(await budgetRow("race")).toMatchObject({ used_in: 1_000, used_out: 1_000, reserved_in: 0, reserved_out: 0 });
  });

  it("reservations count against the limit before any run settles, and a release frees them", async () => {
    await db.store.setBudget(A, "hold", "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    await Promise.all(Array.from({ length: 10 }, () => db.store.reserve(hold("hold", 100))));
    await expect(db.store.reserve(hold("hold", 1))).rejects.toBeInstanceOf(AgentError);
    await db.store.settle(hold("hold", 100), { tokensIn: 0, tokensOut: 0 });
    await db.store.reserve(hold("hold", 100));
    expect(await budgetRow("hold")).toMatchObject({ used_in: 0, reserved_in: 1_000 });
  });

  it("a tenant with no budget row cannot reserve", async () => {
    await expect(db.store.reserve(hold("unknown", 1))).rejects.toMatchObject({ code: "AGT-0202" });
  });

  it("a budget of one tenant is invisible to another under row-level security", async () => {
    await db.store.setBudget(A, "race", "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    const seen = await db.store.withTenant(B, (c) => c.query("select 1 from token_budget"));
    expect(seen.rowCount).toBe(0);
    await expect(db.store.reserve(hold("race", 1, B))).rejects.toMatchObject({ code: "AGT-0202" });
  });

  it("writes budget.threshold to the outbox once per crossing, even when settles race", async () => {
    await db.store.setBudget(A, "alerts", "2026-10", { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    await Promise.all(Array.from({ length: 9 }, () => db.store.reserve(hold("alerts", 100))));
    await Promise.all(Array.from({ length: 9 }, () => db.store.settle(hold("alerts", 100), { tokensIn: 100, tokensOut: 0 })));
    expect((await events("alerts")).map((e) => e.payload)).toEqual([{ tenant_id: A, month: "2026-10", pct: 80, state: "SOFT" }]);
    await db.store.reserve(hold("alerts", 100));
    await db.store.settle(hold("alerts", 100), { tokensIn: 100, tokensOut: 0 });
    const all = await events("alerts");
    expect(all.map((e) => e.type)).toEqual(["dev.passage.agent.budget.threshold.v1", "dev.passage.agent.budget.threshold.v1"]);
    expect(all[1]?.payload).toEqual({ tenant_id: A, month: "2026-10", pct: 100, state: "HARD" });
    expect(await budgetRow("alerts")).toMatchObject({ alert_state: "HARD" });
  });

  it("runAgent reserves the run cap, charges the actual usage, persists the audit rows, and rejects a run the budget cannot cover", async () => {
    await db.store.setBudget(A, "ap_coding", "2026-10", { limitIn: 3_000, limitOut: 3_000, softPct: 80 });
    const run = (runId: string) => {
      const deps = makeDeps({ provider: new ScriptedProvider([proposal(0.8)]), tools: new FakeTools({ "passage.ap.invoice.post": postDescriptor }), approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) });
      return runAgent({ ...deps, budgets: db.store, audit: db.store }, makeRequest({ runId, tenantId: A, runTokenCap: 2_000 }));
    };
    const first = await run("budget-run-1");
    expect(first.status).toBe("COMMITTED");
    expect(await budgetRow("ap_coding")).toMatchObject({ used_in: 1_200, used_out: 180, reserved_in: 0, reserved_out: 0 });
    expect(await db.store.loadActions(A, "budget-run-1")).toEqual(first.actions);
    await expect(run("budget-run-2")).rejects.toMatchObject({ code: "AGT-0202" });
    expect(await budgetRow("ap_coding")).toMatchObject({ used_in: 1_200, reserved_in: 0 });
  });
});
