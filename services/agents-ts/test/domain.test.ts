import { describe, expect, it } from "vitest";
import { buildActionRecord, OUTPUT_CAP_BYTES } from "../src/domain/audit.js";
import { addUsage, assertCanStartRun, assertPromptWithinCap, assertRunWithinCap, budgetStatus, estimateTokens, outputBudgetForCall, type TokenBudget } from "../src/domain/budget.js";
import { canCallTool, selfApprovalBlocked } from "../src/domain/authz.js";
import { compareMoney, scaled6, sumMoney } from "../src/domain/money.js";
import { CASH_APP_MAX_INVOICES_DEFAULT, checkCashApplication, decideDisposition, isApprovalExpired, validatePolicy, type ModePolicy, type Proposal } from "../src/domain/policy.js";
import { promptHash, VersionRegistry, type AgentVersion } from "../src/domain/registry.js";
import { canTransition, transition } from "../src/domain/run.js";
import { AgentError } from "../src/errors.js";
import { usd } from "./fakes.js";

const auto: ModePolicy = { mode: "AUTO", autoThreshold: 0.95, autoAmountLimit: usd("5000") };
const base: Proposal = { confidence: 0.97, amount: usd("1200"), vendorHighRisk: false, sodConflict: false };

describe("money", () => {
  it("scales decimal strings exactly to millionths", () => {
    expect(scaled6("0")).toBe(0n);
    expect(scaled6("1.5")).toBe(1_500_000n);
    expect(scaled6("-0.000001")).toBe(-1n);
    expect(scaled6("12345.678901")).toBe(12_345_678_901n);
  });

  it("rejects floats, exponents and too many decimals", () => {
    for (const bad of ["1e3", "1.0000001", "abc", "", "1,000"]) {
      expect(() => scaled6(bad), bad).toThrow();
    }
  });

  it("compares amounts and refuses currency mismatch", () => {
    expect(compareMoney(usd("4999.99"), usd("5000"))).toBe(-1);
    expect(compareMoney(usd("5000.000000"), usd("5000"))).toBe(0);
    expect(compareMoney(usd("5000.01"), usd("5000"))).toBe(1);
    expect(() => compareMoney(usd("1"), { amount: "1", currency: "EUR" })).toThrow(/currency mismatch/);
  });
});

describe("decideDisposition (BR-47.2)", () => {
  const cases: Array<{ name: string; policy: ModePolicy; proposal: Proposal; want: string }> = [
    { name: "off blocks everything", policy: { ...auto, mode: "OFF" }, proposal: base, want: "BLOCKED" },
    { name: "suggest always proposes", policy: { ...auto, mode: "SUGGEST" }, proposal: base, want: "PROPOSE" },
    { name: "auto passes all gates", policy: auto, proposal: base, want: "AUTO" },
    { name: "confidence 0.80 goes to the gate (AC-47.5)", policy: auto, proposal: { ...base, confidence: 0.8 }, want: "PROPOSE" },
    { name: "amount equal to limit goes to the gate", policy: auto, proposal: { ...base, amount: usd("5000") }, want: "PROPOSE" },
    { name: "high-risk vendor goes to the gate", policy: auto, proposal: { ...base, vendorHighRisk: true }, want: "PROPOSE" },
    { name: "SoD conflict goes to the gate", policy: auto, proposal: { ...base, sodConflict: true }, want: "PROPOSE" },
    { name: "currency differs from the limit", policy: auto, proposal: { ...base, amount: { amount: "1", currency: "EUR" } }, want: "PROPOSE" },
  ];
  for (const c of cases) {
    it(c.name, () => expect(decideDisposition(c.policy, c.proposal)).toBe(c.want));
  }

  it("validates the threshold range and a non-negative limit", () => {
    expect(validatePolicy({ ...auto, autoThreshold: 0.7 })).toHaveLength(1);
    expect(validatePolicy({ ...auto, autoAmountLimit: usd("-1") })).toHaveLength(1);
    expect(validatePolicy(auto)).toEqual([]);
  });

  it("expires approvals at 72 hours (BR-47.3)", () => {
    const t0 = Date.UTC(2026, 9, 1);
    expect(isApprovalExpired(t0, t0 + 72 * 3_600_000 - 1, 72)).toBe(false);
    expect(isApprovalExpired(t0, t0 + 72 * 3_600_000, 72)).toBe(true);
  });
});

