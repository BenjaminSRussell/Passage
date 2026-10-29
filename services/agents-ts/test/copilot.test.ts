import { describe, expect, it } from "vitest";
import { CopilotAnswerCache, COPILOT_MODES, dutiesKey, gateCopilotTools } from "../src/domain/copilot.js";

const TOOLS = [
  { name: "passage.ap.invoice.list", requiredDuty: "APInvoiceView" },
  { name: "passage.ledger.journal.get", requiredDuty: "LedgerView" },
  { name: "passage.ap.invoice.post", requiredDuty: "APInvoicePost" },
];

describe("gateCopilotTools (BR-47.11, AGT-0203)", () => {
  it("allows only tools whose duty the user holds", () => {
    const gate = gateCopilotTools(new Set(["APInvoiceView"]), TOOLS);
    expect(gate.allowed).toEqual(["passage.ap.invoice.list"]);
    expect(gate.denied.map((d) => d.tool)).toEqual(["passage.ledger.journal.get", "passage.ap.invoice.post"]);
  });

  it("answers each denial with AGT-0203 and the catalog text", () => {
    const [first] = gateCopilotTools(new Set(), TOOLS).denied;
    expect(first).toEqual({
      tool: "passage.ap.invoice.list",
      duty: "APInvoiceView",
      code: "AGT-0203",
      message: "I cannot do that: you do not hold duty APInvoiceView required by passage.ap.invoice.list.",
    });
  });

  it("allows everything for a user holding every duty and nothing for none", () => {
    const all = new Set(TOOLS.map((t) => t.requiredDuty));
    expect(gateCopilotTools(all, TOOLS).denied).toHaveLength(0);
    expect(gateCopilotTools(new Set(), TOOLS).allowed).toHaveLength(0);
  });

  it("exposes the four PRD modes", () => {
    expect([...COPILOT_MODES]).toEqual(["ask", "explain", "draft", "summarize"]);
  });
});

describe("CopilotAnswerCache (BR-47.11)", () => {
  const T1 = "0190f3a2-7c1d-7b2e-8f00-000000000001";
  const T2 = "0190f3a2-7c1d-7b2e-8f00-000000000002";
  const aliceDuties = new Set(["APInvoiceView", "LedgerView"]);
  const alice = { tenantId: T1, userId: "alice", duties: aliceDuties };
  const bob = { tenantId: T1, userId: "bob", duties: new Set(["APInvoiceView"]) };

  it("serves a cached answer to the same asker, mode and prompt", () => {
    const cache = new CopilotAnswerCache();
    cache.put(alice, "ask", "show open invoices", "12 open invoices");
    expect(cache.get(alice, "ask", "show open invoices")).toBe("12 open invoices");
    expect(cache.get({ ...alice, duties: new Set(["LedgerView", "APInvoiceView"]) }, "ask", "show open invoices")).toBe("12 open invoices");
  });

  it("never serves another user's, another tenant's or a narrower duty set's answer", () => {
    const cache = new CopilotAnswerCache();
    cache.put(alice, "ask", "show journals", "journal data");
    expect(cache.get({ ...alice, userId: "bob" }, "ask", "show journals")).toBeUndefined();
    expect(cache.get({ ...alice, tenantId: T2 }, "ask", "show journals")).toBeUndefined();
    expect(cache.get({ ...alice, duties: new Set(["APInvoiceView"]) }, "ask", "show journals")).toBeUndefined();
    expect(cache.get(alice, "summarize", "show journals")).toBeUndefined();
  });

  it("keeps both users' answers to the same prompt instead of overwriting one", () => {
    const cache = new CopilotAnswerCache();
    cache.put(alice, "ask", "show open invoices", "alice's 12");
    cache.put(bob, "ask", "show open invoices", "bob's 3");
    expect(cache.get(alice, "ask", "show open invoices")).toBe("alice's 12");
    expect(cache.get(bob, "ask", "show open invoices")).toBe("bob's 3");
  });

  it("expires answers after the TTL", () => {
    let now = 0;
    const cache = new CopilotAnswerCache(() => now, 1_000);
    cache.put(alice, "ask", "q", "a");
    now = 999;
    expect(cache.get(alice, "ask", "q")).toBe("a");
    now = 1_000;
    expect(cache.get(alice, "ask", "q")).toBeUndefined();
    expect(cache.size).toBe(0);
  });

  it("evicts the oldest entry at the size bound", () => {
    const cache = new CopilotAnswerCache(() => 0, 60_000, 2);
    cache.put(alice, "ask", "q1", "a1");
    cache.put(alice, "ask", "q2", "a2");
    cache.put(alice, "ask", "q3", "a3");
    expect(cache.size).toBe(2);
    expect(cache.get(alice, "ask", "q1")).toBeUndefined();
    expect(cache.get(alice, "ask", "q3")).toBe("a3");
  });

  it("duty keys are order independent and cannot be forged by a separator", () => {
    expect(dutiesKey(new Set(["b", "a"]))).toBe(dutiesKey(new Set(["a", "b"])));
    expect(dutiesKey(new Set(["a|b"]))).not.toBe(dutiesKey(new Set(["a", "b"])));
  });
});
