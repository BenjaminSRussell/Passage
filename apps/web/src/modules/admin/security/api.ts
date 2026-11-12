import { z } from "zod";
import { apiFetch } from "../../../shell/api/http";

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

const defaultFetch: FetchLike = (input, init) => apiFetch(input, init);

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export const SELF_GRANT = "IAM-0416";
export const ASSIGNMENT_OVERLAP = "IDN-ASSIGNMENT-OVERLAP";
export const ASSIGNMENT_ENDED = "IDN-ASSIGNMENT-ENDED";
export const LEGAL_ENTITY_EXISTS = "IDN-LEGAL-ENTITY-EXISTS";
export const LEGAL_ENTITY_INACTIVE = "IDN-LEGAL-ENTITY-INACTIVE";

export class SecurityApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;
  readonly traceId?: string;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}, traceId?: string) {
    super(message);
    this.name = "SecurityApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
    this.traceId = traceId;
  }
}

async function readProblem(res: Response): Promise<SecurityApiError> {
  let body: { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown; traceId?: unknown } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    body = {};
  }
  const code = typeof body.code === "string" ? body.code : "COM-0500";
  const message =
    typeof body.detail === "string" ? body.detail : typeof body.title === "string" ? body.title : `request failed with status ${res.status}`;
  const fields = z.record(z.string(), z.string()).safeParse(body.errors ?? {});
  return new SecurityApiError(res.status, code, message, fields.success ? fields.data : {}, typeof body.traceId === "string" ? body.traceId : undefined);
}

async function send<T>(url: string, init: RequestInit, schema: z.ZodType<T, unknown>, fetchImpl: FetchLike): Promise<T> {
  const res = await fetchImpl(url, init);
  if (!res.ok) throw await readProblem(res);
  return schema.parse(await res.json());
}

const GET = { headers: { Accept: "application/json" } };
const JSON_HEADERS = { Accept: "application/json", "Content-Type": "application/json" };

const permissionSchema = z.object({
  resource: z.string(),
  method: z.string(),
  allow: z.boolean(),
  duty: z.string().optional(),
  dutyMissing: z.string().optional(),
});
export type Permission = z.infer<typeof permissionSchema>;

const effectivePermissionsSchema = z.object({
  userId: z.string(),
  legalEntityId: z.string().optional(),
  duties: z.array(z.string()),
  ignoredRoles: z.array(z.string()),
  permissions: z.array(permissionSchema),
});
export type EffectivePermissions = z.infer<typeof effectivePermissionsSchema>;

export function fetchEffectivePermissions(userId: string, legalEntityId?: string, fetchImpl: FetchLike = defaultFetch): Promise<EffectivePermissions> {
  const query = legalEntityId ? `?${new URLSearchParams({ legalEntityId }).toString()}` : "";
  return send(`/api/v1/identity/users/${encodeURIComponent(userId)}/effective-permissions${query}`, GET, effectivePermissionsSchema, fetchImpl);
}

const assignmentSchema = z.object({
  assignmentId: z.string(),
  userId: z.string(),
  role: z.string(),
  legalEntityId: z.string().optional(),
  validFrom: z.string(),
  validTo: z.string().optional(),
  reason: z.string().optional(),
  grantedBy: z.string().optional(),
  createdAt: z.string(),
});
export type RoleAssignment = z.infer<typeof assignmentSchema>;

const assignmentPageSchema = z.object({ items: z.array(assignmentSchema), next: z.string().optional() });
export type RoleAssignmentPage = z.infer<typeof assignmentPageSchema>;

export interface AssignmentQuery {
  userId?: string;
  role?: string;
  active: boolean;
  after?: string;
}

export function listAssignments(q: AssignmentQuery, fetchImpl: FetchLike = defaultFetch): Promise<RoleAssignmentPage> {
  const params = new URLSearchParams();
  if (q.userId) params.set("userId", q.userId);
  if (q.role) params.set("role", q.role);
  if (q.active) params.set("active", "true");
  if (q.after) params.set("after", q.after);
  return send(`/api/v1/authz/assignments?${params.toString()}`, GET, assignmentPageSchema, fetchImpl);
}