describe("authorization", () => {
  it("canCallTool is exactly the user's duty set", () => {
    expect(canCallTool(new Set(["APInvoicePost"]), "APInvoicePost")).toBe(true);
    expect(canCallTool(new Set(["APInvoiceRead"]), "APInvoicePost")).toBe(false);
  });

  it("blocks self-approval only above the limit", () => {
    const inv = "u1";
    expect(selfApprovalBlocked({ approverUserId: inv, invokerUserId: inv, amount: usd("1001"), selfApprovalLimit: usd("1000") })).toBe(true);
    expect(selfApprovalBlocked({ approverUserId: inv, invokerUserId: inv, amount: usd("1000"), selfApprovalLimit: usd("1000") })).toBe(false);
    expect(selfApprovalBlocked({ approverUserId: "u2", invokerUserId: inv, amount: usd("9999"), selfApprovalLimit: usd("1000") })).toBe(false);
  });
});

describe("buildActionRecord", () => {
  const input = { runId: "r", seq: 1, toolName: "passage.ap.invoice.post", inputRedacted: '{"remitTo":"[IBAN-1]"}', outputRedacted: '{"ref":"p-1"}', approverUserId: "u2", decision: "APPROVED" as const, decidedAt: "2026-10-21T00:00:00Z" };

  it("refuses raw PII in stored input (AGT-0210)", () => {
    expect(() => buildActionRecord({ ...input, inputRedacted: '{"remitTo":"DE89370400440532013000"}' })).toThrow("AGT-0210");
  });

  it("refuses a committed reference unless the decision approved or auto-committed", () => {
    expect(() => buildActionRecord({ ...input, decision: "DENIED", committedEntityRef: "p-1" })).toThrow(/committed_entity_ref/);
    expect(buildActionRecord({ ...input, committedEntityRef: "p-1" }).committedEntityRef).toBe("p-1");
  });

  it("hashes the stored output and truncates past the 64 KB cap", () => {
    const small = buildActionRecord(input);
    expect(small.outputHash).toHaveLength(64);
    expect(small.outputTruncated).toBe(false);
    const big = buildActionRecord({ ...input, outputRedacted: "x".repeat(OUTPUT_CAP_BYTES + 10) });
    expect(big.outputTruncated).toBe(true);
    expect(big.outputJson.length).toBe(OUTPUT_CAP_BYTES);
  });
});

describe("version registry (FR-47.10)", () => {
  const v = (version: number): AgentVersion => ({ agentId: "ap_coding", version, modelId: "m", systemPrompt: "p", allowedTools: ["a", "b"], requiredDuties: [] });

  it("refuses to promote a version without a passing eval (AGT-0204)", () => {
    const reg = new VersionRegistry();
    reg.register(v(1));
    expect(() => reg.promote("ap_coding", 1)).toThrow("AGT-0204");
    reg.recordEval({ agentId: "ap_coding", version: 1, evalRunId: "e1", passed: false });
    expect(() => reg.promote("ap_coding", 1)).toThrow("AGT-0204");
  });

  it("promotes, then rolls back by pointer", () => {
    const reg = new VersionRegistry();
    reg.register(v(1));
    reg.register(v(2));
    reg.recordEval({ agentId: "ap_coding", version: 1, evalRunId: "e1", passed: true });
    reg.recordEval({ agentId: "ap_coding", version: 2, evalRunId: "e2", passed: true });
    reg.promote("ap_coding", 1);
    reg.promote("ap_coding", 2);
    expect(reg.production("ap_coding")?.version).toBe(2);
    expect(reg.rollback("ap_coding").version).toBe(1);
    expect(() => reg.rollback("ap_coding")).toThrow(/no earlier version/);
  });

  it("hashes prompts independently of tool order", () => {
    expect(promptHash({ ...v(1), allowedTools: ["b", "a"] })).toBe(promptHash({ ...v(1), allowedTools: ["a", "b"] }));
    expect(promptHash({ ...v(1), systemPrompt: "changed" })).not.toBe(promptHash(v(1)));
  });
});

