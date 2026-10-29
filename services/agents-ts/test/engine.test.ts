import { describe, expect, it } from "vitest";
import { runAgent } from "../src/runtime/engine.js";
import { APPROVER, codingDescriptor, FakeApprovals, FakeTools, INVOKER, makeDeps, makeRequest, postDescriptor, proposal, REAL_IBAN, ScriptedProvider, usd } from "./fakes.js";

const CODING = "passage.ap.invoice.propose_coding";
const tools = () => new FakeTools({ "passage.ap.invoice.post": postDescriptor, [CODING]: codingDescriptor });
const autoPolicy = { mode: "AUTO" as const, autoThreshold: 0.95, autoAmountLimit: usd("5000") };

describe("runAgent gates and commits", () => {
  it("suggest mode: nothing commits until the approver decides, then the real IBAN reaches the commit (AC-47.5, AC-47.8)", async () => {
    const provider = new ScriptedProvider([proposal(0.8)]);
    const t = tools();
    const approvals = new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER });
    const result = await runAgent(makeDeps({ provider, tools: t, approvals }), makeRequest());

    expect(result.status).toBe("COMMITTED");
    expect(approvals.requests).toHaveLength(1);
    expect(provider.seen[0]?.user).not.toContain(REAL_IBAN);
    expect(provider.seen[0]?.user).toContain("[IBAN-1]");
    expect(t.calls[0]?.args.remitTo).toBe(REAL_IBAN);
    expect(result.displayText).toContain(REAL_IBAN);
    expect(result.actions).toEqual([
      expect.objectContaining({ seq: 1, decision: "APPROVED", approverUserId: APPROVER, committedEntityRef: "posting-1" }),
    ]);
    expect(result.actions[0]?.inputJson).not.toContain(REAL_IBAN);
    expect(approvals.requests[0]?.rationale).toContain(REAL_IBAN);
  });

  it("a rejected proposal calls nothing and ends REJECTED", async () => {
    const t = tools();
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.8)]), tools: t, approvals: new FakeApprovals({ decision: "REJECTED", approverUserId: APPROVER, reason: "wrong vendor" }) }),
      makeRequest(),
    );
    expect(result.status).toBe("REJECTED");
    expect(t.calls).toHaveLength(0);
    expect(result.actions[0]).toMatchObject({ decision: "REJECTED", committedEntityRef: null });
  });

  it("an expired approval ends REJECTED with AGT-0208 and posts nothing (BR-47.3)", async () => {
    const t = tools();
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.8)]), tools: t, approvals: new FakeApprovals({ decision: "REJECTED", approverUserId: null, reason: "EXPIRED" }) }),
      makeRequest(),
    );
    expect(result).toMatchObject({ status: "REJECTED", code: "AGT-0208" });
    expect(t.calls).toHaveLength(0);
  });

  it("auto mode commits a suggest-mode tool without a gate when confidence, amount and risk pass (BR-47.2)", async () => {
    const approvals = new FakeApprovals({ decision: "REJECTED", approverUserId: null });
    const t = tools();
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.97, "1200", CODING)]), tools: t, approvals }),
      makeRequest({ policy: autoPolicy }),
    );
    expect(result.status).toBe("COMMITTED");
    expect(approvals.requests).toHaveLength(0);
    expect(t.calls.map((c) => c.name)).toEqual([CODING]);
    expect(result.actions[0]).toMatchObject({ decision: "AUTO", approverUserId: null });
  });

  it("auto mode never commits an approve_before_commit tool without a human (FR-47.8, no autonomous posting in v1)", async () => {
    const approvals = new FakeApprovals({ decision: "REJECTED", approverUserId: APPROVER });
    const t = tools();
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.99)]), tools: t, approvals }),
      makeRequest({ policy: autoPolicy }),
    );
    expect(approvals.requests).toHaveLength(1);
    expect(result.status).toBe("REJECTED");
    expect(t.calls).toHaveLength(0);
  });

  it("auto mode still asks a human when the amount is at or above the limit", async () => {
    const approvals = new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER });
    await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.97, "5000", CODING)]), tools: tools(), approvals }),
      makeRequest({ policy: autoPolicy }),
    );
    expect(approvals.requests).toHaveLength(1);
  });

  it("uses the system's document amount, not the model's, for the AUTO limit", async () => {
    // The model claims 10 USD; the invoice the system reads is 9,000 USD.
    const approvals = new FakeApprovals({ decision: "REJECTED", approverUserId: APPROVER });
    const t = tools();
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.99, "10", CODING)]), tools: t, approvals, risk: { vendorHighRisk: false, sodConflict: false, amount: usd("9000") } }),
      makeRequest({ policy: autoPolicy }),
    );
    expect(approvals.requests[0]?.amount).toEqual(usd("9000"));
    expect(result.status).toBe("REJECTED");
    expect(t.calls).toHaveLength(0);
  });

  it("a high-risk vendor sends an otherwise auto-eligible proposal to the gate", async () => {
    const approvals = new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER });
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.99, "100", CODING)]), tools: tools(), approvals, risk: { vendorHighRisk: true, sodConflict: false } }),
      makeRequest({ policy: autoPolicy }),
    );
    expect(approvals.requests).toHaveLength(1);
    expect(result.actions[0]).toMatchObject({ decision: "APPROVED", approverUserId: APPROVER });
  });

  it("refuses an approval by the invoker above the self-approval limit (AGT-0209)", async () => {
    const t = tools();
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.8)]), tools: t, approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: INVOKER }) }),
      makeRequest(),
    );
    expect(result).toMatchObject({ status: "REJECTED", code: "AGT-0209" });
    expect(t.calls).toHaveLength(0);
  });

  it("a commit that fails after approval ends FAILED with the tool's code", async () => {
    const t = new FakeTools({ "passage.ap.invoice.post": postDescriptor }, { ok: false, code: "AGT-0214", message: "tool failed with 409" });
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.8)]), tools: t, approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }),
      makeRequest(),
    );
    expect(result).toMatchObject({ status: "FAILED", code: "AGT-0214" });
    expect(result.actions[0]).toMatchObject({ decision: "APPROVED", committedEntityRef: null });
  });
});

