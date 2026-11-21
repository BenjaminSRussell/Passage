import { afterEach, describe, expect, it, vi } from "vitest";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { MAX_LOOKBEHIND_CHARS, Pseudonymizer, scanPii } from "../src/domain/redaction.js";
import { createCopilotService } from "../src/runtime/copilot.js";
import type { ModelResponse } from "../src/runtime/engine.js";
import { AMOUNT_WITHHELD_NOTICE } from "../src/domain/financialFields.js";
import { createProviderFactory, loadConfig, withEgressGuard } from "../src/config.js";
import { PROPOSE_TOOL_NAME } from "../src/providers/wire.js";
import { AgentError } from "../src/errors.js";
import { EGRESS_GUARD_TOOL, SEAM_WINDOW_CHARS, type Analyzer, type EgressInput, type GuardDecision } from "../src/runtime/egressGuard.js";
import { Metrics } from "../src/server.js";
import { openRouter, ScriptedProvider, testGuard, allowAiAccess } from "./fakes.js";
import { FakeMcp, headers, MemoryStore, serve, tool } from "./surface-fakes.js";

const GET = "depot.ap.invoice.get";
const SEPARATOR_LENGTH = 2;
const DUTIES = "mcp.copilot.use,ap.invoice.read";
const CANARIES = {
  iban: "DE89370400440532013000",
  bankAccount: "00123456789",
  card: "5500 0000 0000 0004",
  ein: "12-3456789",
  ssn: "078-05-1120",
  salary: "85000.50",
  typedCard: "4111 1111 1111 1111",
};
const toolOutput = {
  id: "INV-7",
  total: "1200.00",
  iban: CANARIES.iban,
  bankAccount: CANARIES.bankAccount,
  paidWith: CANARIES.card,
  ein: CANARIES.ein,
  ssn: CANARIES.ssn,
  salary: CANARIES.salary,
};
const answer = (text: string): ModelResponse => ({ text, usage: { tokensIn: 100, tokensOut: 20 } });
const call = (): ModelResponse => ({
  text: "",
  proposal: { tool: GET, args: { invoiceId: "INV-7" }, confidence: 1, amount: { amount: "0", currency: "USD" } },
  usage: { tokensIn: 200, tokensOut: 10 },
});

const realSetTimeout = globalThis.setTimeout;
const realClearTimeout = globalThis.clearTimeout;
const ANALYZER_REACH_WAIT_MS = 2000;

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  vi.useRealTimers();
  await close?.();
  close = undefined;
});

interface ChatOptions {
  analyzer?: Analyzer;
  observe?: (seconds: number, decision: GuardDecision) => void;
  deadlineMs?: number;
}

const chatWith = async ({ analyzer, observe, deadlineMs }: ChatOptions = {}) => {
  const store = new MemoryStore();
  const provider = new ScriptedProvider([call(), answer("Done.")]);
  let reached = () => {};
  const analyzerReached = new Promise<void>((resolve) => (reached = resolve));
  const watched: Analyzer | undefined = analyzer && {
    scan: (text) => {
      reached();
      return analyzer.scan(text);
    },
  };
  const timed = deadlineMs !== undefined && analyzer !== undefined;
  const copilot = createCopilotService({
    aiAccess: allowAiAccess(),
    router: openRouter(provider),
    mcp: new FakeMcp([tool(GET, "ap.invoice.read")], { [GET]: { ok: true, output: toolOutput, ref: "INV-7" } }),
    store,
    killSwitch: { isHalted: () => false },
    cache: new CopilotAnswerCache(),
    config: { tokenCap: 20_000, maxTurns: 3, pseudonymTtlMs: 60_000 },
    modelId: "claude-haiku-5-5",
    guard: testGuard({ ...(watched !== undefined ? { analyzer: watched } : {}), ...(observe !== undefined ? { observe } : {}), ...(deadlineMs !== undefined ? { deadlineMs } : {}) }),
  });
  const srv = await serve({ approvals: store, copilot });
  close = srv.close;
  if (timed) vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const pending = fetch(`${srv.base}/api/v1/copilot/chat`, {
    method: "POST",
    headers: headers(DUTIES),
    body: JSON.stringify({ mode: "ask", message: `Check invoice INV-7 paid with ${CANARIES.typedCard}` }),
  });
  if (timed) {
    let giveUp: NodeJS.Timeout | undefined;
    const neverReached = new Promise<never>((_, reject) => {
      giveUp = realSetTimeout(() => reject(new Error(`the analyzer was not reached within ${ANALYZER_REACH_WAIT_MS} ms of the chat request`)), ANALYZER_REACH_WAIT_MS);
    });
    await Promise.race([analyzerReached, neverReached]).finally(() => realClearTimeout(giveUp));
    await vi.advanceTimersByTimeAsync(deadlineMs);
  }
  const res = await pending;
  const rows = [...store.actionRows.values()].flat();
  return { body: (await res.json()) as { answer: string; dataClass: string; refusal: { code: string } | null }, provider, rows };
};

