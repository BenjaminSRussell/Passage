import { createHash } from "node:crypto";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";
import { Pseudonymizer } from "../src/domain/redaction.js";
import type {
  ApprovalDecision,
  ApprovalPort,
  ToolDescriptor,
  ToolPort,
  ToolResult,
} from "../src/runtime/engine.js";
import { runAgent } from "../src/runtime/engine.js";
import {
  APPROVER,
  codingDescriptor,
  FakeApprovals,
  FakeTools,
  INVOKER,
  makeDeps,
  makeRequest,
  postDescriptor,
  proposal,
  REAL_IBAN,
  ScriptedProvider,
  TENANT,
  usd,
} from "./fakes.js";

// Wave 47 proof: "Every agent action produces an audit entry with inputs, tool
// calls, outputs and approver". Once the model has proposed an action, every
// way the run can end leaves exactly one agent_action row for it, and a human
// decision is never lost from that row.

const POST = "passage.ap.invoice.post";
const CODING = "passage.ap.invoice.propose_coding";
const READ = "passage.ap.invoice.get";
const autoPolicy = {
  mode: "AUTO" as const,
  autoThreshold: 0.95,
  autoAmountLimit: usd("5000"),
};
const agentWithRead = {
  ...makeRequest().agent,
  allowedTools: [POST, CODING, READ],
};
const tools = (result?: ToolResult) =>
  new FakeTools(
    {
      [POST]: postDescriptor,
      [CODING]: codingDescriptor,
      [READ]: { requiredDuty: "APInvoicePost", approvalMode: "none" },
    },
    result,
  );
const approve = () =>
  new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER });
/** Kill switch that reports a halt from the n-th check on (check 1 is the start gate). */
const killFromCheck = (n: number) => {
  let checks = 0;
  return { isHalted: () => ++checks >= n };
};

