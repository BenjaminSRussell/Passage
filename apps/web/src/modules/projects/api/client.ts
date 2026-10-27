// Typed client for the projects service (services/projects/api/openapi.yaml).
// Browser paths are /api/v1/projects/...; the BFF rewrites them to /v1/projects/...
// No token ever reaches the browser: the session cookie travels with same-origin requests.

export const STAGES = ["CREATED", "IN_PROCESS", "ON_HOLD", "FINISHED"] as const;
export type Stage = (typeof STAGES)[number];

export const PROJECT_TYPES = ["TIME_MATERIAL", "FIXED_PRICE", "INTERNAL", "COST", "INVESTMENT", "TIME"] as const;
export type ProjectType = (typeof PROJECT_TYPES)[number];

/** Types that bill a customer, so a top-level project of this type needs one (domain.ExternalType). */
export const BILLABLE_TYPES: readonly ProjectType[] = ["TIME_MATERIAL", "FIXED_PRICE"];

export interface Project {
  id: string;
  legalEntityId: string;
  parentProjectId?: string;
  projectGroupId: string;
  projectType: ProjectType;
  customerPartyId?: string;
  contractId?: string;
  currency: string;
  /** Decimal string in the currency's minor unit; applied change orders raise it (BR-41.13). */
  contractValue: string;
  stage: Stage;
  startDate: string;
  endDate?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
}

/** Body of POST /v1/projects. A sub-project may omit group, customer and contract (inherited, FR-41.1). */
export interface NewProject {
  id: string;
  legalEntityId: string;
  parentProjectId?: string;
  projectGroupId?: string;
  projectType: ProjectType;
  customerPartyId?: string;
  contractId?: string;
  currency: string;
  contractValue?: string;
  startDate: string;
  endDate?: string;
}

export interface ProjectPage {
  items: Project[];
  nextCursor?: string;
}

export interface ListParams {
  stage?: Stage | "";
  projectType?: ProjectType | "";
  cursor?: string;
  limit?: number;
}

// Stage moves the service accepts (mirrors domain.Transition, FR-41.1). The server stays authoritative.
export const nextStages: Record<Stage, Stage[]> = {
  CREATED: ["IN_PROCESS"],
  IN_PROCESS: ["ON_HOLD", "FINISHED"],
  ON_HOLD: ["IN_PROCESS"],
  FINISHED: [],
};

export class ProjectApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "ProjectApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

/** A failure as a page shows it: status, stable code, message and field errors. */
export interface Problem {
  status: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof ProjectApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "NETWORK", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

const BASE = "/api/v1/projects";

async function failure(res: Response): Promise<ProjectApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new ProjectApiError(res.status, code, message, fields);
}

async function call<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`${BASE}${path}`, { ...init, headers, signal, credentials: "same-origin" });
  if (!res.ok) throw await failure(res);
  return (await res.json()) as T;
}

/** A fresh Idempotency-Key for one create attempt. */
export function newKey(): string {
  return crypto.randomUUID();
}

function query(params: ListParams): string {
  const q = new URLSearchParams();
  if (params.stage) q.set("stage", params.stage);
  if (params.projectType) q.set("projectType", params.projectType);
  if (params.cursor) q.set("cursor", params.cursor);
  if (params.limit) q.set("limit", String(params.limit));
  const s = q.toString();
  return s ? `?${s}` : "";
}

export function listProjects(params: ListParams = {}, signal?: AbortSignal): Promise<ProjectPage> {
  return call<ProjectPage>(query(params), {}, signal);
}

export function getProject(id: string, signal?: AbortSignal): Promise<Project> {
  return call<Project>(`/${encodeURIComponent(id)}`, {}, signal);
}

/** Creates a project in stage CREATED. Reuse the key when retrying the same request; a replay returns the stored project. */
export function createProject(input: NewProject, idempotencyKey: string): Promise<Project> {
  return call<Project>("", { method: "POST", headers: { "Idempotency-Key": idempotencyKey }, body: JSON.stringify(input) });
}

/** Sends the version the user saw as If-Match; a stale version fails with 412 conflict_version. */
export function transitionProject(id: string, to: Stage, version: number): Promise<Project> {
  return call<Project>(`/${encodeURIComponent(id)}/transitions`, {
    method: "POST",
    headers: { "If-Match": `"${version}"` },
    body: JSON.stringify({ to }),
  });
}

