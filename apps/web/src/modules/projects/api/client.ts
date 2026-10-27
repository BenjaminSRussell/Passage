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
