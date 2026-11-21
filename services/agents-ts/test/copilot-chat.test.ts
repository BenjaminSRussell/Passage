import { afterEach, describe, expect, it } from "vitest";
import { tokenFrames } from "../src/api/copilot.js";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { SENSITIVE_DUTY } from "../src/domain/redaction.js";
import { createCopilotService, TYPED_REPLACED_NOTICE } from "../src/runtime/copilot.js";
import { EGRESS_GUARD_TOOL, GUARD_REFUSAL_MESSAGE, type EgressGuard } from "../src/runtime/egressGuard.js";
import { createOutputGuard, OUTPUT_GUARD_MESSAGE, OUTPUT_GUARD_TOOL, type OutputGuard } from "../src/runtime/outputGuard.js";
import type { ModelResponse } from "../src/runtime/engine.js";
import { openRouter, REAL_IBAN, ScriptedProvider, testGuard, allowAiAccess } from "./fakes.js";
import { tokenFor } from "./jwt.js";
import { FakeMcp, headers, MemoryStore, serve, T1, tool, USER } from "./surface-fakes.js";

const GET = "depot.ap.invoice.get";
const LIST = "depot.ap.vendor.list";
const SEARCH = "depot.knowledge.search";
const DUTIES = "mcp.copilot.use,ap.invoice.read,ap.vendor.read";
const answer = (text: string, usage = { tokensIn: 100, tokensOut: 20 }): ModelResponse => ({ text, usage });
const call = (toolName: string, args: Record<string, unknown> = {}): ModelResponse => ({
  text: "",
  proposal: { tool: toolName, args, confidence: 1, amount: { amount: "0", currency: "USD" } },
  usage: { tokensIn: 200, tokensOut: 10 },
});

const setup = async (script: ModelResponse[], mcp = new FakeMcp([tool(GET, "ap.invoice.read"), tool(LIST, "ap.vendor.read")], { [GET]: { ok: true, output: { id: "INV-7", total: "1200.00" }, ref: "INV-7" } }), killed = false, guard?: EgressGuard, outputGuard?: OutputGuard) => {
  const store = new MemoryStore();
  const provider = new ScriptedProvider(script);
  const cache = new CopilotAnswerCache();
  const copilot = createCopilotService({
    aiAccess: allowAiAccess(),
    router: openRouter(provider),
    mcp,
    store,
    killSwitch: { isHalted: () => killed },
    cache,
    config: { tokenCap: 20_000, maxTurns: 3, pseudonymTtlMs: 60_000 },
    modelId: "claude-haiku-5-5",
    guard: guard ?? testGuard(),
    ...(outputGuard !== undefined ? { outputGuard } : {}),
  });
  const srv = await serve({ approvals: store, copilot });
  return { store, provider, mcp, srv };
};

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

const chat = (base: string, body: unknown, duties = DUTIES, accept = "application/json") =>
  fetch(`${base}/api/v1/copilot/chat`, { method: "POST", headers: { ...headers(duties), accept }, body: JSON.stringify(body) });