// ---- Setup: project groups and posting profiles (FR-41.1, FR-41.5) ----

export interface ProjectGroup {
  id: string;
  name: string;
  defaultProjectType?: ProjectType;
  version: number;
  createdAt: string;
  updatedAt: string;
}

/** Body of PUT /v1/projects/groups/{groupId}. */
export interface ProjectGroupInput {
  name: string;
  defaultProjectType?: ProjectType;
}

export const LINE_PROPERTIES = ["CHARGEABLE", "NON_CHARGEABLE"] as const;
export type LineProperty = (typeof LINE_PROPERTIES)[number];

/** The posting-profile wildcard: a group fallback is (group, *, *), the catch-all (*, *, *). */
export const WILDCARD = "*";

export interface PostingProfileInput {
  projectGroup: string;
  category: string;
  lineProperty: LineProperty | typeof WILDCARD;
  costAccount: string;
  wipAccount: string;
  accruedRevenueAccount: string;
  invoicedRevenueAccount: string;
  onAccountAccount: string;
}

export interface PostingProfile extends PostingProfileInput {
  version: number;
}

export interface Items<T> {
  items: T[];
}

/** Sends the version as If-Match when replacing; without it the row is created (201). */
function versionHeader(version?: number): Record<string, string> {
  return version ? { "If-Match": `"${version}"` } : {};
}

export function listGroups(signal?: AbortSignal): Promise<Items<ProjectGroup>> {
  return call<Items<ProjectGroup>>("/groups", {}, signal);
}

export function saveGroup(id: string, input: ProjectGroupInput, version?: number): Promise<ProjectGroup> {
  return call<ProjectGroup>(`/groups/${encodeURIComponent(id)}`, { method: "PUT", headers: versionHeader(version), body: JSON.stringify(input) });
}

export function listProfiles(signal?: AbortSignal): Promise<Items<PostingProfile>> {
  return call<Items<PostingProfile>>("/posting-profiles", {}, signal);
}

export function saveProfile(input: PostingProfileInput, version?: number): Promise<PostingProfile> {
  return call<PostingProfile>("/posting-profiles", { method: "PUT", headers: versionHeader(version), body: JSON.stringify(input) });
}

/** Resolves the accounts for a concrete key with the FR-41.5 fallbacks; PROJ-POST-001 when nothing matches. */
export function resolveProfile(key: { projectGroup: string; category: string; lineProperty: LineProperty }): Promise<PostingProfile> {
  return call<PostingProfile>("/posting-profiles/resolve", { method: "POST", body: JSON.stringify(key) });
}

// ---- Plan versions and WBS (FR-41.2) ----

export const PLAN_KINDS = ["WORKING", "BASELINE", "SIMULATION"] as const;
export type PlanKind = (typeof PLAN_KINDS)[number];

