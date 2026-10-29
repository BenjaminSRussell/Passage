import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { buildActionRecord } from "../src/domain/audit.js";
import { approvalRequestedEvent, budgetThresholdEvent, killSwitchChangedEvent, runCompletedEvent } from "../src/domain/events.js";
import { buildKillSwitchChange, KILL_PROPAGATION_MS, KillSwitchRegistry } from "../src/domain/killswitch.js";
import type { RunResult } from "../src/runtime/engine.js";

const T1 = "00000000-0000-7000-8000-000000000001";
const T2 = "00000000-0000-7000-8000-000000000002";
const OPERATOR = "00000000-0000-7000-8000-0000000000ff";
const NOW = "2026-10-22T10:00:00Z";

function clock(start = 0) {
  let t = start;
  return {
    now: () => t,
    advance: (ms: number) => {
      t += ms;
    },
  };
}

function globalChange(version: number, active: boolean) {
  return buildKillSwitchChange({ scope: "GLOBAL", active, byUser: OPERATOR, version, changedAt: NOW });
}

function tenantChange(tenantId: string, version: number, active: boolean) {
  return buildKillSwitchChange({ scope: "TENANT", tenantId, active, byUser: OPERATOR, version, changedAt: NOW });
}

describe("KillSwitchRegistry (AC-47.6, BR-47.5)", () => {
  it("applies tenant and global toggles and ignores duplicates and stale versions", () => {
    const c = clock();
    const reg = new KillSwitchRegistry(c.now);
    expect(reg.isHalted(T1)).toBe(false);

    expect(reg.apply(tenantChange(T1, 1, true))).toBe(true);
    expect(reg.isHalted(T1)).toBe(true);
    expect(reg.isHalted(T2)).toBe(false);

    expect(reg.apply(tenantChange(T1, 1, true))).toBe(false); // duplicate
    expect(reg.apply(tenantChange(T1, 1, false))).toBe(false);
    expect(reg.isHalted(T1)).toBe(true);

    expect(reg.apply(tenantChange(T1, 2, false))).toBe(true);
    expect(reg.isHalted(T1)).toBe(false);

    expect(reg.apply(globalChange(3, true))).toBe(true);
    expect(reg.isHalted(T2)).toBe(true);
    expect(reg.apply(globalChange(4, false))).toBe(true);
    expect(reg.isHalted(T2)).toBe(false);
    expect(reg.version).toBe(4);
  });

  it("fails closed once the worker has not synced for the propagation window", () => {
    const c = clock();
    const reg = new KillSwitchRegistry(c.now);
    c.advance(KILL_PROPAGATION_MS);
    expect(reg.isStale()).toBe(false);
    c.advance(1);
    expect(reg.isStale()).toBe(true);
    expect(reg.isHalted(T1)).toBe(true);
    reg.heartbeat();
    expect(reg.isHalted(T1)).toBe(false);
  });

  it("a kill applied on one worker halts every tenant it covers within the window (AC-47.6)", () => {
    const c = clock();
    const worker = new KillSwitchRegistry(c.now);
    c.advance(4_000);
    worker.apply(globalChange(1, true));
    c.advance(KILL_PROPAGATION_MS - 1);
    expect(worker.isHalted(T1)).toBe(true);
    expect(worker.isHalted(T2)).toBe(true);
  });
});

describe("buildKillSwitchChange validation", () => {
  it.each([
    ["non-uuid byUser", { scope: "GLOBAL" as const, active: true, byUser: "me", version: 1, changedAt: NOW }],
    ["version zero", { scope: "GLOBAL" as const, active: true, byUser: OPERATOR, version: 0, changedAt: NOW }],
    ["bad timestamp", { scope: "GLOBAL" as const, active: true, byUser: OPERATOR, version: 1, changedAt: "yesterday" }],
    ["tenant scope without tenant", { scope: "TENANT" as const, active: true, byUser: OPERATOR, version: 1, changedAt: NOW }],
  ])("rejects %s", (_name, input) => {
    expect(() => buildKillSwitchChange(input)).toThrow();
  });

  it("global changes carry a null tenant", () => {
    expect(globalChange(1, true)).toMatchObject({ scope: "GLOBAL", tenantId: null });
  });
});

describe("killSwitchChangedEvent", () => {
  it("addresses a global toggle to the operator tenant and keeps the scope explicit", () => {
    const evt = killSwitchChangedEvent(globalChange(7, true), OPERATOR);
    expect(evt.type).toBe("dev.passage.agent.killswitch.changed.v1");
    expect(evt.tenantId).toBe(OPERATOR);
    expect(evt.data).toEqual({ scope: "GLOBAL", tenant_id: null, active: true, version: 7, by_user: OPERATOR, changed_at: NOW });
  });

  it("rejects a non-uuid envelope tenant", () => {
    expect(() => killSwitchChangedEvent(globalChange(1, true), "x")).toThrow();
  });
});