describe("POST /api/v1/copilot/chat", () => {
  it("calls a read tool as the user, feeds the result back, cites the record, audits one run and settles the tokens", async () => {
    const s = await setup([call(GET, { invoiceId: "INV-7" }), answer("Invoice INV-7 totals 1200.00.")]);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "explain", message: "Explain INV-7", recordRef: "ap/invoice/INV-7" });
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({
      answer: "Invoice INV-7 totals 1200.00.",
      citations: [{ sourceType: "RECORD", sourceRef: "INV-7", label: GET }],
      toolCalls: [{ tool: GET, status: "OK" }],
      refusal: null,
      dataClass: "P2",
      generated: true,
    });
    expect(s.mcp.connects[0]).toMatchObject({ userId: USER, token: tokenFor(T1, USER, DUTIES) });
    expect(s.mcp.calls).toEqual([{ name: GET, args: { invoiceId: "INV-7" } }]);
    // The second prompt carries the tool result; only read tools were offered.
    expect(s.provider.seen[1]?.user).toContain("Result of depot.ap.invoice.get");
    expect(s.provider.seen[0]?.allowedTools).toEqual([GET, LIST]);
    const [run] = [...s.store.runs.values()];
    expect(run).toMatchObject({ agentId: "copilot", status: "COMMITTED", tokensIn: 300, tokensOut: 30 });
    const rows = s.store.actionRows.get(run?.runId ?? "") ?? [];
    expect(rows.filter((r) => r.toolName !== EGRESS_GUARD_TOOL)).toEqual([expect.objectContaining({ toolName: GET, decision: "AUTO", reason: "read" })]);
    expect(rows.filter((r) => r.toolName === EGRESS_GUARD_TOOL)).toHaveLength(2);
    expect(s.store.reserved).toEqual([expect.objectContaining({ agentId: "copilot", tokensIn: 20_000 })]);
    expect(s.store.settled).toEqual([[300, 30]]);
  });

  it("tells the explain prompt to read the three history tools, name missing parts, count what the user cannot open and never act", async () => {
    const s = await setup([answer("No change history.")]);
    close = s.srv.close;
    await chat(s.srv.base, { mode: "explain", message: "What changed", recordRef: "record:purchase-order/PO-1" });
    const system = s.provider.seen[0]?.system ?? "";
    for (const tool of ["depot.platform.audit_trail.get", "depot.platform.document_flow.get", "depot.workflow.history.get"]) expect(system).toContain(tool);
    expect(system).toContain("Say which part you could not read");
    expect(system).toContain("never name them");
    expect(system).toContain("no change history");
    expect(system).toContain("never as something you did");
  });

  it("reads the audit trail for the asked record, cites it with a typed ref and reports the other two history tools as not read", async () => {
    const AUDIT = "depot.platform.audit_trail.get";
    const mcp = new FakeMcp([tool(AUDIT, "audit.read")], { [AUDIT]: { ok: true, output: { items: [{ id: "chg-1", field: "status" }] }, ref: "chg-1" } });
    const s = await setup([call(AUDIT, { entity: "purchase-order", recordId: "PO-1" }), answer("Status changed once.")], mcp);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "explain", message: "What changed", recordRef: "record:purchase-order/PO-1" }, `${DUTIES},audit.read`);
    const body = (await res.json()) as { citations: unknown[]; toolCalls: unknown[] };
    expect(body.toolCalls).toEqual([{ tool: AUDIT, status: "OK" }]);
    expect(body.citations).toContainEqual({ sourceType: "RECORD", sourceRef: "record:platform/purchase-order/PO-1", label: "purchase order PO-1" });
    expect(body.toolCalls).not.toContainEqual(expect.objectContaining({ tool: "depot.platform.document_flow.get" }));
  });

  it.each([
    ["an unrelated tool", GET, { invoiceId: "PO-1" }, { id: "INV-7" }],
    ["a different record", "depot.platform.audit_trail.get", { entity: "purchase-order", recordId: "PO-2" }, { items: [{ id: "chg-1" }] }],
    ["an empty audit trail", "depot.platform.audit_trail.get", { entity: "purchase-order", recordId: "PO-1" }, { items: [] }],
  ])("adds no typed record citation for %s", async (_name, toolName, args, output) => {
    const mcp = new FakeMcp([tool(toolName, "audit.read")], { [toolName]: { ok: true, output, ref: "x-1" } });
    const s = await setup([call(toolName, args), answer("Done.")], mcp);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "explain", message: "What changed", recordRef: "record:purchase-order/PO-1" }, `${DUTIES},audit.read`);
    const body = (await res.json()) as { citations: { sourceRef: string }[] };
    expect(body.citations.filter((c) => c.sourceRef.startsWith("record:"))).toEqual([]);
  });

  it("sends the provider and the audit row only redacted text and masks a value the user holds no duty for", async () => {
    const s = await setup([call(GET, { invoiceId: "INV-7", iban: REAL_IBAN }), answer("Pay to [IBAN-1].")]);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: `What is open for IBAN ${REAL_IBAN}?` });
    const body = (await res.json()) as { answer: string };
    expect(body.answer).toBe(`Pay to ****3000.\n\n${TYPED_REPLACED_NOTICE}`);
    for (const seen of s.provider.seen) expect(seen.user).not.toContain(REAL_IBAN);
    expect(JSON.stringify([...s.store.actionRows.values()])).not.toContain(REAL_IBAN);
    // The tool itself is called with the real value (the user's own request).
    expect(s.mcp.calls[0]?.args).toEqual({ invoiceId: "INV-7", iban: REAL_IBAN });
  });

  it("refuses a tool outside the caller's allowed set with AGT-0203 and never calls it", async () => {
    const s = await setup([call("depot.ap.invoice.post", { invoiceId: "INV-7" })]);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: "post it" });
    const body = (await res.json()) as { refusal: { code: string; message: string }; toolCalls: unknown[] };
    expect(body.refusal.code).toBe("AGT-0203");
    expect(body.toolCalls).toEqual([{ tool: "depot.ap.invoice.post", status: "DENIED", code: "AGT-0203" }]);
    expect(s.mcp.calls).toHaveLength(0);
    const [run] = [...s.store.runs.values()];
    expect(s.store.actionRows.get(run?.runId ?? "")?.find((r) => r.toolName === "depot.ap.invoice.post")).toMatchObject({ decision: "DENIED" });
  });

  it("offers a user only the tools whose duty they hold and refuses before any provider call when none remain", async () => {
    const s = await setup([answer("unused")], new FakeMcp([tool(GET, "ap.invoice.read")]));
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: "hi" }, "mcp.copilot.use");
    const body = (await res.json()) as { refusal: { code: string; message: string } };
    expect(body.refusal.code).toBe("AGT-0203");
    expect(body.refusal.message).toBe("I cannot do that: you do not hold duty ap.invoice.read required by depot.ap.invoice.get.");
    expect(s.provider.seen).toHaveLength(0);
    expect(s.store.runs.size).toBe(0);
  });

  it("answers a repeated question from the cache for the same user only", async () => {
    const s = await setup([answer("Once."), answer("Twice.")]);
    close = s.srv.close;
    const ask = () => chat(s.srv.base, { mode: "ask", message: "same question" }).then((r) => r.json() as Promise<{ answer: string }>);
    expect((await ask()).answer).toBe("Once.");
    expect((await ask()).answer).toBe("Once.");
    expect(s.provider.seen).toHaveLength(1);
    const other = await fetch(`${s.srv.base}/api/v1/copilot/chat`, {
      method: "POST",
      headers: headers(DUTIES, "00000000-0000-7000-8000-0000000000c9"),
      body: JSON.stringify({ mode: "ask", message: "same question" }),
    });
    expect(((await other.json()) as { answer: string }).answer).toBe("Twice.");
  });

  it("applies the citation gate to knowledge answers (AGT-0212) and cites grounded chunks", async () => {
    const chunk = (similarity: number) => ({ chunkId: "c1", sourceType: "POLICY", sourceRef: "policy/travel", section: "Meals", fusedScore: 0.5, similarity });
    const mcp = (similarity: number) => new FakeMcp([tool(SEARCH, "knowledge.read")], { [SEARCH]: { ok: true, output: { chunks: [chunk(similarity)] } } });
    const weak = await setup([call(SEARCH, { q: "meals" }), answer("Meals are covered.")], mcp(0.4));
    const refused = (await (await chat(weak.srv.base, { mode: "ask", message: "meal policy?" }, "mcp.copilot.use,knowledge.read")).json()) as { refusal: { code: string } | null; citations: unknown[] };
    await weak.srv.close();
    expect(refused.refusal?.code).toBe("AGT-0212");
    expect(refused.citations).toEqual([]);

    const strong = await setup([call(SEARCH, { q: "meals" }), answer("Meals are covered.")], mcp(0.9));
    close = strong.srv.close;
    const ok = (await (await chat(strong.srv.base, { mode: "ask", message: "meal policy?" }, "mcp.copilot.use,knowledge.read")).json()) as { refusal: unknown; citations: unknown[] };
    expect(ok.refusal).toBeNull();
    expect(ok.citations).toEqual([{ sourceType: "POLICY", sourceRef: "policy/travel", label: "Meals" }]);
  });

  it("streams token frames then one done frame for an event-stream client, and the same body as JSON otherwise", async () => {
    const text = "Invoice INV-7 totals 1200.00 and is due on 30 November, so it is not yet late.";
    const s = await setup([answer(text)]);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: "stream" }, DUTIES, "text/event-stream, application/json;q=0.9");
    expect(res.headers.get("content-type")).toContain("text/event-stream");
    const raw = await res.text();
    const frames = raw.split("\n\n").filter((f) => f !== "");
    expect(frames.at(-1)).toMatch(/^event: done\ndata: /);
    const tokens = frames.filter((f) => f.startsWith("event: token")).map((f) => (JSON.parse(f.split("data: ")[1] ?? "{}") as { text: string }).text);
    expect(tokens.length).toBeGreaterThan(1);
    expect(tokens.join("")).toBe(text);
    const done = JSON.parse((frames.at(-1) ?? "").split("data: ")[1] ?? "{}") as { answer: string; generated: boolean };
    expect(done).toMatchObject({ answer: text, generated: true });
  });

  it("rejects a bad body, a missing duty, and a kill switch before any provider call", async () => {
    const s = await setup([answer("x")]);
    close = s.srv.close;
    expect((await chat(s.srv.base, { mode: "nope", message: "x" })).status).toBe(400);
    expect((await chat(s.srv.base, { mode: "ask", message: "" })).status).toBe(400);
    expect((await chat(s.srv.base, { mode: "ask", message: "x", extra: 1 })).status).toBe(400);
    const noDuty = await chat(s.srv.base, { mode: "ask", message: "x" }, "ap.invoice.read");
    expect(noDuty.status).toBe(403);
    expect(s.provider.seen).toHaveLength(0);
    const killed = await setup([answer("x")], undefined, true);
    expect(((await (await chat(killed.srv.base, { mode: "ask", message: "x" })).json()) as { code: string }).code).toBe("AGT-0201");
    await killed.srv.close();
  });

  it("marks the run FAILED when the provider fails and answers a 500 that shows nothing", async () => {
    const s = await setup([]);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: "x" });
    expect(res.status).toBe(500);
    const [run] = [...s.store.runs.values()];
    expect(run?.status).toBe("FAILED");
  });
});

