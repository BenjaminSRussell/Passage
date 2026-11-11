import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  assertProviderEndpoint,
  EgressDenied,
  hostMatches,
  parseSelector,
  PROFILE_TABLE,
  profileFromEnv,
  resolveProfile,
} from "../src/cloudprofile/index.js";
import { createAnthropicProvider } from "../src/providers/anthropic.js";
import { createBedrockProvider } from "../src/providers/bedrock.js";
import { createOpenAICompatibleProvider } from "../src/providers/openai.js";

const canonical = JSON.parse(readFileSync(fileURLToPath(new URL("../../../deploy/cloud-profiles/profiles.json", import.meta.url)), "utf8"));

describe("cloud profile table (#599)", () => {
  it("is the generated copy of deploy/cloud-profiles/profiles.json", () => {
    expect(PROFILE_TABLE).toEqual(canonical);
  });

  it("gives every profile every endpoint key, each with a Microsoft Learn source", () => {
    for (const [name, p] of Object.entries(PROFILE_TABLE.profiles)) {
      expect(Object.keys(p.endpoints).sort(), name).toEqual([...PROFILE_TABLE.endpointKeys].sort());
      for (const [key, ep] of Object.entries(p.endpoints)) {
        expect(ep.value, `${name}.${key}`).not.toBe("");
        expect(ep.source, `${name}.${key}`).toMatch(/^https:\/\/learn\.microsoft\.com\//);
      }
    }
  });

  it("keeps GCC on the worldwide authority and gcc-high on the .us one", () => {
    expect(resolveProfile("gcc").endpoints.entraAuthority).toBe("https://login.microsoftonline.com");
    expect(resolveProfile("gcc").fips).toBe(true);
    expect(resolveProfile("gcc-high").endpoints.entraAuthority).toBe("https://login.microsoftonline.us");
  });

  it("parses selectors and rejects unknown profiles instead of widening", () => {
    expect(parseSelector("gcc+hipaa")).toEqual(["gcc", ["hipaa"]]);
    expect(parseSelector(undefined)).toEqual(["commercial", []]);
    expect(() => profileFromEnv({ PASSAGE_CLOUD_PROFILE: "azure-stack" })).toThrow(/unknown cloud profile/);
    expect(() => resolveProfile("gcc", ["eu-data-boundary"])).toThrow(/cannot be combined/);
  });

  it("matches hosts the way the Go and Python packages do", () => {
    expect(hostMatches("kv.vault.azure.net", "*.vault.azure.net")).toBe(true);
    expect(hostMatches("vault.azure.net", "*.vault.azure.net")).toBe(false);
    expect(hostMatches("bedrock-runtime.us-east-1.amazonaws.com", "bedrock-runtime.*.amazonaws.com")).toBe(true);
  });
});

describe("provider egress under a cloud profile (#601)", () => {
  const gcc = { PASSAGE_CLOUD_PROFILE: "gcc" };

  it("refuses endpoints outside the gcc allowlist", () => {
    expect(() => assertProviderEndpoint("https://api.anthropic.com", { env: gcc })).toThrow(EgressDenied);
    expect(() => assertProviderEndpoint("https://api.openai.com/v1", { env: gcc })).toThrow(EgressDenied);
    expect(() => assertProviderEndpoint("https://bedrock-runtime.us-east-1.amazonaws.com", { env: gcc, region: "eastus2" })).toThrow(EgressDenied);
  });

  it("accepts Azure OpenAI in a US region and refuses one outside the allowed regions", () => {
    expect(() => assertProviderEndpoint("https://res.openai.azure.com/openai", { env: gcc, region: "eastus2" })).not.toThrow();
    expect(() => assertProviderEndpoint("https://res.openai.azure.com/openai", { env: gcc, region: "westeurope" })).toThrow(/outside/);
    expect(() => assertProviderEndpoint("https://res.openai.azure.com/openai", { env: gcc })).toThrow(/hosting region/);
  });

  it("leaves a commercial deployment unrestricted", () => {
    expect(() => assertProviderEndpoint("https://api.anthropic.com", { env: {} })).not.toThrow();
    expect(() => assertProviderEndpoint("https://api.anthropic.com", { env: { PASSAGE_CLOUD_PROFILE: "commercial" } })).not.toThrow();
  });

  it("applies when a provider adapter is built, before any request", () => {
    const egress = { profile: resolveProfile("gcc") };
    expect(() => createAnthropicProvider({ apiKey: "k", model: "m", egress })).toThrow(EgressDenied);
    expect(() => createBedrockProvider({ credentials: { accessKeyId: "a", secretAccessKey: "s" }, region: "us-east-1", modelId: "m", egress })).toThrow(EgressDenied);
    expect(() => createOpenAICompatibleProvider({ baseUrl: "https://api.openai.com/v1", model: "m", apiKey: "k", egress })).toThrow(EgressDenied);
    expect(() =>
      createOpenAICompatibleProvider({ baseUrl: "https://res.openai.azure.com/openai/deployments/d", model: "m", apiKey: "k", auth: "api-key", egress: { ...egress, region: "centralus" } }),
    ).not.toThrow();
  });

  it("removes paging and unreviewed LLM hosts under the hipaa overlay", () => {
    const hipaa = resolveProfile("commercial", ["hipaa"]);
    expect(hipaa.egressAllowlist).not.toContain("events.pagerduty.com");
    expect(hipaa.egressAllowlist).not.toContain("api.anthropic.com");
  });
});