describe("budgetThresholdEvent (AC-47.4, AC-47.7)", () => {
  const soft = (pct: number) => ({ state: "SOFT" as const, pct });
  const hard = (pct: number) => ({ state: "HARD" as const, pct });
  const ok = (pct: number) => ({ state: "OK" as const, pct });

  it.each([
    ["OK to SOFT alerts once", "OK" as const, soft(82.4), "SOFT", 82],
    ["OK to HARD alerts", "OK" as const, hard(101), "HARD", 101],
    ["SOFT to HARD alerts", "SOFT" as const, hard(100.2), "HARD", 100],
  ])("%s", (_name, previous, current, state, pct) => {
    const evt = budgetThresholdEvent({ tenantId: T1, month: "2026-10", previous, current });
    expect(evt?.data).toEqual({ tenant_id: T1, month: "2026-10", pct, state });
  });

  it.each([
    ["SOFT stays SOFT", "SOFT" as const, soft(85)],
    ["HARD stays HARD", "HARD" as const, hard(120)],
    ["OK stays OK", "OK" as const, ok(10)],
    ["HARD falling back to SOFT does not alert", "HARD" as const, soft(90)],
  ])("%s: no event", (_name, previous, current) => {
    expect(budgetThresholdEvent({ tenantId: T1, month: "2026-10", previous, current })).toBeUndefined();
  });

  it("rejects a malformed month", () => {
    expect(() => budgetThresholdEvent({ tenantId: T1, month: "2026-13", previous: "OK", current: soft(81) })).toThrow();
  });
});

function result(overrides: Partial<RunResult> = {}): RunResult {
  const action = buildActionRecord({
    runId: "run-1",
    seq: 1,
    toolName: "passage.ap.invoice.post",
    inputRedacted: '{"remitTo":"[IBAN-1]"}',
    outputRedacted: '{"ok":true}',
    approverUserId: OPERATOR,
    decision: "APPROVED",
    decidedAt: NOW,
    committedEntityRef: "posting-1",
  });
  return {
    runId: "run-1",
    status: "COMMITTED",
    actions: [action],
    tokensIn: 120,
    tokensOut: 45,
    estimated: false,
    budgetState: "OK",
    displayText: "Coded.",
    ...overrides,
  };
}

describe("runCompletedEvent", () => {
  it("summarises tool calls without inputs, outputs or display text", () => {
    const evt = runCompletedEvent({ tenantId: T1, agentId: "ap_coding", version: 3, result: result(), costUsd: "0.012345" });
    expect(evt.type).toBe("dev.passage.agent.run.completed.v1");
    expect(evt.data).toEqual({
      run_id: "run-1",
      agent_id: "ap_coding",
      version: 3,
      status: "COMMITTED",
      code: null,
      tool_calls: [{ seq: 1, tool: "passage.ap.invoice.post", decision: "APPROVED" }],
      tokens_in: 120,
      tokens_out: 45,
      estimated: false,
      cost_usd: "0.012345",
    });
    const wire = JSON.stringify(evt);
    expect(wire).not.toContain("IBAN");
    expect(wire).not.toContain("Coded.");
  });

  it.each([
    ["non-terminal status", { status: "ACTING" as const }, "0.1"],
    ["negative cost", { status: "COMMITTED" as const }, "-1"],
    ["cost with seven places", { status: "COMMITTED" as const }, "0.1234567"],
  ])("rejects %s", (_name, overrides, cost) => {
    expect(() => runCompletedEvent({ tenantId: T1, agentId: "ap_coding", version: 1, result: result(overrides), costUsd: cost })).toThrow();
  });
});

/** The `required: [...]` list of a top-level schema in events/asyncapi.yaml. */
function asyncApiRequired(schema: string): string[] {
  const text = readFileSync(new URL("../events/asyncapi.yaml", import.meta.url), "utf8");
  const block = new RegExp(`\\n    ${schema}:\\n(?:      .*\\n)*?      required: \\[([^\\]]*)\\]`).exec(text);
  if (!block?.[1]) throw new Error(`schema ${schema} not found in asyncapi.yaml`);
  return block[1].split(",").map((f) => f.trim()).sort();
}

describe("event payloads match events/asyncapi.yaml", () => {
  it.each([
    ["RunCompletedData", () => runCompletedEvent({ tenantId: T1, agentId: "ap_coding", version: 3, result: result(), costUsd: "0.01" }).data],
    [
      "ApprovalRequestedData",
      () =>
        approvalRequestedEvent({
          tenantId: T1,
          runId: "run-1",
          actionId: "act-1",
          toolName: "passage.ar.payment.apply",
          proposalRedacted: "apply to INV-1",
          confidence: 0.9,
          riskScore: 0.1,
        }).data,
    ],
    ["BudgetThresholdData", () => budgetThresholdEvent({ tenantId: T1, month: "2026-10", previous: "OK", current: { state: "SOFT", pct: 81 } })?.data],
    ["KillSwitchChangedData", () => killSwitchChangedEvent(globalChange(1, true), OPERATOR).data],
  ])("%s carries exactly the documented fields", (schema, build) => {
    expect(Object.keys(build() ?? {}).sort()).toEqual(asyncApiRequired(schema));
  });
});