describe("egress guard on the chat", () => {
  it("answers the standard refusal with no provider call and one BLOCKED guard row when the analyzer fails", async () => {
    const guard = testGuard({ analyzer: { scan: () => { throw new Error("analyzer down"); } } });
    const s = await setup([answer("never sent")], undefined, false, guard);
    close = s.srv.close;
    const res = await chat(s.srv.base, { mode: "ask", message: "anything" });
    expect(res.status).toBe(200);
    expect(await res.json()).toMatchObject({ answer: GUARD_REFUSAL_MESSAGE, refusal: { code: "AGT-0219", message: GUARD_REFUSAL_MESSAGE } });
    expect(s.provider.seen).toHaveLength(0);
    const [run] = [...s.store.runs.values()];
    const rows = s.store.actionRows.get(run?.runId ?? "") ?? [];
    expect(rows).toEqual([expect.objectContaining({ toolName: EGRESS_GUARD_TOOL, decision: "DENIED", guardResult: expect.objectContaining({ decision: "BLOCKED", errorCode: "ANALYZER_ERROR" }) })]);
  });

  it("tells the user sensitive numbers were replaced and keeps them out of the provider body", async () => {
    const s = await setup([answer("Noted.")]);
    close = s.srv.close;
    const card = "4111 1111 1111 1111";
    const res = await chat(s.srv.base, { mode: "ask", message: `Is card ${card} on file?` });
    const body = (await res.json()) as { answer: string; dataClass: string };
    expect(body.answer).toContain(TYPED_REPLACED_NOTICE);
    expect(body.dataClass).toBe("P3");
    expect(s.provider.seen[0]?.user).not.toContain("4111");
    expect(s.provider.seen[0]?.user).toContain("[CARD-1]");
  });
});

