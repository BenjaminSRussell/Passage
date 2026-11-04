// Client for the integration hub (services/integration-hub-ts/api/openapi.yaml). Browser calls use
// /api/v1/hub/..., which the gateway/BFF maps to /v1/hub/... on the hub (handoff in the hub's NOT-IMPLEMENTED.md).
// Credentials are write-only: this client sends them on create and never reads them back.

import { EdiApiError, newKey } from "./client";

export { newKey };

export const hubBase = "/api/v1/hub";

/** Duties the pages are gated on (seeded in policies/authz/data.json). */
export const DUTY_HUB_READ = "hub.delivery.read";
export const DUTY_HUB_WRITE = "hub.subscription.write";

export type SyncMethod = "POST" | "PUT" | "PATCH";
export const SYNC_METHODS: readonly SyncMethod[] = ["POST", "PUT", "PATCH"];
export type AuthType = "basic" | "bearer" | "api-key" | "oauth2-client-credentials";
export const AUTH_TYPES: readonly { value: AuthType; label: string }[] = [
  { value: "basic", label: "Basic (user and password)" },
  { value: "bearer", label: "Bearer token" },
  { value: "api-key", label: "API key in a header" },
  { value: "oauth2-client-credentials", label: "OAuth 2.0 client credentials" },
];
export type ClientErrorPolicy = "dead_letter" | "retry";
export type DeliveryStatus = "pending" | "delivered" | "dead_letter";
export const DELIVERY_STATUSES: readonly DeliveryStatus[] = ["pending", "delivered", "dead_letter"];

export interface LineCorrelation {
  bodyPath: string;
  responsePath: string;
}

export interface LineKey {
  index: number;
  legacyKey: string;
}

export interface LookupUse {
  name: string;
  table: string;
  version: number;
}

export interface RestSettings {
  outboundMappingId: string;
  method: SyncMethod;
  responseKeyPath: string;
  /** Path with one [*] to the legacy key of each element of a pushed collection (absent on older subscriptions). */
  lineKeyPath?: string;
  lineCorrelation?: LineCorrelation;
  onClientError: ClientErrorPolicy;
  auth: { type: AuthType; username?: string; headerName?: string; tokenUrl?: string; clientId?: string; scope?: string; hasCredentials: true };
}

export interface Subscription {
  id: string;
  kind: "webhook" | "rest-push";
  rest: RestSettings | null;
  name: string;
  url: string;
  patterns: string[];
  status: "active" | "paused";
  pausedReason: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

/** The secret fields are sent once and never returned. */
export interface AuthInput {
  type: AuthType;
  username?: string;
  password?: string;
  token?: string;
  headerName?: string;
  value?: string;
  tokenUrl?: string;
  clientId?: string;
  clientSecret?: string;
  scope?: string;
}

export interface NewRestSubscription {
  kind: "rest-push";
  name: string;
  url: string;
  patterns: string[];
  outboundMappingId: string;
  method: SyncMethod;
  responseKeyPath?: string;
  lineKeyPath?: string;
  lineCorrelation?: LineCorrelation;
  onClientError: ClientErrorPolicy;
  auth: AuthInput;
}

export interface Delivery {
  id: string;
  subscriptionId: string;
  eventId: string;
  eventType: string;
  aggregateId: string;
  sequence: number;
  status: DeliveryStatus;
  attempts: number;
  lastStatus: number | null;
  nextRetryAt: number | null;
  replay: boolean;
  deadLetterCode: string | null;
  responseExcerpt: string | null;
  legacyKey: string | null;
  /** The key the ERP assigned to each element of the pushed collection. */
  lineKeys?: LineKey[];
  /** The lookup table versions the last render read. */
  lookups?: LookupUse[];
  createdAt: string;
}

export interface Page<T> {
  items: T[];
  nextCursor: string | null;
}

export interface TestSendResult {
  stage: "render" | "url" | "token" | "built" | "sent";
  method: string;
  url?: string;
  body?: unknown;
  status?: number | null;
  excerpt?: string;
  legacyKey?: string;
  lineKeys?: LineKey[];
  lineKeyError?: string;
  lookups?: LookupUse[];
}

async function toError(res: Response): Promise<EdiApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message = typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  return new EdiApiError(res.status, code, message);
}