describe("run state machine (BR-47.1)", () => {
  it("allows the planned paths and nothing out of a terminal state", () => {
    expect(transition("PLANNING", "ACTING")).toBe("ACTING");
    expect(transition("ACTING", "AWAITING_APPROVAL")).toBe("AWAITING_APPROVAL");
    expect(transition("AWAITING_APPROVAL", "COMMITTED")).toBe("COMMITTED");
    expect(canTransition("COMMITTED", "ACTING")).toBe(false);
    expect(canTransition("KILLED", "FAILED")).toBe(false);
    expect(() => transition("PLANNING", "COMMITTED")).toThrow(/invalid run transition/);
  });
});

describe("token budgets (FR-47.11)", () => {
  const b = (usedIn: number, usedOut = 0): TokenBudget => ({ limitIn: 1000, limitOut: 100, usedIn, usedOut, softPct: 80 });

  it("is OK below the soft threshold, SOFT at 80%, HARD at 100%", () => {
    expect(budgetStatus(b(799)).state).toBe("OK");
    expect(budgetStatus(b(800)).state).toBe("SOFT");
    expect(budgetStatus(b(1000)).state).toBe("HARD");
    expect(budgetStatus(b(0, 100)).state).toBe("HARD");
  });

  it("uses the highest of input and output consumption", () => {
    expect(budgetStatus(b(10, 90)).pct).toBeCloseTo(90);
  });

  it("refuses a start at HARD and a run past its cap", () => {
    expect(() => assertCanStartRun(b(1000))).toThrow("AGT-0202");
    expect(() => assertCanStartRun(b(10))).not.toThrow();
    expect(() => assertRunWithinCap(401, 400)).toThrow("AGT-0202");
    expect(outputBudgetForCall(400, 0, 100)).toBe(300);
    expect(() => outputBudgetForCall(400, 0, 400)).toThrow("AGT-0202");
    expect(() => outputBudgetForCall(400, 450, 1)).toThrow("AGT-0202");
  });

  it("bounds the prompt by UTF-8 bytes of all parts together, so CJK and emoji cannot slip under the cap", () => {
    expect(() => assertPromptWithinCap(["abcd", "ef"], 6)).not.toThrow();
    expect(() => assertPromptWithinCap(["abcd", "efg"], 6)).toThrow("AGT-0202");
    // 3 CJK characters are 9 bytes and an emoji is 4: chars/4 would say 2 tokens.
    expect(estimateTokens("請求書😀")).toBe(2);
    expect(() => assertPromptWithinCap(["請求書😀"], 12)).toThrow("13 bytes");
    expect(() => assertPromptWithinCap(["請求書😀"], 13)).not.toThrow();
  });

  it("estimates tokens at four characters each and accumulates usage", () => {
    expect(estimateTokens("abcd")).toBe(1);
    expect(estimateTokens("abcde")).toBe(2);
    expect(addUsage(b(1), { tokensIn: 2, tokensOut: 3, estimated: false }).usedIn).toBe(3);
    expect(() => addUsage(b(0), { tokensIn: -1, tokensOut: 0, estimated: false })).toThrow();
  });
});

describe("errors", () => {
  it("carries the stable code in the message", () => {
    const e = new AgentError("AGT-0201", "halted");
    expect(e.code).toBe("AGT-0201");
    expect(e.message).toBe("AGT-0201: halted");
  });
});