describe("output guard on the chat", () => {
  const PAYROLL = `${DUTIES},${SENSITIVE_DUTY.payroll}`;
  const RETIRED_DUTY = { payroll: "ViewSensitivePayroll", worker: "ViewSensitiveWorker" } as const;
  const ask = { mode: "ask", message: "Is salary: 85000 correct for the vendor?" };
  const guardRows = (s: Awaited<ReturnType<typeof setup>>) => [...s.store.actionRows.values()].flat().filter((r) => r.toolName === OUTPUT_GUARD_TOOL);

  it("shows a payroll value to the holder of the payroll duty and its masked form to everyone else", async () => {
    const holder = await setup([answer("The pay is [PAY-1].")]);
    const holderBody = (await (await chat(holder.srv.base, ask, PAYROLL)).json()) as { answer: string; refusal: unknown };
    await holder.srv.close();
    expect(holderBody.answer).toContain("The pay is 85000.");
    expect(holderBody.refusal).toBeNull();

    const other = await setup([answer("The pay is [PAY-1].")]);
    close = other.srv.close;
    const otherBody = (await (await chat(other.srv.base, ask)).json()) as { answer: string };
    expect(otherBody.answer).toContain("The pay is ****.");
    expect(JSON.stringify([otherBody, ...other.store.actionRows.values()])).not.toContain("85000");
  });

  it("masks the payroll value for a token that carries only the retired PascalCase duty name", async () => {
    const s = await setup([answer("The pay is [PAY-1].")]);
    close = s.srv.close;
    const body = (await (await chat(s.srv.base, ask, `${DUTIES},${RETIRED_DUTY.payroll}`)).json()) as { answer: string };
    expect(body.answer).toContain("The pay is ****.");
  });

  it("shows SSN and tax-id values to the holder of the worker duty and masks them for a token with only the retired PascalCase name", async () => {
    const WORKER = `${DUTIES},${SENSITIVE_DUTY.worker}`;
    const sensitive = { mode: "ask", message: "Is ssn 123-45-6789 and tax id 12-3456789 on file?" };
    const holder = await setup([answer("SSN [SSN-1], tax id [TAXID-1].")]);
    const holderBody = (await (await chat(holder.srv.base, sensitive, WORKER)).json()) as { answer: string };
    await holder.srv.close();
    expect(holderBody.answer).toContain("123-45-6789");
    expect(holderBody.answer).toContain("12-3456789");

    const retired = await setup([answer("SSN [SSN-1], tax id [TAXID-1].")]);
    close = retired.srv.close;
    const retiredBody = (await (await chat(retired.srv.base, sensitive, `${DUTIES},${RETIRED_DUTY.worker}`)).json()) as { answer: string; refusal: unknown };
    expect(retiredBody.refusal).toBeNull();
    expect(retiredBody.answer).toContain("SSN ****6789, tax id ****6789");
    expect(retiredBody.answer).not.toContain("123-45-6789");
    expect(retiredBody.answer).not.toContain("12-3456789");
  });

  it("keeps SSN and tax-id masked for a token holding only the payroll duty, and PAY masked for a token holding only the worker duty", async () => {
    const sensitive = { mode: "ask", message: "Is ssn 123-45-6789 and tax id 12-3456789 on file?" };
    const payrollOnly = await setup([answer("SSN [SSN-1], tax id [TAXID-1].")]);
    const payrollBody = (await (await chat(payrollOnly.srv.base, sensitive, PAYROLL)).json()) as { answer: string };
    await payrollOnly.srv.close();
    expect(payrollBody.answer).toContain("SSN ****6789, tax id ****6789");
    expect(payrollBody.answer).not.toContain("123-45-6789");
    expect(payrollBody.answer).not.toContain("12-3456789");

    const workerOnly = await setup([answer("The pay is [PAY-1].")]);
    close = workerOnly.srv.close;
    const workerBody = (await (await chat(workerOnly.srv.base, ask, `${DUTIES},${SENSITIVE_DUTY.worker}`)).json()) as { answer: string };
    expect(workerBody.answer).toContain("The pay is ****.");
    expect(workerBody.answer).not.toContain("85000");
  });

  it("withholds an answer that prints the raw payroll value for a viewer without the duty, stores only the guard row and caches nothing", async () => {
    const s = await setup([answer("Their salary: 85000."), answer("The pay is [PAY-1].")]);
    close = s.srv.close;
    const body = (await (await chat(s.srv.base, ask)).json()) as { answer: string; refusal: { code: string }; citations: unknown[] };
    expect(body).toMatchObject({ answer: OUTPUT_GUARD_MESSAGE, refusal: { code: "AGT-0220" }, citations: [] });
    expect(guardRows(s)).toEqual([expect.objectContaining({ decision: "DENIED", guardResult: expect.objectContaining({ decision: "OUTPUT_BLOCKED", errorCode: "DUTY_LEAK" }) })]);
    expect(JSON.stringify([...s.store.actionRows.values()])).not.toContain("85000");
    const again = (await (await chat(s.srv.base, ask)).json()) as { refusal: unknown };
    expect(again.refusal).toBeNull();
  });

  it("withholds a token that was not part of this turn's inputs and a record id that was not read this turn", async () => {
    const token = await setup([answer("Write to [EMAIL-9].")]);
    const tokenBody = (await (await chat(token.srv.base, { mode: "ask", message: "who?" })).json()) as { answer: string; refusal: { code: string } };
    await token.srv.close();
    expect(tokenBody).toMatchObject({ answer: OUTPUT_GUARD_MESSAGE, refusal: { code: "AGT-0220" } });
    expect(guardRows(token)[0]?.guardResult).toMatchObject({ errorCode: "TOKEN_NOT_IN_INPUT" });

    const foreign = await setup([call(GET, { invoiceId: "INV-7" }), answer("See [[cite:INV-99]].")]);
    const foreignBody = (await (await chat(foreign.srv.base, { mode: "ask", message: "invoice?" })).json()) as { refusal: { code: string }; citations: unknown[] };
    await foreign.srv.close();
    expect(foreignBody).toMatchObject({ refusal: { code: "AGT-0220" }, citations: [] });
    expect(guardRows(foreign)[0]?.guardResult).toMatchObject({ errorCode: "RECORD_NOT_IN_INPUT" });

    const own = await setup([call(GET, { invoiceId: "INV-7" }), answer("See [[cite:INV-7]].")]);
    close = own.srv.close;
    expect(((await (await chat(own.srv.base, { mode: "ask", message: "invoice?" })).json()) as { refusal: unknown }).refusal).toBeNull();
  });

  it("marks one unreadable citation among many as unverified and never shows its id", async () => {
    const text = `${"See [[cite:INV-7]]. ".repeat(5)}Also [[cite:SECRET-9]].`;
    const mixed = await setup([call(GET, { invoiceId: "INV-7" }), answer(text)]);
    close = mixed.srv.close;
    const body = (await (await chat(mixed.srv.base, { mode: "ask", message: "invoice?" })).json()) as { answer: string; refusal: unknown };
    expect(body.refusal).toBeNull();
    expect(body.answer).toContain("[unverified]");
    expect(body.answer).not.toContain("SECRET-9");
  });

  it("fails closed with the safe message when the guard errors, and leaves the run committed", async () => {
    const outputGuard = createOutputGuard({ analyzer: { scan: () => { throw new Error("analyzer down"); } } });
    const s = await setup([answer("Fine.")], undefined, false, undefined, outputGuard);
    close = s.srv.close;
    const body = (await (await chat(s.srv.base, { mode: "ask", message: "x" })).json()) as { refusal: { code: string } };
    expect(body.refusal.code).toBe("AGT-0220");
    expect(guardRows(s)[0]?.guardResult).toMatchObject({ errorCode: "GUARD_ERROR" });
    expect([...s.store.runs.values()][0]?.status).toBe("COMMITTED");
    expect(s.store.settled).toHaveLength(1);
  });

  it("sends answer.withdrawn and no token frame when a non-streamed answer fails the guard", async () => {
    const s = await setup([answer("Write to [EMAIL-9].")]);
    close = s.srv.close;
    const raw = await (await chat(s.srv.base, { mode: "ask", message: "who?" }, DUTIES, "text/event-stream")).text();
    const names = raw.split("\n\n").filter((f) => f !== "").map((f) => f.split("\n")[0]);
    expect(names).toEqual(["event: answer.withdrawn", "event: done"]);
    expect(raw).toContain(`"code":"AGT-0220"`);
    expect(raw).not.toContain("EMAIL-9");
  });

  it("raises the declared dataClass to the highest class of the inputs", async () => {
    const s = await setup([answer("Noted.")]);
    close = s.srv.close;
    const body = (await (await chat(s.srv.base, { mode: "ask", message: "Is card 4111 1111 1111 1111 on file?" })).json()) as { dataClass: string };
    expect(body.dataClass).toBe("P3");
  });
});

describe("tokenFrames", () => {
  it("cuts on word boundaries and loses nothing", () => {
    const text = "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron";
    const frames = tokenFrames(text, 20);
    expect(frames.join("")).toBe(text);
    expect(frames.every((f) => f.length <= 20)).toBe(true);
    expect(tokenFrames("")).toEqual([]);
  });
});
