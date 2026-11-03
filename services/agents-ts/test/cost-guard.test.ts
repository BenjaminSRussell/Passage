import { describe, expect, it } from "vitest";
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
