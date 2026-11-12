import { apiFetch } from "../../../shell/api/http";
// Client for the party service (services/party/api/openapi.yaml). Browser
// calls use /api/v1/party/..., which the web BFF rewrites to the service.

const base = "/api/v1/party";

export type PartyType = "organization" | "person";
export type PartyStatus = "active" | "inactive" | "merged";
export type TaxType = "VAT" | "EIN" | "GSTIN";
export type ValidationStatus = "valid" | "invalid" | "unverified" | "format_only";
export type PendingChangeStatus = "pending" | "approved" | "rejected";
export type PendingChangeField = "credit_limit";

export interface Party {
  id: string;
  partyNumber: string;
  partyType: PartyType;
  name: string;
  searchName: string;
  status: PartyStatus;
  mergedIntoId?: string;
  legalName?: string;
  tradeName?: string;
  registrationNumber?: string;
  industryCode?: string;
  givenName?: string;
  middleName?: string;
  familyName?: string;
  prefix?: string;
  suffix?: string;
  dob?: string;
  gender?: string;
  languageCode?: string;
  timeZone?: string;
  ownerUserId?: string;
  ownerTeamId?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
  requestedId?: string;
  redirected?: boolean;
}

export interface Vendor {
  id: string;
  partyId: string;
  legalEntityId: string;
  accountNumber: string;
  vendorGroupId: string;
  paymentTermId?: string;
  currencyCode: string;
  blocked: string;
  version: number;
}

export interface TaxValidation {
  type: TaxType;
  country?: string;
  number: string;
  validationStatus: ValidationStatus;
}

export interface PendingChange {
  id: string;
  customerId: string;
  legalEntityId: string;
  field: PendingChangeField;
  currentValue: string | null;
  requestedValue: string;
  status: PendingChangeStatus;
  requestedBy: string;
  decidedBy?: string;
  reason?: string;
  requestedAt: string;
  decidedAt?: string;
}

export class PartyApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "PartyApiError";
  }
}

export interface Problem {
  status?: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof PartyApiError) {
    return {
      status: error.status,
      code: error.code,
      message: error.message,
      fields: error.fields,
    };
  }
  return {
    status: 0,
    code: "COM-0001",
    message: error instanceof Error ? error.message : "Request failed",
    fields: {},
  };
}

