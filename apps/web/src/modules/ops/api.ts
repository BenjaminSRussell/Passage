import { z } from "zod";
import { defaultFetch, requestUrl, type FetchLike } from "../../shell/api/http";

// The ops control plane (services/ops, #50). Every route serves the platform operator tenant only.
const BASE = "/api/v1/ops";

export const OPS_VIEW_DUTY = "ops.region.view";

const region = z.object({
  name: z.string(),
  role: z.enum(["PRIMARY", "PASSIVE", "FENCED"]),
  lagSeconds: z.number(),
  lagStale: z.boolean().optional(),
});
const regions = z.object({ items: z.array(region), maxLagSeconds: z.number() });
export type Region = z.infer<typeof region>;
export type Regions = z.infer<typeof regions>;

const breakGlass = z.object({
  id: z.string(),
  userId: z.string(),
  reasonCode: z.string(),
  ticketRef: z.string(),
  grantedAt: z.string(),
  expiresAt: z.string(),
  revokedAt: z.string().nullish(),
  reviewDueAt: z.string(),
  reviewedBy: z.string().optional(),
  reviewedAt: z.string().nullish(),
  reviewOutcome: z.string().optional(),
  reviewNote: z.string().optional(),
  uses: z.number(),
});
const breakGlassList = z.object({ items: z.array(breakGlass) });
export type BreakGlass = z.infer<typeof breakGlass>;

const sloLine = z.object({
  service: z.string(),
  requests: z.number(),
  availability: z.number().nullable(),
  availabilityTarget: z.number(),
  latencyP95Ms: z.number().nullable(),
  latencyTargetMs: z.number(),
  errorBudgetRemaining: z.number().nullable(),
  availabilityBreached: z.boolean(),
  latencyBreached: z.boolean(),
  breached: z.boolean(),
  breachedSince: z.string().optional(),
  noTraffic: z.boolean(),
});
const sloReport = z.object({ window: z.string(), generatedAt: z.string(), breached: z.number(), services: z.array(sloLine) });
export type SloLine = z.infer<typeof sloLine>;
export type SloReport = z.infer<typeof sloReport>;
export const SLO_WINDOWS = ["1h", "6h", "24h", "7d", "30d"] as const;
export type SloWindow = (typeof SLO_WINDOWS)[number];

const licenseItem = z.object({
  license: z.object({ tenantId: z.string(), issuedAt: z.string(), expiresAt: z.string(), fullUsers: z.number(), operationalUsers: z.number(), selfserviceUsers: z.number() }),
  version: z.number(),
  status: z.object({ state: z.enum(["VALID", "GRACE", "EXPIRED"]), daysToExpiry: z.number(), codes: z.array(z.string()) }),
  messages: z.array(z.string()),
  usage: z.object({
    activeFull: z.number(),
    activeOperational: z.number(),
    activeSelfservice: z.number(),
    countedAt: z.string().nullish(),
    lastState: z.string().optional(),
  }),
  tenantName: z.string(),
});
const licenseList = z.object({ items: z.array(licenseItem) });
export type LicenseItem = z.infer<typeof licenseItem>;

export function fetchRegions(signal?: AbortSignal, fetchImpl: FetchLike = defaultFetch): Promise<Regions> {
  return requestUrl("GET", `${BASE}/regions`, regions, fetchImpl, {}, undefined, signal);
}

export function fetchBreakGlass(signal?: AbortSignal, fetchImpl: FetchLike = defaultFetch): Promise<BreakGlass[]> {
  return requestUrl("GET", `${BASE}/break-glass`, breakGlassList, fetchImpl, {}, undefined, signal).then((r) => r.items);
}

export function fetchSlo(window: SloWindow, signal?: AbortSignal, fetchImpl: FetchLike = defaultFetch): Promise<SloReport> {
  return requestUrl("GET", `${BASE}/slo?window=${encodeURIComponent(window)}`, sloReport, fetchImpl, {}, undefined, signal);
}

export function fetchLicenses(signal?: AbortSignal, fetchImpl: FetchLike = defaultFetch): Promise<LicenseItem[]> {
  return requestUrl("GET", `${BASE}/licenses`, licenseList, fetchImpl, {}, undefined, signal).then((r) => r.items);
}