export interface AssignRoleInput {
  userId: string;
  role: string;
  legalEntityId?: string;
  validFrom?: string;
  validTo?: string;
  reason?: string;
}

export function assignRole(input: AssignRoleInput, fetchImpl: FetchLike = defaultFetch): Promise<RoleAssignment> {
  return send("/api/v1/authz/assignments", { method: "POST", headers: JSON_HEADERS, body: JSON.stringify(input) }, assignmentSchema, fetchImpl);
}

export function endAssignment(assignmentId: string, fetchImpl: FetchLike = defaultFetch): Promise<RoleAssignment> {
  return send(
    `/api/v1/authz/assignments/${encodeURIComponent(assignmentId)}/end`,
    { method: "POST", headers: JSON_HEADERS, body: "{}" },
    assignmentSchema,
    fetchImpl,
  );
}

const legalEntitySchema = z.object({
  legalEntityId: z.string(),
  code: z.string(),
  displayName: z.string(),
  status: z.string(),
  createdAt: z.string(),
});
export type LegalEntity = z.infer<typeof legalEntitySchema>;

const legalEntityPageSchema = z.object({ items: z.array(legalEntitySchema), next: z.string().optional() });
export type LegalEntityPage = z.infer<typeof legalEntityPageSchema>;

export function listLegalEntities(after?: string, fetchImpl: FetchLike = defaultFetch): Promise<LegalEntityPage> {
  const query = after ? `?${new URLSearchParams({ after }).toString()}` : "";
  return send(`/api/v1/legal-entities${query}`, GET, legalEntityPageSchema, fetchImpl);
}

export interface CreateLegalEntityInput {
  code: string;
  displayName: string;
}

export function createLegalEntity(input: CreateLegalEntityInput, fetchImpl: FetchLike = defaultFetch): Promise<LegalEntity> {
  return send("/api/v1/legal-entities", { method: "POST", headers: JSON_HEADERS, body: JSON.stringify(input) }, legalEntitySchema, fetchImpl);
}

export const USER_STATUSES = ["active", "deprovisioned"] as const;
export type UserStatus = (typeof USER_STATUSES)[number];

const userSchema = z.object({
  userId: z.string(),
  idpSubject: z.string(),
  email: z.string().optional(),
  displayName: z.string(),
  status: z.enum(USER_STATUSES),
  createdAt: z.string(),
});
export type User = z.infer<typeof userSchema>;

const userPageSchema = z.object({
  items: z.array(userSchema),
  next: z.string().optional(),
});
export type UserPage = z.infer<typeof userPageSchema>;

export interface UserQuery {
  q?: string;
  status?: UserStatus;
  legalEntityId?: string;
  after?: string;
}

export function listUsers(q: UserQuery, fetchImpl: FetchLike = defaultFetch): Promise<UserPage> {
  const params = new URLSearchParams();
  if (q.q) params.set("q", q.q);
  if (q.status) params.set("status", q.status);
  if (q.legalEntityId) params.set("legalEntityId", q.legalEntityId);
  if (q.after) params.set("after", q.after);
  return send(`/api/v1/identity/users?${params.toString()}`, GET, userPageSchema, fetchImpl);
}

export function getUser(userId: string, fetchImpl: FetchLike = defaultFetch): Promise<User> {
  return send(`/api/v1/identity/users/${encodeURIComponent(userId)}`, GET, userSchema, fetchImpl);
}

const dutySummarySchema = z.object({
  name: z.string(),
  serviceOnly: z.boolean(),
  permissionCount: z.number(),
});
export type DutySummary = z.infer<typeof dutySummarySchema>;

const dutyDetailSchema = z.object({
  name: z.string(),
  serviceOnly: z.boolean(),
  permissions: z
    .array(z.object({ resource: z.string(), methods: z.array(z.string()) }))
    .nullable()
    .transform((v) => v ?? []),
});
export type DutyDetail = z.infer<typeof dutyDetailSchema>;