async function hub<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  const res = await fetch(`${hubBase}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (res.status === 204 ? null : await res.json()) as T;
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") q.set(key, String(value));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

const json = (method: string, body: unknown, extra: Record<string, string> = {}): RequestInit => ({
  method,
  headers: { "Content-Type": "application/json", ...extra },
  body: JSON.stringify(body),
});

export function listSubscriptions(cursor?: string, signal?: AbortSignal): Promise<Page<Subscription>> {
  return hub<Page<Subscription>>(`/subscriptions${query({ limit: 100, cursor })}`, {}, signal);
}

export function getSubscription(id: string, signal?: AbortSignal): Promise<Subscription> {
  return hub<Subscription>(`/subscriptions/${encodeURIComponent(id)}`, {}, signal);
}

/** Reuse one key per form so a retried submit cannot create two subscriptions. */
export function createRestSubscription(input: NewRestSubscription, key: string = newKey()): Promise<Subscription> {
  return hub<Subscription>("/subscriptions", json("POST", input, { "Idempotency-Key": key }));
}

export function resumeSubscription(id: string): Promise<Subscription> {
  return hub<Subscription>(`/subscriptions/${encodeURIComponent(id)}:resume`, { method: "POST" });
}

export function testSend(id: string, event: unknown, send: boolean): Promise<TestSendResult> {
  return hub<TestSendResult>(`/subscriptions/${encodeURIComponent(id)}:test`, json("POST", { event, send }));
}

export function listDeliveries(
  filter: { status?: DeliveryStatus | ""; subscriptionId?: string; cursor?: string } = {},
  signal?: AbortSignal,
): Promise<Page<Delivery>> {
  return hub<Page<Delivery>>(`/deliveries${query({ limit: 50, ...filter })}`, {}, signal);
}

export function listDeadLetters(cursor?: string, signal?: AbortSignal): Promise<Page<Delivery>> {
  return hub<Page<Delivery>>(`/dead-letters${query({ limit: 50, cursor })}`, {}, signal);
}

export function replayDelivery(id: string): Promise<{ status: "pending" }> {
  return hub<{ status: "pending" }>(`/deliveries/${encodeURIComponent(id)}:replay`, { method: "POST" });
}

// API clients and quotas (FR-35.8, BR-35.8). The Keycloak secret never passes through the hub or these pages.

/** New duty, enforced by the hub and by the page's actions only: no nav item or route is gated on it until it is seeded (hub NOT-IMPLEMENTED.md). */
export const DUTY_HUB_CLIENT_WRITE = "hub.client.write";
/** Defaults and ranges of the quota (FR-35.8, §6.35.13). */
export const DEFAULT_QUOTA_PER_MIN = 600;
export const DEFAULT_BURST = 100;
export const QUOTA_MIN = 10;
export const QUOTA_MAX = 100_000;
export const BURST_MIN = 1;
export const BURST_MAX = 100_000;

export interface ApiClient {
  id: string;
  clientId: string;
  name: string;
  scopes: string[];
  quotaPerMin: number;
  burst: number;
  sandboxTenantId: string | null;
  ownerContact: string;
  status: "active" | "revoked";
  revokedAt: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface NewApiClient {
  clientId: string;
  name: string;
  scopes: string[];
  quotaPerMin: number;
  burst: number;
  sandboxTenantId?: string;
  ownerContact: string;
}

export interface ApiClientPatch {
  version: number;
  name?: string;
  scopes?: string[];
  quotaPerMin?: number;
  burst?: number;
  sandboxTenantId?: string | null;
  ownerContact?: string;
}

export interface UsageMinute {
  minute: string;
  allowed: number;
  throttled: number;
}

export function listClients(cursor?: string, signal?: AbortSignal): Promise<Page<ApiClient>> {
  return hub<Page<ApiClient>>(`/clients${query({ limit: 100, cursor })}`, {}, signal);
}

export function getClient(id: string, signal?: AbortSignal): Promise<ApiClient> {
  return hub<ApiClient>(`/clients/${encodeURIComponent(id)}`, {}, signal);
}

/** Reuse one key per form so a retried submit cannot register two clients. */
export function registerClient(input: NewApiClient, key: string = newKey()): Promise<ApiClient> {
  return hub<ApiClient>("/clients", json("POST", input, { "Idempotency-Key": key }));
}

export function updateClient(id: string, patch: ApiClientPatch): Promise<ApiClient> {
  return hub<ApiClient>(`/clients/${encodeURIComponent(id)}`, json("PATCH", patch));
}

export function revokeClient(id: string): Promise<ApiClient> {
  return hub<ApiClient>(`/clients/${encodeURIComponent(id)}:revoke`, { method: "POST" });
}

export function getClientUsage(id: string, minutes = 60, signal?: AbortSignal): Promise<{ minutes: UsageMinute[] }> {
  return hub<{ minutes: UsageMinute[] }>(`/clients/${encodeURIComponent(id)}/usage${query({ minutes })}`, {}, signal);
}