async function toError(res: Response): Promise<PartyApiError> {
  const body = (await res.json().catch(() => null)) as {
    code?: unknown;
    detail?: unknown;
    title?: unknown;
    errors?: unknown;
  } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new PartyApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await apiFetch(`${base}${path}`, {
    ...init,
    headers,
    credentials: "same-origin",
    signal,
  });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

const id = (value: string) => encodeURIComponent(value);

const post = (body: unknown, key?: string): RequestInit => ({
  method: "POST",
  body: JSON.stringify(body),
  headers: key ? { "Idempotency-Key": key } : undefined,
});

export type PartyNameFields = Partial<Record<"legalName" | "tradeName" | "registrationNumber" | "givenName" | "familyName", string>>;

export type PartyReceipt = Pick<Party, "id" | "partyNumber" | "partyType" | "status" | "version">;

export interface NewVendor {
  accountNumber: string;
  vendorGroupId: string;
  paymentTermId?: string;
  currencyCode: string;
}

export interface NewCustomer {
  accountNumber: string;
  customerGroupId: string;
  paymentTermId: string;
  postingProfileId: string;
  currencyCode: string;
}

export interface Customer extends NewCustomer {
  id: string;
  partyId: string;
  legalEntityId: string;
  creditLimit?: string;
}

export interface CreditLimitResult {
  creditLimit: string | null;
  pendingChange?: PendingChange;
}

export function createParty(input: { partyType: PartyType } & PartyNameFields, key: string): Promise<PartyReceipt> {
  return send<PartyReceipt>("/parties", post(input, key));
}

export function updateParty(partyId: string, version: number, fields: PartyNameFields): Promise<PartyReceipt> {
  return send<PartyReceipt>(`/parties/${id(partyId)}`, {
    method: "PATCH",
    body: JSON.stringify(fields),
    headers: { "If-Match": `"${version}"` },
  });
}

export function createVendorRole(partyId: string, input: NewVendor, key: string): Promise<Vendor> {
  return send<Vendor>(`/parties/${id(partyId)}/vendor`, post(input, key));
}

export function createCustomerRole(partyId: string, input: NewCustomer, key: string): Promise<Customer> {
  return send<Customer>(`/parties/${id(partyId)}/customer`, post(input, key));
}

export function setCreditLimit(customerId: string, creditLimit: string): Promise<CreditLimitResult> {
  return send<CreditLimitResult>(`/customers/${id(customerId)}/credit-limit`, post({ creditLimit }));
}

export function getParty(partyId: string, signal?: AbortSignal): Promise<Party> {
  return send<Party>(`/parties/${id(partyId)}`, {}, signal);
}

/** One customer or vendor role found by name or number (GET /customers:search, /vendors:search). */
export interface RoleHit {
  id: string;
  partyId: string;
  legalEntityId?: string;
  accountNumber: string;
  name: string;
  role: "customer" | "vendor";
}

/** Customers and vendors by name or number, for browsing; the service caps one page at 50 per role. */
export async function searchRoles(kind: "all" | "customer" | "vendor", q: string, signal?: AbortSignal): Promise<RoleHit[]> {
  const kinds = kind === "all" ? (["customer", "vendor"] as const) : ([kind] as const);
  const pages = await Promise.all(
    kinds.map(async (role) => {
      const query = new URLSearchParams({ limit: "50" });
      if (q.trim() !== "") query.set("q", q.trim());
      const body = await send<{ items?: Omit<RoleHit, "role">[] }>(`/${role}s:search?${query}`, {}, signal);
      return (body.items ?? []).map((hit): RoleHit => ({ ...hit, role }));
    }),
  );
  return pages.flat().sort((a, b) => a.name.localeCompare(b.name));
}

export async function listPartyVendors(partyId: string, signal?: AbortSignal): Promise<Vendor[]> {
  const body = await send<{ vendors: Vendor[] }>(`/parties/${id(partyId)}/vendors`, {}, signal);
  return Array.isArray(body.vendors) ? body.vendors : [];
}

export function validateTaxRegistration(input: { type: TaxType; country?: string; number: string }): Promise<TaxValidation> {
  const body: Record<string, string> = {
    type: input.type,
    number: input.number,
  };
  if (input.country) body.country = input.country;
  return send<TaxValidation>("/tax-registrations:validate", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function getPendingChange(changeId: string, signal?: AbortSignal): Promise<PendingChange> {
  return send<PendingChange>(`/pending-changes/${id(changeId)}`, {}, signal);
}

export function decidePendingChange(changeId: string, action: "approve" | "reject", reason: string): Promise<PendingChange> {
  const body = reason === "" ? undefined : JSON.stringify({ reason });
  return send<PendingChange>(`/pending-changes/${id(changeId)}:${action}`, {
    method: "POST",
    body,
  });
}

/** A supplier's own registration from the public portal page (services/party vendor_prospect), waiting for a buyer. */
export const PROSPECT_STATUSES = ["NEW", "ACCEPTED", "REJECTED"] as const;
export type ProspectStatus = (typeof PROSPECT_STATUSES)[number];

export interface Prospect {
  id: string;
  companyName: string;
  contactName: string;
  email: string;
  phone?: string;
  country: string;
  taxId?: string;
  website?: string;
  message?: string;
  status: ProspectStatus;
  createdAt: string;
  decidedAt?: string;
  decisionNote?: string;
  partyId?: string;
  vendorId?: string;
  requestId?: string;
}

/** An onboarding questionnaire a new supplier is asked to answer. */
export interface Questionnaire {
  id: string;
  name: string;
  version: number;
  createdAt: string;
}

/** What accepting a registration creates: the vendor role (blocked until onboarding is approved) and its questionnaire. */
export interface AcceptProspectInput extends NewVendor {
  questionnaireId: string;
}

/** GET /vendor-prospects: one page of registrations newest first, optionally of one status. */
export async function listProspects(status: ProspectStatus | "", cursor?: string, signal?: AbortSignal): Promise<{ items: Prospect[]; nextCursor?: string }> {
  const query = new URLSearchParams({ limit: "100" });
  if (status) query.set("status", status);
  if (cursor) query.set("cursor", cursor);
  const body = await send<{ items?: Prospect[]; nextCursor?: string }>(`/vendor-prospects?${query}`, {}, signal);
  return { items: Array.isArray(body.items) ? body.items : [], nextCursor: body.nextCursor || undefined };
}

export function getProspect(prospectId: string, signal?: AbortSignal): Promise<Prospect> {
  return send<Prospect>(`/vendor-prospects/${id(prospectId)}`, {}, signal);
}

/** POST /vendor-prospects/{id}:accept creates the party, a blocked vendor role and the onboarding questionnaire request. */
export function acceptProspect(prospectId: string, input: AcceptProspectInput): Promise<Prospect> {
  return send<Prospect>(`/vendor-prospects/${id(prospectId)}:accept`, post(input));
}

/** POST /vendor-prospects/{id}:reject closes the registration with a note; nothing is created. */
export function rejectProspect(prospectId: string, note: string): Promise<Prospect> {
  return send<Prospect>(`/vendor-prospects/${id(prospectId)}:reject`, post({ note }));
}

export async function listQuestionnaires(signal?: AbortSignal): Promise<Questionnaire[]> {
  const body = await send<{ items?: Questionnaire[] }>("/vendor-questionnaires", {}, signal);
  return Array.isArray(body.items) ? body.items : [];
}

/** Credit-limit changes waiting for a decision, oldest request last, a page at a time. */
export async function listPendingChanges(cursor?: string, signal?: AbortSignal): Promise<{ items: PendingChange[]; nextCursor?: string }> {
  const query = new URLSearchParams({ limit: "100" });
  if (cursor) query.set("cursor", cursor);
  const body = await send<{ items?: PendingChange[]; nextCursor?: string }>(`/pending-changes?${query}`, {}, signal);
  return { items: Array.isArray(body.items) ? body.items : [], nextCursor: body.nextCursor || undefined };
}
