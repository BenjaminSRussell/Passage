/**
 * TypeScript side of the Passage cloud profile (#599). The data is deploy/cloud-profiles/profiles.json,
 * generated into profiles.generated.ts (`python3 deploy/cloud-profiles/cloudprofile.py sync`; a test fails when it
 * drifts). Read Azure hostnames from the resolved profile; do not write them in code.
 *
 * `PASSAGE_CLOUD_PROFILE` names the profile ("gcc", "gcc+hipaa"); unset means commercial and an unknown name throws,
 * so a typo fails closed instead of falling back to a wider profile.
 */
import { CLOUD_PROFILE_DATA } from "./profiles.generated.js";

export const ENV_PROFILE = "PASSAGE_CLOUD_PROFILE";

interface EndpointRow {
  value: string;
  source: string;
  status?: string;
  aliases?: string[];
}
interface ProfileRow {
  cloud: string;
  fips: boolean;
  allowedRegions: string[];
  endpoints: Record<string, EndpointRow>;
  registry: { default: string; requireMirror: boolean };
  egress: { llmProviders: string[]; enforceProviders: boolean; pagerDuty: boolean; extraAllow: string[] };
  keycloak: { passwordHash: string; passwordMinLength: number; eventRetentionDays: number };
}
interface OverlayRow {
  allowedRegions: string[];
  egressRemove: string[];
  requireFips: boolean;
  eventRetentionDays?: number;
  enforceProviders?: boolean;
  requireMirror?: boolean;
  idleTimeoutMinutes?: number;
  baaCoveredServices?: { source: string; services: string[] };
}
export interface ProfileTable {
  endpointKeys: string[];
  profiles: Record<string, ProfileRow>;
  overlays: Record<string, OverlayRow>;
}

export const PROFILE_TABLE = CLOUD_PROFILE_DATA as unknown as ProfileTable;

export interface ResolvedProfile {
  name: string;
  overlays: string[];
  fips: boolean;
  allowedRegions: string[];
  endpoints: Record<string, string>;
  llmProviders: string[];
  enforceProviders: boolean;
  egressAllowlist: string[];
  /** The hipaa overlay is active: model endpoints must be covered by a business associate agreement (Azure OpenAI). */
  hipaa: boolean;
  /** Azure services allowed to hold PHI under the hipaa overlay; empty without it. */
  baaCoveredServices: string[];
  /** Web app idle sign-out in minutes; 0 means the app default. */
  idleTimeoutMinutes: number;
}

const LLM_HOSTS: Record<string, string> = {
  anthropic: "api.anthropic.com",
  bedrock: "bedrock-runtime.*.amazonaws.com",
  openai: "api.openai.com",
};

export function parseSelector(selector: string | undefined): [string, string[]] {
  const parts = (selector ?? "").trim().split("+").map((p) => p.trim());
  const base = parts[0] === undefined || parts[0] === "" ? "commercial" : parts[0];
  return [base, parts.slice(1).filter((p) => p !== "")];
}

export function hostOf(value: string): string {
  return value.includes("://") ? new URL(value).hostname : value;
}

/** Exact host, `*.suffix` (one or more labels) or a `*` label inside a host (`bedrock-runtime.*.amazonaws.com`). */
export function hostMatches(host: string, pattern: string): boolean {
  const h = host.toLowerCase();
  const p = pattern.toLowerCase();
  if (h === p) return true;
  const labels = h.split(".");
  const plabels = p.split(".");
  if (plabels[0] === "*") {
    const rest = plabels.slice(1);
    return labels.length > rest.length && labels.slice(labels.length - rest.length).join(".") === rest.join(".");
  }
  return labels.length === plabels.length && plabels.every((pl, i) => pl === "*" || pl === labels[i]);
}