export interface PlanVersion {
  id: string;
  projectId: string;
  kind: PlanKind;
  sequence: number;
  copiedFromVersionId?: string;
  /** Set on a BASELINE an applied change order replaced (BR-41.13). */
  supersededByVersionId?: string;
  locked: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export const DEPENDENCY_TYPES = ["FS", "SS", "FF", "SF"] as const;
export type DependencyType = (typeof DEPENDENCY_TYPES)[number];

export interface WbsNode {
  id: string;
  planVersionId: string;
  wbsCode: string;
  parentId?: string;
  name: string;
  effortHours?: string;
  durationDays: number;
  constraintType: "ASAP" | "SNET";
  constraintDate?: string;
  roleId?: string;
  isBillableMilestone: boolean;
  earlyStart?: string;
  earlyFinish?: string;
  lateStart?: string;
  lateFinish?: string;
  slackDays?: number;
  critical?: boolean;
}

export interface WbsDependency {
  predecessorId: string;
  successorId: string;
  type: DependencyType;
  lagDays: number;
}

export interface WbsView {
  planVersion: PlanVersion;
  nodes: WbsNode[];
  dependencies: WbsDependency[];
}

export interface ScheduleView extends WbsView {
  finishDate?: string;
  criticalPath: string[];
}

export interface WbsNodeInput {
  wbsCode: string;
  parentWbsCode?: string;
  name: string;
  effortHours?: string;
  durationDays: number;
  constraintType?: "ASAP" | "SNET";
  constraintDate?: string;
  isBillableMilestone: boolean;
}

export interface WbsDependencyInput {
  predecessorWbsCode: string;
  successorWbsCode: string;
  type: DependencyType;
  lagDays: number;
}

const plansPath = (projectId: string) => `/${encodeURIComponent(projectId)}/plans`;

export function listPlans(projectId: string, signal?: AbortSignal): Promise<Items<PlanVersion>> {
  return call<Items<PlanVersion>>(plansPath(projectId), {}, signal);
}

/** Adds a plan version; BASELINE and SIMULATION copy another version. Reuse the key when retrying. */
export function createPlan(projectId: string, input: { kind: PlanKind; copyFromVersionId?: string }, idempotencyKey: string): Promise<PlanVersion> {
  return call<PlanVersion>(plansPath(projectId), { method: "POST", headers: { "Idempotency-Key": idempotencyKey }, body: JSON.stringify(input) });
}

export function getWbs(projectId: string, planId: string, signal?: AbortSignal): Promise<WbsView> {
  return call<WbsView>(`${plansPath(projectId)}/${encodeURIComponent(planId)}/wbs`, {}, signal);
}

/** Replaces the whole WBS; the plan version the user saw goes as If-Match (412 conflict_version when stale). */
export function replaceWbs(projectId: string, planId: string, body: { nodes: WbsNodeInput[]; dependencies: WbsDependencyInput[] }, version: number): Promise<WbsView> {
  return call<WbsView>(`${plansPath(projectId)}/${encodeURIComponent(planId)}/wbs`, {
    method: "PUT",
    headers: { "If-Match": `"${version}"` },
    body: JSON.stringify(body),
  });
}

export function schedulePlan(projectId: string, planId: string): Promise<ScheduleView> {
  return call<ScheduleView>(`${plansPath(projectId)}/${encodeURIComponent(planId)}/schedule`, { method: "POST" });
}

// ---- Change orders and claims (FR-41.18, BR-41.13) ----

export const CHANGE_ORDER_TYPES = ["SCOPE_CHANGE", "CLAIM_CUSTOMER", "CLAIM_VENDOR", "INTERNAL"] as const;
export type ChangeOrderType = (typeof CHANGE_ORDER_TYPES)[number];

export const CHANGE_ORDER_STATUSES = ["DRAFT", "SUBMITTED", "APPROVED", "REJECTED", "APPLIED"] as const;
export type ChangeOrderStatus = (typeof CHANGE_ORDER_STATUSES)[number];

export interface ChangeOrderLine {
  lineNo: number;
  wbsNodeId?: string;
  wbsCode?: string;
  costImpact: string;
  revenueImpact: string;
  effortHoursDelta: string;
  scheduleDaysDelta: number;
}

export interface ChangeOrder {
  id: string;
  number: string;
  projectId: string;
  type: ChangeOrderType;
  title: string;
  description: string;
  status: ChangeOrderStatus;
  customerApprovalRef?: string;
  vendorPartyId?: string;
  createdBy: string;
  decidedBy?: string;
  appliedBy?: string;
  appliedBaselineVersionId?: string;
  appliedAt?: string;
  costImpact: string;
  revenueImpact: string;
  /** Empty in list responses. */
  lines: ChangeOrderLine[];
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ChangeOrderLineInput {
  wbsNodeId?: string;
  costImpact: string;
  revenueImpact: string;
  effortHoursDelta?: string;
  scheduleDaysDelta: number;
}

export interface NewChangeOrder {
  projectId: string;
  type: ChangeOrderType;
  title: string;
  description?: string;
  customerApprovalRef?: string;
  vendorPartyId?: string;
  lines: ChangeOrderLineInput[];
}

export interface ApplyResult {
  changeOrder: ChangeOrder;
  effect: { contractValueDelta: string; budgetDelta: string; contractValue: string; effortHoursDelta: string; scheduleDaysDelta: number };
  baseline: PlanVersion;
}

export type ChangeOrderAction = "submit" | "approve" | "reject" | "apply";

/** Actions per status (mirrors domain.CheckChangeOrderMove). The service stays authoritative. */
export const changeOrderActions: Record<ChangeOrderStatus, ChangeOrderAction[]> = {
  DRAFT: ["submit"],
  SUBMITTED: ["approve", "reject"],
  APPROVED: ["apply"],
  REJECTED: [],
  APPLIED: [],
};

export function listChangeOrders(
  projectId: string,
  filter: { type?: ChangeOrderType | ""; status?: ChangeOrderStatus | "" } = {},
  signal?: AbortSignal,
): Promise<Items<ChangeOrder>> {
  const q = new URLSearchParams({ projectId });
  if (filter.type) q.set("type", filter.type);
  if (filter.status) q.set("status", filter.status);
  return call<Items<ChangeOrder>>(`/change-orders?${q.toString()}`, {}, signal);
}

export function getChangeOrder(id: string, signal?: AbortSignal): Promise<ChangeOrder> {
  return call<ChangeOrder>(`/change-orders/${encodeURIComponent(id)}`, {}, signal);
}

/** Creates a DRAFT change order. Reuse the key when retrying the same request. */
export function createChangeOrder(input: NewChangeOrder, idempotencyKey: string): Promise<ChangeOrder> {
  return call<ChangeOrder>("/change-orders", { method: "POST", headers: { "Idempotency-Key": idempotencyKey }, body: JSON.stringify(input) });
}

/** Submit, approve or reject with the version the user saw; submit and approve may attach the customer approval reference. */
export function decideChangeOrder(id: string, action: Exclude<ChangeOrderAction, "apply">, version: number, customerApprovalRef?: string): Promise<ChangeOrder> {
  return call<ChangeOrder>(`/change-orders/${encodeURIComponent(id)}/${action}`, {
    method: "POST",
    headers: { "If-Match": `"${version}"` },
    body: JSON.stringify(customerApprovalRef && action !== "reject" ? { customerApprovalRef } : {}),
  });
}

/** Applies an APPROVED change order (BR-41.13). A second apply fails with PROJ-CO-001. */
export function applyChangeOrder(id: string, version: number): Promise<ApplyResult> {
  return call<ApplyResult>(`/change-orders/${encodeURIComponent(id)}/apply`, { method: "POST", headers: { "If-Match": `"${version}"` } });
}

// ---- Risk and issue register (FR-41.20) ----

export const RISK_KINDS = ["RISK", "ISSUE"] as const;
export type RiskKind = (typeof RISK_KINDS)[number];

export const RISK_STATUSES = ["OPEN", "MITIGATED", "CLOSED"] as const;
export type RiskStatus = (typeof RISK_STATUSES)[number];

export interface RiskIssue {
  id: string;
  projectId: string;
  kind: RiskKind;
  title: string;
  description: string;
  probability: number;
  impact: number;
  /** probability × impact, 1 to 25. */
  score: number;
  ownerWorkerId?: string;
  mitigation: string;
  dueDate?: string;
  status: RiskStatus;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface RiskInput {
  kind: RiskKind;
  title: string;
  description?: string;
  probability: number;
  impact: number;
  ownerWorkerId?: string;
  mitigation?: string;
  dueDate?: string;
}

/** Status moves per status (mirrors domain.CheckRiskMove); CLOSED is final. */
export const riskMoves: Record<RiskStatus, RiskStatus[]> = {
  OPEN: ["MITIGATED", "CLOSED"],
  MITIGATED: ["OPEN", "CLOSED"],
  CLOSED: [],
};

const risksPath = (projectId: string) => `/${encodeURIComponent(projectId)}/risks`;

export function listRisks(projectId: string, filter: { kind?: RiskKind | ""; status?: RiskStatus | "" } = {}, signal?: AbortSignal): Promise<Items<RiskIssue>> {
  const q = new URLSearchParams();
  if (filter.kind) q.set("kind", filter.kind);
  if (filter.status) q.set("status", filter.status);
  const qs = q.toString();
  return call<Items<RiskIssue>>(`${risksPath(projectId)}${qs ? `?${qs}` : ""}`, {}, signal);
}

/** Adds an OPEN entry. Reuse the key when retrying the same request. */
export function createRisk(projectId: string, input: RiskInput, idempotencyKey: string): Promise<RiskIssue> {
  return call<RiskIssue>(risksPath(projectId), { method: "POST", headers: { "Idempotency-Key": idempotencyKey }, body: JSON.stringify(input) });
}

/** Replaces an entry's fields and status under the version the user saw. */
export function updateRisk(projectId: string, riskId: string, input: RiskInput & { status: RiskStatus }, version: number): Promise<RiskIssue> {
  return call<RiskIssue>(`${risksPath(projectId)}/${encodeURIComponent(riskId)}`, {
    method: "PUT",
    headers: { "If-Match": `"${version}"` },
    body: JSON.stringify(input),
  });
}