describe("egress guard before the provider", () => {
  it("replaces every financial canary in tool results and typed text with a stable pseudonym", async () => {
    const { body, provider, rows } = await chatWith();
    expect(provider.seen).toHaveLength(2);
    for (const seen of provider.seen) {
      for (const value of Object.values(CANARIES)) expect(seen.user).not.toContain(value);
      expect(scanPii(seen.user)).toEqual([]);
    }
    const second = provider.seen[1]?.user ?? "";
    for (const pseudonym of ["[IBAN-1]", "[BANKACCT-1]", "[CARD-1]", "[CARD-2]", "[TAXID-1]", "[SSN-1]"]) expect(second).toContain(pseudonym);
    expect(second).not.toContain("salary");
    expect(body.answer).toContain(AMOUNT_WITHHELD_NOTICE);
    expect(provider.seen[0]?.user).toContain("[CARD-1]");
    expect(body.dataClass).toBe("P3");
    const guardRows = rows.filter((r) => r.toolName === EGRESS_GUARD_TOOL);
    expect(guardRows.map((r) => r.guardResult?.decision)).toEqual(["REDACTED", "REDACTED"]);
    expect(guardRows[1]?.guardResult?.counts).toEqual({ CARD: 2, IBAN: 1, BANKACCT: 1, TAXID: 1, SSN: 1 });
    expect(guardRows[1]?.guardResult?.excluded).toEqual({ AMOUNT_WITHHELD: 1 });
    const stored = JSON.stringify(rows);
    for (const value of Object.values(CANARIES)) expect(stored).not.toContain(value);
    expect(scanPii(stored)).toEqual([]);
  });

  it("blocks with ANALYZER_ERROR and calls no provider when the analyzer throws", async () => {
    const { body, provider, rows } = await chatWith({
      analyzer: {
        scan: () => {
          throw new Error(`down ${CANARIES.iban}`);
        },
      },
    });
    expect(body.refusal?.code).toBe("AGT-0219");
    expect(provider.seen).toHaveLength(0);
    expect(rows[0]?.guardResult).toMatchObject({ decision: "BLOCKED", errorCode: "ANALYZER_ERROR", counts: {} });
    expect(JSON.stringify(rows)).not.toContain(CANARIES.iban);
  });

  it("blocks with ANALYZER_TIMEOUT when the analyzer misses the deadline", async () => {
    const { body, provider, rows } = await chatWith({ analyzer: { scan: () => new Promise(() => undefined) }, deadlineMs: 50 });
    expect(body.refusal?.code).toBe("AGT-0219");
    expect(provider.seen).toHaveLength(0);
    expect(rows[0]?.guardResult).toMatchObject({ decision: "BLOCKED", errorCode: "ANALYZER_TIMEOUT" });
  });

  it("blocks with RESIDUAL_PII when a finding survives redaction", async () => {
    const { provider, rows } = await chatWith({ analyzer: { scan: () => [{ category: "IBAN", value: "kept" }] } });
    expect(provider.seen).toHaveLength(0);
    expect(rows[0]?.guardResult).toMatchObject({ decision: "BLOCKED", errorCode: "RESIDUAL_PII" });
  });

  it("exports the duration histogram by decision and never a value", async () => {
    const metrics = new Metrics();
    await chatWith({ observe: (seconds, decision) => metrics.observeGuard(seconds, decision) });
    const text = metrics.render();
    expect(text).toContain('agents_egress_guard_duration_seconds_count{decision="REDACTED"} 2');
    expect(text).toContain('agents_egress_guard_duration_seconds_bucket{decision="REDACTED",le="+Inf"} 2');
    for (const value of Object.values(CANARIES)) expect(text).not.toContain(value);
  });
});

const recordingAnalyzer = (seen: string[]): Analyzer => ({
  scan: (text) => {
    seen.push(text);
    return scanPii(text);
  },
});

