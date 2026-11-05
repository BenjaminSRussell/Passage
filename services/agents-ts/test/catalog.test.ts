import { describe, expect, it } from "vitest";
import { AGENT_IDS, AGENT_INVOKE_DUTY, COMMIT_SUFFIX, defaultCatalog, INJECTION_GUARD, MODEL_VISIBLE_TOOL } from "../src/agents/catalog.js";
import { promptHash, VersionRegistry } from "../src/domain/registry.js";

const catalog = defaultCatalog("claude-test-model");

describe("agent catalog (FR-47.5 to FR-47.7, FR-47.10)", () => {
  it("defines the five PRD agents as version 1, pinned to the given model", () => {
    expect(catalog.map((a) => a.agentId)).toEqual([...AGENT_IDS]);
    for (const a of catalog) {
      expect(a.version).toBe(1);
      expect(a.modelId).toBe("claude-test-model");
      // 9.47.5: starting a run needs agents.invoke plus the agent's duty.
      expect(a.requiredDuties[0]).toBe(AGENT_INVOKE_DUTY);
      expect(a.requiredDuties.length).toBeGreaterThan(1);
    }
  });

  it("gives every agent a model-visible tool list of reads and proposals only", () => {
    for (const a of catalog) {
      for (const tool of a.allowedTools) {
        expect(tool, `${a.agentId} exposes ${tool}`).toMatch(MODEL_VISIBLE_TOOL);
        expect(tool, `${a.agentId} exposes a commit tool`).not.toMatch(COMMIT_SUFFIX);
      }
    }
  });

  it("never lists a commit tool, even by name, in any allow-list", () => {
    const all = catalog.flatMap((a) => a.allowedTools);
    expect(all.filter((t) => COMMIT_SUFFIX.test(t))).toEqual([]);
    expect(all).not.toContain("passage.ap.invoice.post");
  });

  it("gives every agent exactly one proposable tool, so each run ends in one governed proposal", () => {
    for (const a of catalog) {
      const proposable = a.allowedTools.filter((t) => /\.(propose|propose_[a-z_]+|draft)$/.test(t));
      expect(proposable, a.agentId).toHaveLength(1);
    }
  });

  it("states the BR-47.10 cash application limits in its prompt", () => {
    const cash = catalog.find((a) => a.agentId === "cash_app");
    expect(cash?.systemPrompt).toContain("20 invoices");
    expect(cash?.systemPrompt).toContain("tolerance");
  });

  it("tells every prompt that document text is data, not instructions", () => {
    for (const a of catalog) {
      expect(a.systemPrompt).toContain(INJECTION_GUARD);
    }
  });

  it("gives each agent a distinct prompt hash, stable across rebuilds (audit, FR-47.12)", () => {
    const hashes = catalog.map((a) => promptHash(a));
    expect(new Set(hashes).size).toBe(catalog.length);
    expect(defaultCatalog("claude-test-model").map((a) => promptHash(a))).toEqual(hashes);
  });

  it("registers in the version registry and promotes only with a passing eval (FR-47.10)", () => {
    const reg = new VersionRegistry();
    for (const a of catalog) reg.register(a);
    let caught: unknown;
    try {
      reg.promote("ap_coding", 1);
    } catch (e) {
      caught = e;
    }
    expect(caught).toMatchObject({ code: "AGT-0204" });
    reg.recordEval({ agentId: "ap_coding", version: 1, evalRunId: "ev-1", passed: true });
    expect(reg.promote("ap_coding", 1).agentId).toBe("ap_coding");
    expect(reg.production("ap_coding")?.version).toBe(1);
  });
});

describe("MODEL_VISIBLE_TOOL and COMMIT_SUFFIX", () => {
  it.each([
    ["passage.ap.invoice.get", true],
    ["passage.ap.vendor.history.list", true],
    ["passage.ar.settlement.propose", true],
    ["passage.collections.letter.draft", true],
    ["passage.ap.invoice.post", false],
    ["passage.ap.invoice.apply_coding", false],
    ["passage.ap", false],
  ])("%s visible=%s", (tool, visible) => {
    expect(MODEL_VISIBLE_TOOL.test(tool)).toBe(visible);
  });

  it.each([
    ["passage.ap.invoice.post", true],
    ["passage.ap.invoice.apply_coding", true],
    ["passage.ap.invoice.propose_coding", false],
  ])("%s commit=%s", (tool, commit) => {
    expect(COMMIT_SUFFIX.test(tool)).toBe(commit);
  });
});