describe("audit row on every path that holds a proposal (AC3)", () => {
  it("a read-only tool proposed as the action is recorded DENIED with its redacted input", async () => {
    const t = tools();
    const result = await runAgent(
      makeDeps({
        provider: new ScriptedProvider([proposal(0.9, "1200", READ)]),
        tools: t,
        approvals: approve(),
      }),
      makeRequest({ agent: agentWithRead }),
    );
    expect(result.status).toBe("FAILED");
    expect(t.calls).toHaveLength(0);
    expect(result.actions).toHaveLength(1);
    expect(result.actions[0]).toMatchObject({
      seq: 1,
      toolName: READ,
      decision: "DENIED",
      approverUserId: null,
      committedEntityRef: null,
    });
    expect(result.actions[0]?.inputJson).toBe(
      '{"invoiceId":"INV-7","remitTo":"[IBAN-1]"}',
    );
  });

  it("an approval followed by a kill before the commit keeps the approver on the row", async () => {
    const t = tools();
    const deps = makeDeps({
      provider: new ScriptedProvider([proposal(0.8)]),
      tools: t,
      approvals: approve(),
    });
    // Checks: 1 start, 2 before provider, 3 after the reply, 4 before the gate, 5 before the commit.
    deps.killSwitch = killFromCheck(5);
    const result = await runAgent(deps, makeRequest());
    expect(result.status).toBe("KILLED");
    expect(t.calls).toHaveLength(0);
    expect(result.actions).toEqual([
      expect.objectContaining({
        seq: 1,
        toolName: POST,
        decision: "APPROVED",
        approverUserId: APPROVER,
        committedEntityRef: null,
        reason: "KILLED",
      }),
    ]);
    // sha256 of the literal output, computed outside the engine (python3 -I, hashlib.sha256).
    expect(result.actions[0]?.outputJson).toBe(
      '{"killed":"kill switch activated before commit"}',
    );
    expect(result.actions[0]?.outputHash).toBe(
      "c12ebe3321df04ffd17cd25741f9960958061ed560411f88bf098b97f0ff56bd",
    );
  });

  it("a kill after the model replied, before routing, records the halted proposal DENIED", async () => {
    const deps = makeDeps({
      provider: new ScriptedProvider([proposal(0.8)]),
      tools: tools(),
      approvals: approve(),
    });
    deps.killSwitch = killFromCheck(3);
    const result = await runAgent(deps, makeRequest());
    expect(result.status).toBe("KILLED");
    expect(result.actions).toEqual([
      expect.objectContaining({
        toolName: POST,
        decision: "DENIED",
        approverUserId: null,
        reason: "KILLED",
      }),
    ]);
  });

  it("a kill just before the approval gate records the halted proposal DENIED and opens no task", async () => {
    const approvals = approve();
    const deps = makeDeps({
      provider: new ScriptedProvider([proposal(0.8)]),
      tools: tools(),
      approvals,
    });
    deps.killSwitch = killFromCheck(4);
    const result = await runAgent(deps, makeRequest());
    expect(result.status).toBe("KILLED");
    expect(approvals.requests).toHaveLength(0);
    expect(result.actions).toEqual([
      expect.objectContaining({
        toolName: POST,
        decision: "DENIED",
        approverUserId: null,
        reason: "KILLED",
      }),
    ]);
  });

  it("a tool port that throws after approval still records the attempted call with its approver", async () => {
    const t: ToolPort = {
      describe: (n) => tools().describe(n),
      call: async () => {
        throw new Error("socket hang up");
      },
    };
    const result = await runAgent(
      makeDeps({
        provider: new ScriptedProvider([proposal(0.8)]),
        tools: t,
        approvals: approve(),
      }),
      makeRequest(),
    );
    expect(result).toMatchObject({
      status: "FAILED",
      reason: "socket hang up",
    });
    expect(result.actions).toEqual([
      expect.objectContaining({
        toolName: POST,
        decision: "APPROVED",
        approverUserId: APPROVER,
        committedEntityRef: null,
      }),
    ]);
  });

  it("a tool port that throws on an AUTO commit records the AUTO decision", async () => {
    const t: ToolPort = {
      describe: (n) => tools().describe(n),
      call: async () => Promise.reject(new Error("timeout")),
    };
    const result = await runAgent(
      makeDeps({
        provider: new ScriptedProvider([proposal(0.97, "1200", CODING)]),
        tools: t,
        approvals: approve(),
      }),
      makeRequest({ policy: autoPolicy }),
    );
    expect(result.status).toBe("FAILED");
    expect(result.actions).toEqual([
      expect.objectContaining({
        toolName: CODING,
        decision: "AUTO",
        approverUserId: null,
      }),
    ]);
  });

  it("an inbox that throws records the proposal DENIED with no approver", async () => {
    const approvals: ApprovalPort = {
      request: async () => Promise.reject(new Error("inbox unavailable")),
    };
    const result = await runAgent(
      makeDeps({
        provider: new ScriptedProvider([proposal(0.8)]),
        tools: tools(),
        approvals,
      }),
      makeRequest(),
    );
    expect(result).toMatchObject({
      status: "FAILED",
      reason: "inbox unavailable",
    });
    expect(result.actions).toEqual([
      expect.objectContaining({
        toolName: POST,
        decision: "DENIED",
        approverUserId: null,
      }),
    ]);
  });

  it("a committed action carries input, output, output hash, approver and the committed reference", async () => {
    const t = tools({
      ok: true,
      output: { postingId: "P-100", status: "POSTED" },
      ref: "P-100",
    });
    const result = await runAgent(
      makeDeps({
        provider: new ScriptedProvider([proposal(0.8)]),
        tools: t,
        approvals: approve(),
      }),
      makeRequest(),
    );
    expect(result.actions).toEqual([
      {
        runId: "run-1",
        seq: 1,
        toolName: POST,
        inputJson: '{"invoiceId":"INV-7","remitTo":"[IBAN-1]"}',
        outputJson: '{"postingId":"P-100","status":"POSTED"}',
        // python3 -I -c 'import hashlib;print(hashlib.sha256(b"{\"postingId\":\"P-100\",\"status\":\"POSTED\"}").hexdigest())'
        outputHash:
          "c5e6b66805e1de6f9f37b7641f14477af66def8ac451c8af5a52c059c37d1759",
        outputTruncated: false,
        approverUserId: APPROVER,
        decision: "APPROVED",
        decidedAt: "2026-10-21T00:00:00.000Z",
        committedEntityRef: "P-100",
        reason: null,
      },
    ]);
  });
});