describe("guard inspect", () => {
  const input = (pseudo: Pseudonymizer, over: Partial<EgressInput> = {}): EgressInput => ({
    tenantId: "t",
    callId: "c",
    routeId: "r",
    pseudo,
    system: "system",
    typed: "hello",
    results: [],
    ...over,
  });

  it("allows clean text unchanged with an empty record", async () => {
    const verdict = await testGuard().inspect(input(new Pseudonymizer("t", 1000, () => 0), { results: ["Result of x: INV-2026-0042 1,200.50"] }));
    expect(verdict.result).toMatchObject({ decision: "ALLOWED", highestClass: "P0", counts: {}, excluded: {}, routeId: "r", policyVersion: 1 });
    expect(verdict.user).toBe("hello\n\nResult of x: INV-2026-0042 1,200.50");
  });

  it("keeps one pseudonym per value across calls with the same pseudonymizer", async () => {
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard();
    const first = await guard.inspect(input(pseudo, { typed: `iban ${CANARIES.iban}` }));
    const second = await guard.inspect(input(pseudo, { results: [`other ${CANARIES.iban}`] }));
    expect(first.user).toBe("iban [IBAN-1]");
    expect(second.user).toBe("hello\n\nother [IBAN-1]");
    expect(first.typedReplaced).toBe(1);
  });

  it("carries this turn's tokens, record ids and highest class in the context", async () => {
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const verdict = await testGuard().inspect(input(pseudo, { typed: `iban ${CANARIES.iban}`, recordIds: new Set(["R-1"]) }));
    expect([...verdict.context.inputTokens]).toEqual(["[IBAN-1]"]);
    expect([...verdict.context.inputRecordIds]).toEqual(["R-1"]);
    expect(verdict.context.highestClass).toBe("P3");
  });

  it("scans each distinct text and the system prompt once per pseudonymizer across turns and keeps the full-scan verdict", async () => {
    const seen: string[] = [];
    const analyzer = recordingAnalyzer(seen);
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard({ analyzer });
    const first = `a ${CANARIES.iban}`;
    const second = `b ${CANARIES.ssn}`;
    await guard.inspect(input(pseudo, { results: [first] }));
    const turn = await guard.inspect(input(pseudo, { results: [first, second] }));
    const countOf = (text: string) => seen.filter((t) => t === text).length;
    expect(countOf(first)).toBe(1);
    expect(countOf(second)).toBe(1);
    expect(countOf("system")).toBe(1);
    expect(countOf("hello")).toBe(1);
    expect(turn.result).toMatchObject({ decision: "REDACTED", counts: { IBAN: 1, SSN: 1 } });
    expect(turn.user).toBe("hello\n\na [IBAN-1]\n\nb [SSN-1]");
  });

  it("scans the same text again under a different guard", async () => {
    const seen: string[] = [];
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const text = `x ${CANARIES.iban}`;
    await testGuard({ analyzer: recordingAnalyzer(seen) }).inspect(input(pseudo, { results: [text] }));
    await testGuard({ analyzer: recordingAnalyzer(seen) }).inspect(input(pseudo, { results: [text] }));
    expect(seen.filter((t) => t === text)).toHaveLength(2);
  });

  it("scans the same text again under a different pseudonymizer", async () => {
    const seen: string[] = [];
    const analyzer = recordingAnalyzer(seen);
    const guard = testGuard({ analyzer });
    const text = `x ${CANARIES.iban}`;
    await guard.inspect(input(new Pseudonymizer("t", 1000, () => 0), { results: [text] }));
    await guard.inspect(input(new Pseudonymizer("t", 1000, () => 0), { results: [text] }));
    expect(seen.filter((t) => t === text)).toHaveLength(2);
  });

  it("blocks a labelled value that only forms across the blank line between two segments", async () => {
    const verdict = await testGuard().inspect(input(new Pseudonymizer("t", 1000, () => 0), { typed: "the account", results: ["123456789 is ours"] }));
    expect(scanPii("the account")).toEqual([]);
    expect(scanPii("123456789 is ours")).toEqual([]);
    expect(verdict.result).toMatchObject({ decision: "BLOCKED", errorCode: "RESIDUAL_PII" });
    expect(verdict.user).toBe("");
  });

  it("blocks a labelled value split across the system prompt and the first user segment", async () => {
    const verdict = await testGuard().inspect(input(new Pseudonymizer("t", 1000, () => 0), { system: "You help with the account", typed: "123456789 is ours" }));
    expect(scanPii("You help with the account")).toEqual([]);
    expect(verdict.result).toMatchObject({ decision: "BLOCKED", errorCode: "RESIDUAL_PII" });
  });

  const word = (n: number) => "x".repeat(n);
  const afterLabel = `"   :`;
  const labelledSpans: Array<{ category: string; label: string; value: string; widened: string[] }> = [
    { category: "PAY", label: `compensation${word(12)}"   =   {${"y".repeat(80)}"amount"   :`, value: `"USD 85000`, widened: [`compensation${word(13)}"   =   {${"y".repeat(80)}"amount"   :`, `compensation${word(12)}"   =   {${"y".repeat(81)}"amount"   :`] },
    { category: "SSN", label: `ssn${word(12)}${afterLabel}`, value: "123456789", widened: [`ssn${word(13)}${afterLabel}`] },
    { category: "TAXID", label: `GSTIN_number.${afterLabel}`, value: "DE 12-3456789", widened: [] },
    { category: "BANKACCT", label: `account${word(12)} number.${afterLabel}`, value: "123456789", widened: [`account${word(13)} number.${afterLabel}`] },
  ];

  it.each(labelledSpans)("blocks the longest $category label-to-value span that ends one segment and starts the next", async ({ category, label, value }) => {
    expect(label.length).toBeLessThanOrEqual(MAX_LOOKBEHIND_CHARS);
    expect(scanPii(`${label}\n\n${value}`).map((f) => f.category)).toEqual([category]);
    expect(scanPii(label)).toEqual([]);
    expect(scanPii(`${value} and more`)).toEqual([]);
    const verdict = await testGuard().inspect(input(new Pseudonymizer("t", 1000, () => 0), { typed: `${"filler ".repeat(200)}${label}`, results: [`${value} and more`] }));
    expect(verdict.result).toMatchObject({ decision: "BLOCKED", errorCode: "RESIDUAL_PII" });
  });

  it.each(labelledSpans.flatMap((c) => c.widened.map((label) => ({ category: c.category, label, value: c.value }))))("does not match a $category label wider than the bound was sized for", ({ label, value }) => {
    expect(scanPii(`${label}\n\n${value}`)).toEqual([]);
  });

  it("keeps the window bound above every labelled span", () => {
    expect(SEAM_WINDOW_CHARS).toBeGreaterThan(MAX_LOOKBEHIND_CHARS);
    expect(Math.max(...labelledSpans.map((c) => c.label.length))).toBeLessThanOrEqual(MAX_LOOKBEHIND_CHARS);
  });

  const cutShapes = [
    { name: "a card number after letters", token: "INV4111111111111111,", cutAt: 3 },
    { name: "an SSN after a letter", token: "x123-45-6789,", cutAt: 1 },
    { name: "a tax id after a letter", token: "XTIN:123456789,", cutAt: 1 },
    { name: "a spaced card number", token: "5555 4111 1111 1111 1111,", cutAt: 1 },
    { name: "the tail of a long digit run", token: `${"1".repeat(640)}4111111111111111,`, cutAt: 640 },
  ];

  it.each(cutShapes)("does not block $name that a seam window would start inside", async ({ token, cutAt }) => {
    const system = "system";
    const preamble = `{${'"p":1,'.repeat(100)}"b":"`;
    const typed = `${preamble}${token}${"z".repeat(SEAM_WINDOW_CHARS + cutAt - token.length)}`;
    expect(system.length + SEPARATOR_LENGTH + typed.length - SEAM_WINDOW_CHARS).toBe(system.length + SEPARATOR_LENGTH + preamble.length + cutAt);
    expect(scanPii(typed)).toEqual([]);
    expect(scanPii(typed.slice(preamble.length + cutAt))).not.toEqual([]);
    const verdict = await testGuard().inspect(input(new Pseudonymizer("t", 1000, () => 0), { system, typed, results: ['{"n":1}'] }));
    expect(verdict.result.errorCode).toBeUndefined();
  });

  it("extends a seam window outward to the start of a word run longer than the cut zone", async () => {
    const run = "4111111111111111".repeat(40);
    const seen: string[] = [];
    const verdict = await testGuard({ analyzer: recordingAnalyzer(seen) }).inspect(input(new Pseudonymizer("t", 1000, () => 0), { typed: `${"a ".repeat(300)}${run}`, results: [`${run} end`] }));
    expect(verdict.result.errorCode).toBeUndefined();
    expect(seen.some((t) => t.startsWith(run) && t.includes("\n\n"))).toBe(true);
  });

  it("scans identical seam windows once", async () => {
    const seen: string[] = [];
    await testGuard({ analyzer: recordingAnalyzer(seen) }).inspect(input(new Pseudonymizer("t", 1000, () => 0), { results: ["a", "b", "c"] }));
    expect(seen.filter((t) => t.includes("\n\n"))).toHaveLength(1);
  });

  it("analyses each segment and each seam window once across a growing conversation", async () => {
    const seen: string[] = [];
    const guard = testGuard({ analyzer: recordingAnalyzer(seen) });
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const results = Array.from({ length: 6 }, (_, i) => `Result ${i}: INV-${i}`);
    for (let turn = 1; turn <= results.length; turn++) await guard.inspect(input(pseudo, { results: results.slice(0, turn) }));
    for (const text of ["system", "hello", ...results]) expect(seen.filter((t) => t === text), text).toHaveLength(1);
    expect(new Set(seen).size).toBe(seen.length);
  });

  it("hands the analyzer a bounded number of characters per turn however long the conversation grows", async () => {
    const seen: string[] = [];
    const guard = testGuard({ analyzer: recordingAnalyzer(seen) });
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const results = Array.from({ length: 30 }, (_, i) => `Result ${i}: ${"lorem ipsum ".repeat(25)}${i}`);
    const perTurn: number[] = [];
    for (let turn = 1; turn <= results.length; turn++) {
      const before = seen.length;
      await guard.inspect(input(pseudo, { results: results.slice(0, turn) }));
      perTurn.push(seen.slice(before).reduce((sum, t) => sum + t.length, 0));
    }
    const newest = results[results.length - 1] ?? "";
    const accumulated = results.join("").length;
    const bound = newest.length + 3 * (2 * SEAM_WINDOW_CHARS + SEPARATOR_LENGTH);
    expect(accumulated).toBeGreaterThan(2 * bound);
    for (const scanned of perTurn.slice(4)) expect(scanned).toBeLessThanOrEqual(bound);
  });

  it("blocks on a seam window finding and does not memoise the window after a failed scan", async () => {
    let failing = true;
    const analyzer: Analyzer = { scan: (text) => (failing && text.includes("\n\n") ? (undefined as never) : scanPii(text)) };
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard({ analyzer });
    const failed = await guard.inspect(input(pseudo, { typed: "the account", results: ["123456789 is ours"] }));
    failing = false;
    const retry = await guard.inspect(input(pseudo, { typed: "the account", results: ["123456789 is ours"] }));
    expect(failed.result).toMatchObject({ decision: "BLOCKED", errorCode: "ANALYZER_ERROR" });
    expect(retry.result).toMatchObject({ decision: "BLOCKED", errorCode: "RESIDUAL_PII" });
  });

  it("keeps the system prompt and typed text memoised while later turns add more than the memo holds", async () => {
    const seen: string[] = [];
    const guard = testGuard({ analyzer: recordingAnalyzer(seen) });
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    await guard.inspect(input(pseudo, { results: Array.from({ length: 300 }, (_, i) => `a ${i}`) }));
    await guard.inspect(input(pseudo, { results: Array.from({ length: 300 }, (_, i) => `b ${i}`) }));
    await guard.inspect(input(pseudo));
    expect(seen.filter((t) => t === "system")).toHaveLength(1);
    expect(seen.filter((t) => t === "hello")).toHaveLength(1);
  });

  it("memoises a large text too, because keys are digests, so it is analysed once per conversation", async () => {
    const seen: string[] = [];
    const guard = testGuard({ analyzer: recordingAnalyzer(seen) });
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const large = "x".repeat(64 * 1024 + 1);
    await guard.inspect(input(pseudo, { results: [large] }));
    await guard.inspect(input(pseudo, { results: [large] }));
    expect(seen.filter((t) => t === large)).toHaveLength(1);
    expect(seen.filter((t) => t === "hello")).toHaveLength(1);
  });

  it("keeps at most 512 memoised texts per conversation, dropping the oldest", async () => {
    const seen: string[] = [];
    const guard = testGuard({ analyzer: recordingAnalyzer(seen) });
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const texts = Array.from({ length: 520 }, (_, i) => `row ${i}`);
    await guard.inspect(input(pseudo, { results: texts }));
    await guard.inspect(input(pseudo, { results: ["row 0", "row 519"] }));
    expect(seen.filter((t) => t === "row 0")).toHaveLength(2);
    expect(seen.filter((t) => t === "row 519")).toHaveLength(1);
  });

  it("keeps a text's memoised categories when a concurrent turn evicts it while new text is still being scanned", async () => {
    let release = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    const analyzer: Analyzer = { scan: async (text) => (text === "slow" ? gate.then(() => scanPii(text)) : scanPii(text)) };
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard({ analyzer });
    const known = `x ${CANARIES.iban}`;
    await guard.inspect(input(pseudo, { results: [known] }));
    const pending = guard.inspect(input(pseudo, { results: [known, "slow"] }));
    await guard.inspect(input(pseudo, { results: Array.from({ length: 520 }, (_, i) => `row ${i}`) }));
    release();
    expect((await pending).result).toMatchObject({ decision: "REDACTED", counts: { IBAN: 1 } });
  });

  it("keeps the memo within 512 texts when a turn re-inserts entries a concurrent turn evicted", async () => {
    let release = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    const seen: string[] = [];
    const analyzer: Analyzer = {
      scan: async (text) => {
        seen.push(text);
        return text === "slow" ? gate.then(() => scanPii(text)) : scanPii(text);
      },
    };
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard({ analyzer });
    const rows = Array.from({ length: 520 }, (_, i) => `row ${i}`);
    await guard.inspect(input(pseudo, { results: ["known"] }));
    const pending = guard.inspect(input(pseudo, { results: ["known", "slow"] }));
    await guard.inspect(input(pseudo, { results: rows }));
    release();
    await pending;
    seen.length = 0;
    await guard.inspect(input(pseudo, { results: rows }));
    expect(seen.filter((t) => t.startsWith("row ") && !t.includes("\n"))).toHaveLength(20);
  });

  it("does not memoise a failed scan, so a retry scans again", async () => {
    let calls = 0;
    const analyzer: Analyzer = {
      scan: (text) => {
        calls += 1;
        if (calls === 1) throw new Error("down");
        return scanPii(text);
      },
    };
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard({ analyzer });
    const failed = await guard.inspect(input(pseudo, { results: [`x ${CANARIES.iban}`] }));
    const retry = await guard.inspect(input(pseudo, { results: [`x ${CANARIES.iban}`] }));
    expect(failed.result.errorCode).toBe("ANALYZER_ERROR");
    expect(retry.result).toMatchObject({ decision: "REDACTED", counts: { IBAN: 1 } });
  });

  it("still blocks on residual PII in new text after earlier turns were clean", async () => {
    const analyzer: Analyzer = { scan: (text) => (text.includes("leftover") ? [{ category: "IBAN", value: "absent" }] : []) };
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard({ analyzer });
    await guard.inspect(input(pseudo));
    const verdict = await guard.inspect(input(pseudo, { results: ["leftover"] }));
    expect(verdict.result).toMatchObject({ decision: "BLOCKED", errorCode: "RESIDUAL_PII" });
  });

  it("blocks with ANALYZER_ERROR and a safe message when the analyzer returns a non-array", async () => {
    const logs: Record<string, unknown>[] = [];
    const broken = { scan: () => undefined } as unknown as Analyzer;
    const verdict = await testGuard({ analyzer: broken, log: (_l, _m, extra) => logs.push(extra ?? {}) }).inspect(input(new Pseudonymizer("t", 1000, () => 0)));
    expect(verdict.result).toMatchObject({ decision: "BLOCKED", errorCode: "ANALYZER_ERROR" });
    expect(logs[0]).toMatchObject({ errorCode: "ANALYZER_ERROR", errorMessage: "analyzer returned a non-array" });
  });

  it("blocks with ANALYZER_ERROR and memoises nothing when only some of the requested texts get a result", async () => {
    let partial = true;
    const analyzer: Analyzer = { scan: (text) => (partial && text !== "hello" ? (undefined as never) : scanPii(text)) };
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard({ analyzer });
    const failed = await guard.inspect(input(pseudo, { results: [`x ${CANARIES.iban}`] }));
    partial = false;
    const retry = await guard.inspect(input(pseudo, { results: [`x ${CANARIES.iban}`] }));
    expect(failed.result).toMatchObject({ decision: "BLOCKED", errorCode: "ANALYZER_ERROR" });
    expect(retry.result).toMatchObject({ decision: "REDACTED", counts: { IBAN: 1 } });
  });

  it("blocks with ANALYZER_TIMEOUT when the analyzer misses the deadline", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const guard = testGuard({ analyzer: { scan: () => new Promise(() => undefined) }, deadlineMs: 50 });
    const pending = guard.inspect(input(new Pseudonymizer("t", 1000, () => 0)));
    await vi.advanceTimersByTimeAsync(50);
    const verdict = await pending;
    expect(verdict.result).toMatchObject({ decision: "BLOCKED", errorCode: "ANALYZER_TIMEOUT" });
    expect(verdict.user).toBe("");
  });

  const logsFor = async (thrown: () => never) => {
    const logs: Record<string, unknown>[] = [];
    await testGuard({ analyzer: { scan: thrown }, log: (_l, _m, extra) => logs.push(extra ?? {}) }).inspect(input(new Pseudonymizer("t", 1000, () => 0)));
    return logs;
  };

  it("logs a fixed message and the detail code, never the raw text, for a foreign error", async () => {
    const logs = await logsFor(() => {
      throw Object.assign(new Error(`down ${CANARIES.iban} 1234567 ${"x".repeat(300)}`), { code: "ECONNREFUSED" });
    });
    expect(logs[0]).toMatchObject({ errorCode: "ANALYZER_ERROR", error: "Error", errorDetailCode: "ECONNREFUSED", errorMessage: "analyzer connection refused" });
    expect(JSON.stringify(logs)).not.toContain(CANARIES.iban);
  });

  it("logs a message for a codeless error that names a system condition", async () => {
    const logs = await logsFor(() => {
      throw new Error("connect ETIMEDOUT host:3000");
    });
    expect(logs[0]).toMatchObject({ errorCode: "ANALYZER_ERROR", error: "Error", errorMessage: "analyzer connection timed out" });
    expect(JSON.stringify(logs)).not.toContain("host:3000");
  });

  it.each([
    { name: "a hostile message with a system token", message: `getaddrinfo ENOTFOUND ${CANARIES.ssn}.corp.example:5432`, expected: "analyzer host could not be resolved" },
    { name: "a hostile message with no known token", message: `bad row ${CANARIES.ssn} at db-7.corp.example`, expected: "analyzer error" },
  ])("never logs $name", async ({ message, expected }) => {
    const logs = await logsFor(() => {
      throw new Error(message);
    });
    expect(logs[0]).toMatchObject({ errorMessage: expected });
    const logged = JSON.stringify(logs);
    expect(logged).not.toContain(CANARIES.ssn);
    expect(logged).not.toContain("corp.example");
  });

  it.each([
    { name: "a code that is an Object.prototype key", make: () => Object.assign(new Error("boom"), { code: "toString" }) },
    { name: "a name that is an Object.prototype key", make: () => Object.assign(new Error("boom"), { name: "constructor" }) },
    { name: "a non-string message", make: () => Object.assign(new Error(), { message: 42 }) },
  ])("logs a string message and still blocks for $name", async ({ make }) => {
    const logs = await logsFor(() => {
      throw make();
    });
    expect(logs[0]).toMatchObject({ errorCode: "ANALYZER_ERROR" });
    expect(logs[0]?.errorMessage).toBe("analyzer error");
  });

  it("names only the class of a programming error", async () => {
    const logs = await logsFor(() => {
      throw new TypeError(`cannot read ${CANARIES.ssn}`);
    });
    expect(logs[0]).toMatchObject({ error: "TypeError", errorMessage: "analyzer error: TypeError" });
    expect(JSON.stringify(logs)).not.toContain(CANARIES.ssn);
  });

  it("logs the guard's own code once and a message only when it adds something", async () => {
    const logs: Record<string, unknown>[] = [];
    const guard = testGuard({ analyzer: { scan: () => [{ category: "IBAN", value: "absent" }] }, log: (_l, _m, extra) => logs.push(extra ?? {}) });
    await guard.inspect(input(new Pseudonymizer("t", 1000, () => 0)));
    expect(logs[0]).toMatchObject({ errorCode: "RESIDUAL_PII", error: "GuardFailure" });
    expect(logs[0]).not.toHaveProperty("errorDetailCode");
    expect(logs[0]).not.toHaveProperty("errorMessage");
  });

  it("omits the detail code when it is not a code and still logs a fixed message for admit", async () => {
    const logs: Record<string, unknown>[] = [];
    const failing: Analyzer = {
      scan: () => {
        throw Object.assign(new Error(`down ${CANARIES.iban}`), { code: "not a code" });
      },
    };
    const admission = await testGuard({ analyzer: failing, log: (_l, _m, extra) => logs.push(extra ?? {}) }).admit({ tenantId: "t", tool: GET, text: "x" });
    expect(admission).toMatchObject({ pass: false, failure: "ANALYZER_ERROR" });
    expect(logs[0]).not.toHaveProperty("errorDetailCode");
    expect(logs[0]).toMatchObject({ errorMessage: "analyzer error" });
    expect(JSON.stringify(logs)).not.toContain(CANARIES.iban);
  });

  it("logs only the name for a non-Error throw", async () => {
    const logs: Record<string, unknown>[] = [];
    const failing: Analyzer = {
      scan: () => {
        throw "boom " + CANARIES.iban;
      },
    };
    await testGuard({ analyzer: failing, log: (_l, _m, extra) => logs.push(extra ?? {}) }).inspect(input(new Pseudonymizer("t", 1000, () => 0)));
    expect(logs[0]).toMatchObject({ error: "unknown" });
    expect(logs[0]).not.toHaveProperty("errorMessage");
  });

  it("returns an empty context when the analyzer fails", async () => {
    const failing: Analyzer = { scan: () => { throw new Error("down"); } };
    const verdict = await testGuard({ analyzer: failing }).inspect(input(new Pseudonymizer("t", 1000, () => 0), { recordIds: new Set(["R-1"]) }));
    expect(verdict.result.decision).toBe("BLOCKED");
    expect(verdict.context.inputTokens.size).toBe(0);
    expect(verdict.context.inputRecordIds.size).toBe(0);
  });
});

