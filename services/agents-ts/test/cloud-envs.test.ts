import { describe, expect, it } from "vitest";
import { assertProviderEndpoint, EgressDenied, resolveProfile } from "../src/cloudprofile/index.js";
import { createProviderFactory, loadConfig, withPhiGuard } from "../src/config.js";
import { createPhiGuard, parsePhiFields } from "../src/domain/phi.js";
import { createOpenAICompatibleProvider } from "../src/providers/openai.js";
import { createAnthropicProvider } from "../src/providers/anthropic.js";
import { runAgent } from "../src/runtime/engine.js";
import { FakeApprovals, FakeTools, makeDeps, makeRequest, postDescriptor, proposal, ScriptedProvider, APPROVER, codingDescriptor } from "./fakes.js";

const azure = "https://res.openai.azure.us/openai/deployments/d";

describe("provider egress under GCC High, DoD, EU Data Boundary and HIPAA (#602 #603 #605 #606)", () => {
  it("refuses plain http under an enforcing profile, even for an allowed host (#599)", () => {
    const gcc = { profile: resolveProfile("gcc"), region: "eastus2" };
    expect(() => assertProviderEndpoint("http://res.openai.azure.com/openai", gcc)).toThrow(/must use https/);
    expect(() => assertProviderEndpoint("https://res.openai.azure.com/openai", gcc)).not.toThrow();
    // commercial does not enforce, so local gateways keep working
    expect(() => assertProviderEndpoint("http://localhost:11434/v1", { profile: resolveProfile("commercial") })).not.toThrow();
  });

  it("gcc-high and dod allow Azure OpenAI in Azure Government only", () => {
    for (const name of ["gcc-high", "dod"]) {
      const profile = resolveProfile(name);
      const region = name === "dod" ? "usdodeast" : "usgovvirginia";
      expect(() => assertProviderEndpoint(azure, { profile, region })).not.toThrow();
      expect(() => assertProviderEndpoint("https://res.openai.azure.com/openai", { profile, region })).toThrow(EgressDenied);
      expect(() => assertProviderEndpoint("https://api.anthropic.com", { profile, region })).toThrow(EgressDenied);
      expect(() => assertProviderEndpoint("https://bedrock-runtime.us-gov-west-1.amazonaws.com", { profile, region })).toThrow(EgressDenied);
    }
  });

  it("the EU overlay accepts an EU-region Azure OpenAI deployment and refuses a US one or an unnamed region", () => {
    const profile = resolveProfile("commercial", ["eu-data-boundary"]);
    expect(profile.enforceProviders).toBe(true);
    expect(() => assertProviderEndpoint("https://res.openai.azure.com/openai", { profile, region: "swedencentral" })).not.toThrow();
    expect(() => assertProviderEndpoint("https://res.openai.azure.com/openai", { profile, region: "eastus2" })).toThrow(/outside/);
    expect(() => assertProviderEndpoint("https://res.openai.azure.com/openai", { profile })).toThrow(/hosting region/);
    expect(() => assertProviderEndpoint("https://api.anthropic.com", { profile, region: "westeurope" })).toThrow(EgressDenied);
  });

  it("the hipaa overlay turns enforcement on for a commercial profile, leaving only the BAA-covered Azure OpenAI", () => {
    const profile = resolveProfile("commercial", ["hipaa"]);
    expect(profile.hipaa).toBe(true);
    expect(profile.idleTimeoutMinutes).toBe(15);
    expect(profile.baaCoveredServices).toContain("Azure OpenAI Service");
    expect(() => assertProviderEndpoint("https://api.anthropic.com", { profile })).toThrow(EgressDenied);
    expect(() => assertProviderEndpoint("https://api.openai.com/v1", { profile })).toThrow(EgressDenied);
    expect(() => assertProviderEndpoint("https://res.openai.azure.com/openai", { profile })).not.toThrow();
  });
});