describe("cash application limits (BR-47.10)", () => {
  const limits = { maxInvoices: 20, writeOffTolerance: usd("25") };
  const alloc = (n: number, amount = "10") => Array.from({ length: n }, (_, i) => ({ invoiceId: `INV-${i}`, amount: usd(amount) }));

  it("accepts 20 invoices and a write-off at the tolerance", () => {
    expect(checkCashApplication({ payment: usd("200"), allocations: alloc(20), writeOff: usd("25") }, limits)).toEqual([]);
  });

  it("refuses a 21st invoice", () => {
    expect(checkCashApplication({ payment: usd("210"), allocations: alloc(21) }, limits).join(" ")).toContain("at most 20");
  });

  it("refuses a write-off above the tolerance", () => {
    expect(checkCashApplication({ payment: usd("10"), allocations: alloc(1), writeOff: usd("25.01") }, limits).join(" ")).toContain("exceeds the tolerance");
  });

  it("refuses allocations above the payment, duplicates, non-positive amounts and foreign currency", () => {
    expect(checkCashApplication({ payment: usd("19.999999"), allocations: alloc(2) }, limits).join(" ")).toContain("exceed the payment");
    expect(checkCashApplication({ payment: usd("20"), allocations: [...alloc(1), ...alloc(1)] }, limits).join(" ")).toContain("settled twice");
    expect(checkCashApplication({ payment: usd("20"), allocations: alloc(1, "0") }, limits).join(" ")).toContain("positive");
    expect(checkCashApplication({ payment: usd("20"), allocations: [{ invoiceId: "e", amount: { amount: "1", currency: "EUR" } }] }, limits)).toHaveLength(1);
    expect(checkCashApplication({ payment: usd("20"), allocations: [] }, limits)).toHaveLength(1);
  });

  it("checks allocations and the write-off against the invoices' open amounts", () => {
    const open = (id: string, amount: string, openAmount: string) => ({ invoiceId: id, amount: usd(amount), openAmount: usd(openAmount) });
    // Customer pays 249.50 on a 250.00 invoice: apply it all, write off the 0.50 left open.
    expect(checkCashApplication({ payment: usd("249.50"), allocations: [open("i1", "249.50", "250.00")], writeOff: usd("0.50") }, limits)).toEqual([]);
    // Overpayment: 10 stays on account, nothing is written off.
    expect(checkCashApplication({ payment: usd("260"), allocations: [open("i1", "250", "250")] }, limits)).toEqual([]);
    expect(checkCashApplication({ payment: usd("300"), allocations: [open("i1", "250.01", "250")] }, limits).join(" ")).toContain("exceeds its open amount");
    expect(checkCashApplication({ payment: usd("250"), allocations: [open("i1", "250", "250")], writeOff: usd("0.01") }, limits).join(" ")).toContain(
      "the allocations leave open",
    );
    expect(checkCashApplication({ payment: usd("10"), allocations: [open("i1", "10", "20")], writeOff: usd("-1") }, limits).join(" ")).toContain("negative");
    expect(
      checkCashApplication({ payment: usd("10"), allocations: [{ invoiceId: "i1", amount: usd("10"), openAmount: { amount: "10", currency: "EUR" } }] }, limits),
    ).toHaveLength(1);
  });

  it("enforces the 1..50 parameter range", () => {
    expect(checkCashApplication({ payment: usd("10"), allocations: alloc(1) }, { ...limits, maxInvoices: 51 }).join(" ")).toContain("between 1 and 50");
    expect(CASH_APP_MAX_INVOICES_DEFAULT).toBe(20);
  });

  it("sums money exactly", () => {
    expect(sumMoney("USD", [usd("0.1"), usd("0.2")])).toEqual(usd("0.300000"));
    expect(sumMoney("USD", [usd("-1.5"), usd("0.25")]).amount).toBe("-1.250000");
  });
});

describe("validatePolicy edge cases", () => {
  it("rejects a NaN threshold, a non-decimal limit and a bad currency", () => {
    expect(validatePolicy({ ...auto, autoThreshold: Number.NaN })).toHaveLength(1);
    expect(validatePolicy({ ...auto, autoAmountLimit: { amount: "5e3", currency: "USD" } })).toHaveLength(1);
    expect(validatePolicy({ ...auto, autoAmountLimit: { amount: "5000", currency: "usd" } })).toHaveLength(1);
  });
});