describe("run provider wrapper", () => {
  const response: ModelResponse = { text: "ok" };
  const make = (calls: string[]) => withEgressGuard({ complete: async (req) => (calls.push(req.user), response) }, testGuard());

  it("forwards a prompt with no sensitive data", async () => {
    const calls: string[] = [];
    await make(calls).complete({ system: "s", user: "hello [IBAN-1]", allowedTools: [], maxOutputTokens: 10 });
    expect(calls).toEqual(["hello [IBAN-1]"]);
  });

  it("refuses with AGT-0219 and makes no call when a raw IBAN reaches the provider layer", async () => {
    const calls: string[] = [];
    const err = await make(calls)
      .complete({ system: "s", user: "pay DE89 3704 0044 0532 0130 00", allowedTools: [], maxOutputTokens: 10 })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(AgentError);
    expect((err as AgentError).code).toBe("AGT-0219");
    expect(calls).toEqual([]);
  });
});

const wireAnswer = (adapter: string, input: Record<string, unknown>): unknown => {
  if (adapter === "anthropic") return { content: [{ type: "tool_use", name: PROPOSE_TOOL_NAME, input }], usage: { input_tokens: 10, output_tokens: 5 } };
  if (adapter === "bedrock") return { output: { message: { content: [{ toolUse: { name: PROPOSE_TOOL_NAME, input } }] } }, usage: { inputTokens: 10, outputTokens: 5 } };
  return { choices: [{ message: { tool_calls: [{ function: { name: PROPOSE_TOOL_NAME, arguments: JSON.stringify(input) } }] } }], usage: { prompt_tokens: 10, completion_tokens: 5 } };
};