const roleSummarySchema = z.object({
  name: z.string(),
  duties: z.array(z.string()),
});
export type RoleSummary = z.infer<typeof roleSummarySchema>;

const roleDetailSchema = z.object({
  name: z.string(),
  duties: z.array(dutyDetailSchema),
});
export type RoleDetail = z.infer<typeof roleDetailSchema>;

function filterQuery(params: Record<string, string | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) if (value) search.set(key, value);
  const text = search.toString();
  return text ? `?${text}` : "";
}

export function listRoles(q?: string, fetchImpl: FetchLike = defaultFetch): Promise<RoleSummary[]> {
  return send(`/api/v1/authz/roles${filterQuery({ q })}`, GET, z.object({ items: z.array(roleSummarySchema) }), fetchImpl).then((r) => r.items);
}

export function getRole(name: string, fetchImpl: FetchLike = defaultFetch): Promise<RoleDetail> {
  return send(`/api/v1/authz/roles/${encodeURIComponent(name)}`, GET, roleDetailSchema, fetchImpl);
}

export function listDuties(q?: string, serviceOnly?: boolean, fetchImpl: FetchLike = defaultFetch): Promise<DutySummary[]> {
  const query = filterQuery({
    q,
    serviceOnly: serviceOnly === undefined ? undefined : String(serviceOnly),
  });
  return send(`/api/v1/authz/duties${query}`, GET, z.object({ items: z.array(dutySummarySchema) }), fetchImpl).then((r) => r.items);
}

const customerContactSchema = z.object({
  contactId: z.string(),
  email: z.string(),
  displayName: z.string(),
  customerId: z.string(),
  legalEntityIds: z.array(z.string()),
  status: z.enum(["enabled", "disabled"]),
  invitationSent: z.boolean(),
  invitedAt: z.string().optional(),
  lastInvitationSentAt: z.string().optional(),
});
export type CustomerContact = z.infer<typeof customerContactSchema>;

const customerContactPageSchema = z.object({ items: z.array(customerContactSchema), next: z.string().optional() });

export interface CustomerContactMapping {
  customerId: string;
  legalEntityIds: string[];
}

export interface InviteCustomerContactInput extends CustomerContactMapping {
  email: string;
  givenName?: string;
  familyName?: string;
}

export function listCustomerContacts(after?: string, fetchImpl: FetchLike = defaultFetch): Promise<z.infer<typeof customerContactPageSchema>> {
  return send(`/api/v1/identity/customer-contacts${filterQuery({ after })}`, GET, customerContactPageSchema, fetchImpl);
}

export function inviteCustomerContact(input: InviteCustomerContactInput, fetchImpl: FetchLike = defaultFetch): Promise<CustomerContact> {
  return send("/api/v1/identity/customer-contacts", { method: "POST", headers: JSON_HEADERS, body: JSON.stringify(input) }, customerContactSchema, fetchImpl);
}

export function remapCustomerContact(contactId: string, input: CustomerContactMapping, fetchImpl: FetchLike = defaultFetch): Promise<CustomerContact> {
  return send(`/api/v1/identity/customer-contacts/${encodeURIComponent(contactId)}`, { method: "PATCH", headers: JSON_HEADERS, body: JSON.stringify(input) }, customerContactSchema, fetchImpl);
}

export function resendCustomerContactInvitation(contactId: string, fetchImpl: FetchLike = defaultFetch): Promise<CustomerContact> {
  return send(`/api/v1/identity/customer-contacts/${encodeURIComponent(contactId)}:resend`, { method: "POST", headers: GET.headers }, customerContactSchema, fetchImpl);
}

export function disableCustomerContact(contactId: string, fetchImpl: FetchLike = defaultFetch): Promise<CustomerContact> {
  return send(`/api/v1/identity/customer-contacts/${encodeURIComponent(contactId)}:disable`, { method: "POST", headers: GET.headers }, customerContactSchema, fetchImpl);
}