describe("runAgent authorization (AC-47.2)", () => {
  it("never calls a tool the invoking user lacks the duty for, and audits DENIED", async () => {
    const t = tools();
    const approvals = new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER });
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.99)]), tools: t, approvals }),
      makeRequest({ userDuties: new Set(["SomethingElse"]), policy: autoPolicy }),
    );
    expect(result).toMatchObject({ status: "FAILED", code: "AGT-0203" });
    expect(t.calls).toHaveLength(0);
    expect(approvals.requests).toHaveLength(0);
    expect(result.actions[0]).toMatchObject({ decision: "DENIED", toolName: "passage.ap.invoice.post" });
  });

  it("never calls a tool outside the agent's allow-list, even if the model asks for it", async () => {
    const t = new FakeTools({ "passage.ledger.journal.reverse": { requiredDuty: "LedgerPost", approvalMode: "approve_before_commit" } });
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([proposal(0.99, "1200", "passage.ledger.journal.reverse")]), tools: t, approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }),
      makeRequest({ userDuties: new Set(["LedgerPost"]) }),
    );
    expect(result.status).toBe("FAILED");
    expect(t.calls).toHaveLength(0);
    expect(result.actions[0]?.decision).toBe("DENIED");
  });
});

describe("runAgent start rejections and limits", () => {
  it("rejects a start while the kill switch is on (AC-47.6)", async () => {
    await expect(
      runAgent(makeDeps({ provider: new ScriptedProvider([]), tools: tools(), approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }), kill: { on: true } }), makeRequest()),
    ).rejects.toThrow("AGT-0201");
  });

  it("rejects a start when the monthly budget is exhausted (AC-47.7)", async () => {
    await expect(
      runAgent(
        makeDeps({ provider: new ScriptedProvider([]), tools: tools(), approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }),
        makeRequest({ budget: { limitIn: 100, limitOut: 100, usedIn: 100, usedOut: 0, softPct: 80 } }),
      ),
    ).rejects.toThrow("AGT-0202");
  });

  it("rejects a start without a passing eval for the version (AGT-0204)", async () => {
    await expect(
      runAgent(makeDeps({ provider: new ScriptedProvider([]), tools: tools(), approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }), makeRequest({ evalPassed: false })),
    ).rejects.toThrow("AGT-0204");
  });

  it("a kill during planning stops the run before any tool call (BR-47.5)", async () => {
    const kill = { on: false };
    const provider = new ScriptedProvider([{ ...proposal(0.99), text: "ok" }]);
    const originalComplete = provider.complete.bind(provider);
    provider.complete = async (req) => {
      kill.on = true;
      return originalComplete(req);
    };
    const t = tools();
    const result = await runAgent(makeDeps({ provider, tools: t, approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }), kill }), makeRequest());
    expect(result.status).toBe("KILLED");
    expect(t.calls).toHaveLength(0);
  });

  it("a kill that lands before the first provider call makes no provider call at all (AC-47.6)", async () => {
    const provider = new ScriptedProvider([proposal(0.99)]);
    const deps = makeDeps({ provider, tools: tools(), approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) });
    let checks = 0;
    // First check is the start gate; every later check reports the kill.
    deps.killSwitch = { isHalted: () => ++checks > 1 };
    const result = await runAgent(deps, makeRequest());
    expect(result).toMatchObject({ status: "KILLED" });
    expect(provider.seen).toHaveLength(0);
  });

  it("runs past the per-run token cap end FAILED with AGT-0202, and the spent tokens still count", async () => {
    const big = { ...proposal(0.8), usage: { tokensIn: 300_000, tokensOut: 150_000 } };
    const t = tools();
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([big]), tools: t, approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }),
      makeRequest({ budget: { limitIn: 1_000_000, limitOut: 1_000_000, usedIn: 500_000, usedOut: 0, softPct: 80 } }),
    );
    expect(result).toMatchObject({ status: "FAILED", code: "AGT-0202", tokensIn: 300_000 });
    expect(result.budgetState).toBe("SOFT");
    expect(t.calls).toHaveLength(0);
  });

  it("a provider that throws fails the run instead of leaving it open", async () => {
    const provider = { complete: async () => { throw new Error("provider returned 529"); } };
    const result = await runAgent(
      makeDeps({ provider, tools: tools(), approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }),
      makeRequest(),
    );
    expect(result).toMatchObject({ status: "FAILED", reason: "provider returned 529" });
  });

  it("an inbox that throws while awaiting approval fails the run and posts nothing", async () => {
    const t = tools();
    const approvals = { request: async () => { throw new Error("inbox unavailable"); } };
    const result = await runAgent(makeDeps({ provider: new ScriptedProvider([proposal(0.8)]), tools: t, approvals }), makeRequest());
    expect(result).toMatchObject({ status: "FAILED", reason: "inbox unavailable" });
    expect(t.calls).toHaveLength(0);
  });

  it("refuses to start with a policy outside the parameter ranges", async () => {
    await expect(
      runAgent(
        makeDeps({ provider: new ScriptedProvider([]), tools: tools(), approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }),
        makeRequest({ policy: { mode: "AUTO", autoThreshold: 0.5, autoAmountLimit: usd("5000") } }),
      ),
    ).rejects.toThrow(/auto_threshold/);
  });

  it("a model reply without a proposal ends FAILED", async () => {
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([{ text: "I am not sure." }]), tools: tools(), approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }),
      makeRequest(),
    );
    expect(result).toMatchObject({ status: "FAILED", reason: "model returned no proposal" });
  });

  it("marks estimated usage when the provider reports none (BR-47.4) and reports budget state", async () => {
    const noUsage = { text: "Proposed [IBAN-1]." , proposal: { ...proposal(0.8).proposal! } };
    const result = await runAgent(
      makeDeps({ provider: new ScriptedProvider([noUsage]), tools: tools(), approvals: new FakeApprovals({ decision: "REJECTED", approverUserId: APPROVER }) }),
      makeRequest({ budget: { limitIn: 50_000_000, limitOut: 10_000_000, usedIn: 42_000_000, usedOut: 0, softPct: 80 } }),
    );
    expect(result.estimated).toBe(true);
    expect(result.tokensIn).toBeGreaterThan(0);
    expect(result.budgetState).toBe("SOFT");
  });
});