describe("adapter parity", () => {
  const base = { MCP_URL: "http://mcp:8080", AGENTS_JWT_ISSUER: "https://idp.dev.depot.dev/realms/dev", AGENTS_JWKS_URL: "https://idp.dev.depot.dev/realms/dev/protocol/openid-connect/certs" };
  const adapters: Record<string, Record<string, string>> = {
    anthropic: { AGENTS_PROVIDER: "anthropic", ANTHROPIC_API_KEY: "k" },
    bedrock: { AGENTS_PROVIDER: "bedrock", AWS_REGION: "us-east-1", AWS_ACCESS_KEY_ID: "id", AWS_SECRET_ACCESS_KEY: "secret" },
    azure: { AGENTS_PROVIDER: "openai", OPENAI_API_KEY: "k", AGENTS_OPENAI_BASE_URL: "https://acme.openai.azure.com/openai/deployments/d", AGENTS_OPENAI_AUTH: "api-key" },
    vllm: { AGENTS_PROVIDER: "openai", OPENAI_API_KEY: "k", AGENTS_OPENAI_BASE_URL: "https://vllm.internal/v1" },
  };
  const typed = `Pay ${CANARIES.iban} with card ${CANARIES.typedCard}, EIN ${CANARIES.ein}, ssn ${CANARIES.ssn}`;

  it("sends the same redacted prompt through all four adapters and no canary", async () => {
    const verdict = await testGuard().inspect({ tenantId: "t", callId: "c", routeId: "r", pseudo: new Pseudonymizer("t", 60_000, () => 0), system: "s", typed, results: [] });
    expect(verdict.result.decision).toBe("REDACTED");
    const escaped = JSON.stringify(verdict.user).slice(1, -1);
    for (const [name, env] of Object.entries(adapters)) {
      const bodies: string[] = [];
      const fetchFn = (async (_url: string | URL, init?: RequestInit): Promise<Response> => {
        bodies.push(String(init?.body ?? ""));
        return Response.json(wireAnswer(name, { text: "ok" }));
      }) as unknown as typeof fetch;
      const provider = createProviderFactory(loadConfig({ ...base, ...env }), fetchFn)("m");
      await provider.complete({ system: "s", user: verdict.user, allowedTools: [GET], maxOutputTokens: 10 });
      expect(bodies, name).toHaveLength(1);
      expect(bodies[0], name).toContain(escaped);
      for (const value of [CANARIES.iban, CANARIES.typedCard, CANARIES.ein, CANARIES.ssn]) expect(bodies[0], name).not.toContain(value);
    }
  });
});