describe("a refused audit row does not change how the run ends (#389)", () => {
  const OUTPUT_MISS = "missed-by-redactor";
  const realRedact = Pseudonymizer.prototype.redact;
  /** A redaction miss: text matching `when` passes the redactor untouched, so the write-time scanner refuses it (AGT-0210). */
  const missRedaction = (when: (text: string) => boolean) =>
    vi.spyOn(Pseudonymizer.prototype, "redact").mockImplementation(function (
      this: Pseudonymizer,
      text: string,
    ) {
      return when(text) ? text : realRedact.call(this, text);
    });
  const rawArgs = { invoiceId: "INV-7", remitTo: REAL_IBAN };
  const noPiiPrompt = makeRequest({ prompt: "Code invoice INV-7 from Acme." });
  afterEach(() => vi.restoreAllMocks());

  it("a kill whose closing row is refused still ends KILLED, with no row", async () => {
    missRedaction(() => true);
    const deps = makeDeps({
      provider: new ScriptedProvider([proposal(0.8, "1200", POST, rawArgs)]),
      tools: tools(),
      approvals: approve(),
    });
    deps.killSwitch = killFromCheck(3);
    const result = await runAgent(deps, noPiiPrompt);
    expect(result).toMatchObject({
      status: "KILLED",
      code: undefined,
      reason: "kill switch activated",
    });
    expect(result.actions).toHaveLength(0);
  });

  it("a commit whose row is refused after the tool call stays COMMITTED and records the reference", async () => {
    missRedaction((text) => text.includes(OUTPUT_MISS));
    const t = tools({
      ok: true,
      output: { postingId: "P-100", note: OUTPUT_MISS, remitTo: REAL_IBAN },
      ref: "P-100",
    });
    const result = await runAgent(
      makeDeps({
        provider: new ScriptedProvider([proposal(0.8)]),
        tools: t,
        approvals: approve(),
      }),
      makeRequest(),
    );
    expect(t.calls).toHaveLength(1);
    expect(result).toMatchObject({
      status: "COMMITTED",
      code: undefined,
      reason: "committed; tool output not recorded",
    });
    expect(result.actions).toEqual([
      expect.objectContaining({
        seq: 1,
        toolName: POST,
        inputJson: '{"invoiceId":"INV-7","remitTo":"[IBAN-1]"}',
        outputJson: '{"committed":"P-100"}',
        decision: "APPROVED",
        approverUserId: APPROVER,
        committedEntityRef: "P-100",
        reason: "committed; tool output not recorded",
      }),
    ]);
  });

  it("an input the redactor missed is never committed: no tool call, FAILED AGT-0210, one refusal row", async () => {
    missRedaction((text) => text.includes(REAL_IBAN));
    const t = tools({ ok: true, output: { postingId: "P-100" }, ref: "P-100" });
    const result = await runAgent(
      makeDeps({
        provider: new ScriptedProvider([proposal(0.8, "1200", POST, rawArgs)]),
        tools: t,
        approvals: approve(),
      }),
      noPiiPrompt,
    );
    expect(t.calls).toHaveLength(0);
    expect(result).toMatchObject({ status: "FAILED", code: "AGT-0210" });
    expect(result.actions).toEqual([
      expect.objectContaining({
        seq: 1,
        toolName: POST,
        decision: "DENIED",
        approverUserId: APPROVER,
        committedEntityRef: null,
        inputJson: '{"withheld":"AGT-0210"}',
      }),
    ]);
  });
});