export function resolveProfile(base: string, overlays: string[] = [], table: ProfileTable = PROFILE_TABLE): ResolvedProfile {
  const p = table.profiles[base];
  if (p === undefined) throw new Error(`unknown cloud profile "${base}" (known: ${Object.keys(table.profiles).sort().join(", ")})`);
  let regions = [...p.allowedRegions];
  let fips = p.fips;
  const llm = [...p.egress.llmProviders];
  let pager = p.egress.pagerDuty;
  let enforce = p.egress.enforceProviders;
  let requireMirror = p.registry.requireMirror;
  let extra = [...p.egress.extraAllow];
  let idle = 0;
  let baa: string[] = [];
  for (const name of overlays) {
    const o = table.overlays[name];
    if (o === undefined) throw new Error(`unknown overlay "${name}"`);
    if (o.allowedRegions.length > 0) {
      regions = regions.length === 0 ? [...o.allowedRegions] : regions.filter((r) => o.allowedRegions.includes(r));
      if (regions.length === 0) throw new Error(`overlay ${name} cannot be combined with ${base}: no region is allowed by both`);
    }
    fips = fips || o.requireFips;
    enforce = enforce || o.enforceProviders === true;
    requireMirror = requireMirror || o.requireMirror === true;
    idle = Math.max(idle, o.idleTimeoutMinutes ?? 0);
    if (o.baaCoveredServices !== undefined) baa = [...o.baaCoveredServices.services];
    for (const item of o.egressRemove) {
      extra = extra.filter((h) => h !== item);
      if (item === "pagerDuty") pager = false;
      const i = llm.indexOf(item);
      if (i >= 0) llm.splice(i, 1);
    }
  }
  const endpoints: Record<string, string> = {};
  for (const [k, row] of Object.entries(p.endpoints)) endpoints[k] = row.value;
  const ep = (k: string): string => endpoints[k] ?? "";
  const hosts = [hostOf(ep("entraAuthority")), ...(p.endpoints.entraAuthority?.aliases ?? []).map(hostOf), hostOf(ep("graphRoot")), hostOf(ep("armEndpoint"))];
  hosts.push(`*.${ep("keyVaultSuffix")}`, `*.${ep("blobSuffix")}`, `*.${ep("containerRegistrySuffix")}`, `*.${ep("postgresSuffix")}`);
  for (const name of llm) hosts.push(name === "azure-openai" ? `*.${ep("azureOpenAISuffix")}` : (LLM_HOSTS[name] ?? ""));
  if (pager) hosts.push("events.pagerduty.com");
  if (!requireMirror) hosts.push("ghcr.io");
  hosts.push(...extra);
  return {
    name: base,
    overlays,
    fips,
    allowedRegions: regions,
    endpoints,
    llmProviders: llm,
    enforceProviders: enforce,
    egressAllowlist: [...new Set(hosts.filter((h) => h !== ""))],
    hipaa: overlays.includes("hipaa"),
    baaCoveredServices: baa,
    idleTimeoutMinutes: idle,
  };
}

export function profileFromEnv(env: Record<string, string | undefined>): ResolvedProfile {
  const [base, overlays] = parseSelector(env[ENV_PROFILE]);
  return resolveProfile(base, overlays);
}

/** A model provider endpoint the cloud profile does not allow. Raised at construction, before any request. */
export class EgressDenied extends Error {
  constructor(message: string) {
    super(message);
    this.name = "EgressDenied";
  }
}

export interface EgressOptions {
  /** Defaults to the profile named by PASSAGE_CLOUD_PROFILE. */
  profile?: ResolvedProfile;
  env?: Record<string, string | undefined>;
  /** Region the endpoint is hosted in. Checked against the profile's allowedRegions when it restricts them. */
  region?: string;
}

/** Where a model call is served: recorded on every run in the audit log (#605), so a residency review can see it. */
export interface ModelEndpoint {
  provider: string;
  host: string;
  /** Hosting region as configured (AGENTS_PROVIDER_REGION); "unspecified" when the deployment did not say. */
  region: string;
}

export function describeEndpoint(provider: string, url: string, region: string | undefined): ModelEndpoint {
  return { provider, host: new URL(url).hostname, region: region !== undefined && region !== "" ? region : "unspecified" };
}

/**
 * Refuses a provider endpoint that is not in the profile's egress allowlist (or whose region is outside
 * allowedRegions). Profiles that do not enforce provider egress (commercial) accept any endpoint, so existing
 * deployments are unchanged; gcc, gcc-high, dod and china enforce. Azure OpenAI in a US region is the documented
 * default for gcc.
 */
export function assertProviderEndpoint(url: string, opts: EgressOptions = {}): void {
  const profile = opts.profile ?? profileFromEnv(opts.env ?? process.env);
  if (!profile.enforceProviders) return;
  const parsed = new URL(url);
  // Plain http would send prompts and credentials in the clear, and an allowlisted host proves nothing about the transport.
  if (parsed.protocol !== "https:") {
    throw new EgressDenied(`provider endpoint ${parsed.hostname} must use https under the ${[profile.name, ...profile.overlays].join("+")} profile, got ${parsed.protocol.replace(":", "")}`);
  }
  const host = parsed.hostname;
  if (!profile.egressAllowlist.some((pattern) => hostMatches(host, pattern))) {
    throw new EgressDenied(`provider endpoint host ${host} is not in the ${[profile.name, ...profile.overlays].join("+")} egress allowlist`);
  }
  if (profile.allowedRegions.length > 0) {
    if (opts.region === undefined) {
      throw new EgressDenied(`provider endpoint ${host} needs its hosting region: profile ${profile.name} pins allowedRegions`);
    }
    if (!profile.allowedRegions.includes(opts.region)) {
      throw new EgressDenied(`provider region ${opts.region} is outside the ${profile.name} allowed regions`);
    }
  }
}