describe("the model endpoint recorded on every run (#605)", () => {
  it("adapters describe provider, host and the configured region", () => {
    const eu = { profile: resolveProfile("commercial", ["eu-data-boundary"]), region: "westeurope" };
    const p = createOpenAICompatibleProvider({ baseUrl: "https://res.openai.azure.com/openai/deployments/d", model: "m", apiKey: "k", egress: eu });
    expect(p.endpoint).toEqual({ provider: "azure-openai", host: "res.openai.azure.com", region: "westeurope" });
    const a = createAnthropicProvider({ apiKey: "k", model: "m", egress: { profile: resolveProfile("commercial") } });
    expect(a.endpoint).toEqual({ provider: "anthropic", host: "api.anthropic.com", region: "unspecified" });
  });

  it("a run that called the model carries the endpoint, a run refused before the call does not", async () => {
    const endpoint = { provider: "azure-openai", host: "res.openai.azure.com", region: "westeurope" };
    const provider = Object.assign(new ScriptedProvider([proposal(0.8)]), { endpoint });
    const tools = new FakeTools({ "passage.ap.invoice.post": postDescriptor, "passage.ap.invoice.propose_coding": codingDescriptor });
    const result = await runAgent(
      makeDeps({ provider, tools, approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) }),
      makeRequest(),
    );
    expect(result.modelEndpoint).toEqual(endpoint);
  });

  it("the factory passes AGENTS_PROVIDER_REGION to the adapter, so a region-pinned profile can build it", () => {
    const env = {
      MCP_URL: "http://mcp", AGENTS_JWT_ISSUER: "https://kc/realms/p", AGENTS_JWKS_FILE: "/dev/null",
      AGENTS_PROVIDER: "openai", OPENAI_API_KEY: "k", AGENTS_OPENAI_BASE_URL: "https://res.openai.azure.com/openai/deployments/d", AGENTS_OPENAI_AUTH: "api-key",
      PASSAGE_CLOUD_PROFILE: "commercial+eu-data-boundary",
    };
    const before = process.env["PASSAGE_CLOUD_PROFILE"];
    process.env["PASSAGE_CLOUD_PROFILE"] = env.PASSAGE_CLOUD_PROFILE;
    try {
      const without = createProviderFactory(loadConfig(env));
      expect(() => without("m")).toThrow(/hosting region/);
      const withRegion = createProviderFactory(loadConfig({ ...env, AGENTS_PROVIDER_REGION: "northeurope" }));
      expect(withRegion("m").endpoint?.region).toBe("northeurope");
    } finally {
      if (before === undefined) delete process.env["PASSAGE_CLOUD_PROFILE"];
      else process.env["PASSAGE_CLOUD_PROFILE"] = before;
    }
  });
});

describe("PHI-tagged fields never reach a model (#606)", () => {
  const guard = createPhiGuard(["diagnosis_code", "plan_tier"]);

  it("refuses a non-empty value under a tagged field, in snake or camel case, and names the field not the value", () => {
    expect(() => guard.assert('{"diagnosis_code":"J45.909","name":"x"}')).toThrow(/AGT-0215.*diagnosis_code/);
    expect(() => guard.assert('{"diagnosisCode": "J45.909"}')).toThrow(/AGT-0215/);
    expect(() => guard.assert("plan_tier: GOLD")).toThrow(/plan_tier/);
    try {
      guard.assert('{"diagnosis_code":"J45.909"}');
    } catch (err) {
      expect(String(err)).not.toContain("J45.909");
    }
  });

  it("lets empty values and other fields through", () => {
    expect(() => guard.assert('{"diagnosis_code":null,"plan_tier":"","other":"J45"}')).not.toThrow();
    expect(() => guard.assert("the plan tier was discussed")).not.toThrow();
  });

  it("reads the field list from a variable and from the generated JSON", () => {
    expect(parsePhiFields("a, b", '{"BenefitElection":["c"],"Other":["a"]}').sort()).toEqual(["a", "b", "c"]);
    expect(() => parsePhiFields(undefined, "42")).toThrow(/AGENTS_PHI_FIELDS_FILE/);
  });

  it("wraps a provider: the call is refused before the adapter runs, and an empty list changes nothing", async () => {
    const inner = new ScriptedProvider([]);
    const guarded = withPhiGuard(inner, guard);
    await expect(guarded.complete({ system: "s", user: '{"plan_tier":"GOLD"}', allowedTools: ["t"] })).rejects.toThrow("AGT-0215");
    expect(inner.seen).toHaveLength(0);
    expect(withPhiGuard(inner, createPhiGuard([]))).toBe(inner);
  });

  it("a run whose prompt carries a PHI field fails AGT-0215 and the budget hold is released with zero", async () => {
    const inner = new ScriptedProvider([proposal(0.8)]);
    const provider = withPhiGuard(inner, guard);
    const settled: Array<{ tokensIn: number; tokensOut: number }> = [];
    const base = makeDeps({ provider, tools: new FakeTools({ "passage.ap.invoice.post": postDescriptor, "passage.ap.invoice.propose_coding": codingDescriptor }), approvals: new FakeApprovals({ decision: "APPROVED", approverUserId: APPROVER }) });
    const budgets = { ...base.budgets, settle: async (h: Parameters<typeof base.budgets.settle>[0], u: { tokensIn: number; tokensOut: number }) => { settled.push(u); return base.budgets.settle(h, u); } };
    const result = await runAgent(
      { ...base, budgets },
      makeRequest({ prompt: 'invoice for {"plan_tier":"GOLD"}' }),
    );
    expect(result).toMatchObject({ status: "FAILED", code: "AGT-0215" });
    expect(settled).toEqual([{ tokensIn: 0, tokensOut: 0 }]);
    expect(inner.seen).toHaveLength(0);
  });
});
