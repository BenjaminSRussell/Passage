import { z } from "zod";
import { defaultFetch, requestUrl, type FetchLike } from "../api/http";

export const FEATURES_PATH = "/api/v1/tenant/features";
export const SETUP_PATH = "/api/v1/tenant/setup";
export const WORKSPACE_CONFIG_PATH = "/api/v1/tenant/workspace-config";
export const DEMO_COMPANY_PATH = "/api/v1/org/demo-company";

// Duties the shell checks before it calls these endpoints, so a person who cannot read them costs no request.
export const FEATURE_VIEW_DUTY = "identity.feature.view";
export const FEATURE_MAINTAIN_DUTY = "identity.feature.maintain";
export const SETUP_VIEW_DUTY = "identity.setup.view";
export const SETUP_MAINTAIN_DUTY = "identity.setup.maintain";
export const DEMO_COMPANY_DUTY = "org.demo-company.manage";

const feature = z.object({
  key: z.string(),
  name: z.string(),
  description: z.string(),
  module: z.string(),
  dependsOn: z.array(z.string()).nullish().transform((d) => d ?? []),
  enabled: z.boolean(),
});
const template = z.object({ key: z.string(), name: z.string(), description: z.string(), features: z.array(z.string()) });
const featuresResponse = z.object({ features: z.array(feature), templates: z.array(template) });

export type TenantFeature = z.infer<typeof feature>;
export type FeatureTemplate = z.infer<typeof template>;
export type TenantFeatures = z.infer<typeof featuresResponse>;

export function fetchFeatures(signal?: AbortSignal, fetchImpl: FetchLike = defaultFetch): Promise<TenantFeatures> {
  return requestUrl("GET", FEATURES_PATH, featuresResponse, fetchImpl, {}, undefined, signal);
}

export function saveFeatures(change: { template: string } | { features: Record<string, boolean> }, fetchImpl: FetchLike = defaultFetch): Promise<TenantFeatures> {
  return requestUrl("PUT", FEATURES_PATH, featuresResponse, fetchImpl, {}, change);
}

export const stepStatuses = ["todo", "done", "skipped"] as const;
export type StepStatus = (typeof stepStatuses)[number];

const step = z.object({ key: z.string(), name: z.string(), description: z.string(), status: z.enum(stepStatuses) });
const setupResponse = z.object({ steps: z.array(step), total: z.number(), finished: z.number(), complete: z.boolean() });

export type SetupStep = z.infer<typeof step>;
export type TenantSetup = z.infer<typeof setupResponse>;

export function fetchSetup(signal?: AbortSignal, fetchImpl: FetchLike = defaultFetch): Promise<TenantSetup> {
  return requestUrl("GET", SETUP_PATH, setupResponse, fetchImpl, {}, undefined, signal);
}

export function saveSetupStep(key: string, status: StepStatus, fetchImpl: FetchLike = defaultFetch): Promise<TenantSetup> {
  return requestUrl("PUT", `${SETUP_PATH}/steps/${encodeURIComponent(key)}`, setupResponse, fetchImpl, {}, { status });
}

const workspaceConfigResponse = z.object({ config: z.unknown() });

/** The tenant's workspace override as stored, or undefined when the tenant uses the shipped workspaces. */
export async function fetchWorkspaceOverride(signal?: AbortSignal, fetchImpl: FetchLike = defaultFetch): Promise<unknown> {
  const body = await requestUrl("GET", WORKSPACE_CONFIG_PATH, workspaceConfigResponse, fetchImpl, {}, undefined, signal);
  return body.config ?? undefined;
}

const demoCompanyResponse = z.object({ package: z.string(), created: z.number(), present: z.number() });
export type DemoCompanyResult = z.infer<typeof demoCompanyResponse>;

export function loadDemoCompany(fetchImpl: FetchLike = defaultFetch): Promise<DemoCompanyResult> {
  return requestUrl("POST", DEMO_COMPANY_PATH, demoCompanyResponse, fetchImpl, {}, undefined);
}