/** mulberry32: a small seeded PRNG so the property run is the same on every machine. */
function prng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

describe("audit and authorization invariants over generated runs (AC2, AC3)", () => {
  const DUTIES = [
    "APInvoicePost",
    "ARCashApply",
    "LedgerPost",
    "APInvoiceRead",
  ];
  const CASH = "passage.ar.cash.apply";
  const OTHER_IBAN = "GB82WEST12345698765432";
  const LEDGER = "passage.ledger.journal.reverse";
  const descriptors: Record<string, ToolDescriptor> = {
    [POST]: postDescriptor,
    [CODING]: codingDescriptor,
    [CASH]: { requiredDuty: "ARCashApply", approvalMode: "suggest" },
    [READ]: { requiredDuty: "APInvoiceRead", approvalMode: "none" },
    [LEDGER]: {
      requiredDuty: "LedgerPost",
      approvalMode: "approve_before_commit",
    },
  };
  // LEDGER is described by the gateway but not on the agent's allow-list.
  const agent = {
    ...makeRequest().agent,
    allowedTools: [POST, CODING, CASH, READ],
  };
  const pick = <T>(r: () => number, xs: readonly T[]): T =>
    xs[Math.floor(r() * xs.length)] as T;

  const propertyRun = async (persisted?: Fixture): Promise<void> => {
    const r = prng(47);
    const counts = {
      committed: 0,
      denied: 0,
      killed: 0,
      rejected: 0,
      failed: 0,
      rawArgs: 0,
    };
    for (let i = 0; i < 5000; i++) {
      const duties = new Set(DUTIES.filter(() => r() < 0.5));
      const tool = pick(r, [
        POST,
        CODING,
        CASH,
        READ,
        LEDGER,
        "passage.unknown.tool",
      ]);
      const confidence = pick(r, [0.5, 0.8, 0.96, 0.99]);
      const amount = pick(r, ["10", "1200", "9000"]);
      const decision: ApprovalDecision | "throw" = pick(r, [
        { decision: "APPROVED", approverUserId: APPROVER },
        { decision: "APPROVED", approverUserId: INVOKER },
        {
          decision: "REJECTED",
          approverUserId: APPROVER,
          reason: "wrong account",
        },
        { decision: "REJECTED", approverUserId: null, reason: "EXPIRED" },
        "throw",
      ] as const);
      const toolOutcome = pick(r, ["ok", "error", "throw"] as const);
      const killAt = pick(r, [0, 0, 0, 2, 3, 4, 5]);
      // Model args that echo a raw IBAN (the prompt's or a new one) must still reach the row redacted.
      const args = pick(r, [
        { invoiceId: `INV-${i}`, remitTo: "[IBAN-1]" },
        { invoiceId: `INV-${i}`, remitTo: REAL_IBAN },
        {
          invoiceId: `INV-${i}`,
          remitTo: OTHER_IBAN,
          note: `was ${REAL_IBAN}`,
        },
      ]);
      const rawArgs = JSON.stringify(args).includes(REAL_IBAN);

      const approvalsSeen: ApprovalDecision[] = [];
      const approvals: ApprovalPort = {
        request: async () => {
          if (decision === "throw") throw new Error("inbox down");
          approvalsSeen.push(decision);
          return decision;
        },
      };
      const calls: string[] = [];
      const port: ToolPort = {
        describe: (n) => descriptors[n],
        call: async (n) => {
          calls.push(n);
          if (toolOutcome === "throw") throw new Error("port down");
          return toolOutcome === "ok"
            ? { ok: true, output: { id: `doc-${i}` }, ref: `doc-${i}` }
            : { ok: false, code: "AGT-0214", message: "409" };
        },
      };
      const provider = new ScriptedProvider([
        proposal(confidence, amount, tool, args),
      ]);
      const deps = makeDeps({ provider, tools: port, approvals });
      if (killAt > 0) deps.killSwitch = killFromCheck(killAt);
      if (persisted) deps.audit = persisted.store;
      const res = await runAgent(
        deps,
        makeRequest({
          runId: `run-${i}`,
          agent,
          userDuties: duties,
          policy: r() < 0.5 ? autoPolicy : makeRequest().policy,
        }),
      );

      if (persisted)
        expect(await persisted.store.loadActions(TENANT, `run-${i}`), `run ${i}`).toEqual(res.actions);
      // A kill before the provider call means no proposal exists: no row, no call.
      if (provider.seen.length === 0) {
        expect(res.status).toBe("KILLED");
        expect(res.actions).toHaveLength(0);
        expect(calls).toHaveLength(0);
        counts.killed++;
        continue;
      }
      // AC3: exactly one row for the proposal, with input, output and its hash.
      expect(res.actions, `run ${i}`).toHaveLength(1);
      const row = res.actions[0]!;
      expect(row.toolName).toBe(tool);
      expect(row.inputJson, `run ${i}`).not.toContain(REAL_IBAN);
      expect(row.inputJson, `run ${i}`).not.toContain(OTHER_IBAN);
      expect(row.inputJson).toContain(`"invoiceId":"INV-${i}"`);
      if (rawArgs) {
        expect(row.inputJson).toMatch(/"remitTo":"\[IBAN-\d\]"/);
        counts.rawArgs++;
      }
      expect(row.outputHash).toBe(
        createHash("sha256").update(row.outputJson).digest("hex"),
      );
      expect(row.decidedAt).toBe("2026-10-21T00:00:00.000Z");
      // A human decision is on the row whenever the inbox returned one.
      if (approvalsSeen.length === 1)
        expect(row.approverUserId).toBe(approvalsSeen[0]!.approverUserId);
      else expect(row.approverUserId).toBeNull();
      // Every tool call is on the row with a committing decision.
      for (const c of calls) {
        expect(c).toBe(row.toolName);
        expect(["APPROVED", "AUTO"]).toContain(row.decision);
      }
      expect(row.committedEntityRef !== null).toBe(res.status === "COMMITTED");

      // AC2: the agent never calls past the invoking user's duties or its allow-list.
      for (const c of calls) {
        const d = descriptors[c]!;
        expect(
          duties.has(d.requiredDuty),
          `run ${i}: ${c} without ${d.requiredDuty}`,
        ).toBe(true);
        expect(agent.allowedTools).toContain(c);
        expect(d.approvalMode).not.toBe("none");
        // approve_before_commit is only ever called after a human approved it.
        if (d.approvalMode === "approve_before_commit")
          expect(row.decision).toBe("APPROVED");
      }
      if (res.status === "COMMITTED") counts.committed++;
      else if (res.status === "KILLED") counts.killed++;
      else if (res.status === "REJECTED") counts.rejected++;
      else if (row.decision === "DENIED") counts.denied++;
      else counts.failed++;
    }
    // The generator reaches every outcome, so the invariants were exercised on
    // each. The mix is adversarial (random duties, off-list tools, kills), so
    // commits are the rarest outcome; seed 47 gives a fixed count per outcome.
    for (const [k, n] of Object.entries(counts))
      expect(n, k).toBeGreaterThanOrEqual(20);
    if (persisted) {
      const runs = await persisted.owner.query("select count(*)::int as n from agent_run");
      expect(runs.rows[0].n).toBe(5000);
    }
  };

  it("for 5,000 seeded runs: one row per proposal, no call beyond the user's duties, no lost approver", () => propertyRun());

  describe.runIf(ownerUrl)("persisted in Postgres", () => {
    let db: Fixture;
    beforeAll(async () => {
      db = await fixture();
    });
    afterAll(() => db.close());

    it("for 5,000 seeded runs every run and action row is stored and reads back identical", () => propertyRun(db), 600_000);
  });
});
