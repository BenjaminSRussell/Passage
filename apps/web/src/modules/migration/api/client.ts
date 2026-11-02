// Client for the migration service (services/migration/api/openapi.yaml). Commands send an
// Idempotency-Key so a retried submit replays the first outcome.
export interface Problem {
  code: string;
  message: string;
  /** Field or test name to reason, from the problem's errors map. */
  errors?: Record<string, string>;
}

export class MigrationApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly errors?: Record<string, string>,
  ) {
    super(message);
    this.name = "MigrationApiError";
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof MigrationApiError) return { code: error.code, message: error.message, errors: error.errors };
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed" };
}

export type LegacySourceSystem = "ax2012" | "d365fo" | "sap_ecc" | "sap_s4";
/** A legacy system, or custom_<name> for a customer's own ERP. */
export type SourceSystem = LegacySourceSystem | `custom_${string}`;
export type RunMode = "rehearsal" | "cutover" | "delta";
export type RunStatus = "succeeded" | "partial" | "failed";

export interface RowIssue {
  field: string;
  code: string;
  detail: string;
}

export interface MigrationRun {
  id: string;
  sourceSystem: SourceSystem;
  mode: RunMode;
  entity: string;
  status: RunStatus;
  dryRun: boolean;
  mappingVersion: number;
  mappingSpecId?: string;
  dataTemplateId?: string;
  /** The migrate project the run belongs to, when it was started under one. */
  projectId?: string;
  /** The entity binding a scheduled delta sync run read. */
  bindingId?: string;
  /** The largest change cursor the run read. */
  watermark?: string;
  parentRunId?: string;
  counts: { extracted: number; mapped: number; created: number; updated: number; unchanged: number; templated?: number; conflicted?: number; rejected: number; warned: number };
  rejects: { sourceKey: string; rejects: RowIssue[] }[];
  warnings: { sourceKey: string; warnings: RowIssue[] }[];
  absent: string[];
  reconciliation?: { source: number; target: number; checks: { name: string; ok: boolean; detail: string }[] };
  createdAt: string;
}

export interface MappingSpec {
  id: string;
  entity: string;
  version: number;
  checksum: string;
  fieldCount: number;
  testCount: number;
  createdAt: string;
  mapping?: unknown;
}

export interface DataTemplate {
  id: string;
  entity: string;
  name: string;
  version: number;
  checksum: string;
  defaultCount: number;
  createdAt: string;
  /** Present on the single-template read and on publish only. */
  defaults?: Record<string, string>;
}

export interface PublishTemplateInput {
  name: string;
  entity: string;
  version: number;
  defaults: Record<string, string>;
}

export interface LedgerRow {
  sourceKey: string;
  targetId?: string;
  action: "created" | "updated" | "unchanged";
  defaultFromTemplate: string[];
}

export interface CreateRunInput {
  sourceSystem: SourceSystem;
  mode: RunMode;
  dryRun: boolean;
  mappingSpecId: string;
  /** Omitted when no data template applies. */
  dataTemplateId?: string;
  /** Omitted for a run outside any project; a project allows one active run at a time (BR-10.2). */
  projectId?: string;
}

export function newKey(): string {
  return crypto.randomUUID();
}

async function toError(res: Response): Promise<MigrationApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message = typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const errors = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : undefined;
  return new MigrationApiError(res.status, code, message, errors);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`/api/v1/integration${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new MigrationApiError(0, "COM-0001", "The response has no items");
  return body.items;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

async function page<T>(path: string, signal?: AbortSignal): Promise<Page<T>> {
  const body = await send<Page<T>>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new MigrationApiError(0, "COM-0001", "The response has no items");
  return body;
}

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : "";
}

export const listRunsPage = (filter: { entity?: string; status?: string; projectId?: string; bindingId?: string; cursor?: string; limit?: string }, signal?: AbortSignal) =>
  page<MigrationRun>(`/migration-runs${query(filter)}`, signal);

export const getRun = (id: string, signal?: AbortSignal) => send<MigrationRun>(`/migration-runs/${encodeURIComponent(id)}`, {}, signal);

export async function createRun(input: CreateRunInput, key: string = newKey()): Promise<MigrationRun> {
  const body = await send<{ run: MigrationRun }>("/migration-runs", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });
  return body.run;
}

export async function rerunRun(id: string, key: string = newKey()): Promise<MigrationRun> {
  const body = await send<{ run: MigrationRun }>(`/migration-runs/${encodeURIComponent(id)}:rerun`, { method: "POST", headers: { "Idempotency-Key": key } });
  return body.run;
}

/** The largest page the service serves; pickers ask for it and say when more exist. */
export const PICKER_LIMIT = 200;

export interface ListFilter {
  entity?: string;
  cursor?: string;
  limit?: number;
}

const listQuery = ({ limit, ...rest }: ListFilter) => query({ ...rest, limit: limit ? String(limit) : undefined });

export const listSpecsPage = (filter: ListFilter, signal?: AbortSignal) => page<MappingSpec>(`/mapping-specs${listQuery(filter)}`, signal);

export const getSpec = (id: string, signal?: AbortSignal) => send<MappingSpec>(`/mapping-specs/${encodeURIComponent(id)}`, {}, signal);

export const publishSpec = (mapping: unknown, key: string = newKey()) =>
  send<MappingSpec>("/mapping-specs", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify({ mapping }) });

export const listTemplatesPage = (filter: ListFilter, signal?: AbortSignal) => page<DataTemplate>(`/data-templates${listQuery(filter)}`, signal);

export const getTemplate = (id: string, signal?: AbortSignal) => send<DataTemplate>(`/data-templates/${encodeURIComponent(id)}`, {}, signal);

export const publishTemplate = (input: PublishTemplateInput, key: string = newKey()) =>
  send<DataTemplate>("/data-templates", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });

/** Lineage rows of a run; templatedOnly keeps the rows that took a data template default. */
export const listLedgerPage = (runId: string, templatedOnly: boolean, cursor?: string, signal?: AbortSignal) =>
  page<LedgerRow>(`/migration-runs/${encodeURIComponent(runId)}/ledger${query({ templated: templatedOnly ? "true" : undefined, limit: "200", cursor })}`, signal);

export interface PackageCounts {
  create: number;
  update: number;
  unchanged: number;
}

export interface ManifestFile {
  name: string;
  sha256: string;
  dependsOn: string[];
}

export interface ConfigPackage {
  id: string;
  version: string;
  schemaVersion: number;
  fileCount: number;
  files?: ManifestFile[];
  applied: { tenantId: string; appliedAt: string; idempotencyKey?: string; diffCounts: PackageCounts }[];
  createdBy: string;
  createdAt: string;
}

export interface PackageDiff {
  version: string;
  schemaVersion: number;
  counts: PackageCounts;
  files: { name: string; entity: string; create: string[]; update: string[]; unchanged: string[] }[];
}

export interface PackageApplied {
  packageId: string;
  version: string;
  schemaVersion: number;
  appliedAt: string;
  counts: PackageCounts;
}

export const listPackages = (signal?: AbortSignal) => items<ConfigPackage>("/config-packages", signal);

export const getPackage = (id: string, signal?: AbortSignal) => send<ConfigPackage>(`/config-packages/${encodeURIComponent(id)}`, {}, signal);

export const diffPackage = (pkg: unknown, signature: string) =>
  send<PackageDiff>("/config-packages:diff", { method: "POST", body: JSON.stringify({ package: pkg, signature }) });

export const applyPackage = (pkg: unknown, signature: string, key: string = newKey()) =>
  send<PackageApplied>("/config-packages:apply", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify({ package: pkg, signature }) });

export interface XrefEntry {
  sourceSystem: SourceSystem;
  entity: string;
  sourceKey: string;
  targetId: string;
  contentHash?: string;
  /** Set when the legacy record was merged into another Passage record. */
  mergedIntoTargetId?: string;
  updatedAt: string;
}

export interface XrefCount {
  sourceSystem: SourceSystem;
  entity: string;
  count: number;
}

export interface XrefFilter {
  sourceSystem?: string;
  entity?: string;
  sourceKey?: string;
  keyPrefix?: string;
  targetId?: string;
  cursor?: string;
  limit?: number;
}

export interface XrefPage {
  items: XrefEntry[];
  nextCursor?: string;
}

export async function listXref(filter: XrefFilter, signal?: AbortSignal): Promise<XrefPage> {
  const { limit, ...rest } = filter;
  const body = await send<XrefPage>(`/xref${query({ ...rest, limit: limit ? String(limit) : undefined })}`, {}, signal);
  if (!Array.isArray(body.items)) throw new MigrationApiError(0, "COM-0001", "The response has no items");
  return body;
}

export const xrefSummary = (signal?: AbortSignal) => items<XrefCount>("/xref/summary", signal);

export type ProjectType = "migrate" | "copy" | "config_package";
export type ProjectStatus = "active" | "archived";

export interface MigrationProject {
  id: string;
  name: string;
  projectType: ProjectType;
  sourceSystem?: SourceSystem;
  sourceLegalEntityId?: string;
  targetLegalEntityId?: string;
  /** The connection profile a migrate project reads from. */
  connectionProfileId?: string;
  status: ProjectStatus;
  cutoverAt?: string;
  scheduleCron?: string;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface CreateProjectInput {
  name: string;
  projectType: ProjectType;
  sourceSystem?: SourceSystem;
  sourceLegalEntityId?: string;
  targetLegalEntityId?: string;
  connectionProfileId?: string;
  cutoverAt?: string;
  scheduleCron?: string;
}

export interface KeyRewrite {
  field: string;
  from: string;
  to: string;
}

export interface KeyRewriteRules {
  /** The project version to send back with the next replacement. */
  version: number;
  items: KeyRewrite[];
}

export interface KeyRewritePreview {
  key: string;
  rewritten: string;
  changed: boolean;
}

export interface ProjectPage {
  items: MigrationProject[];
  nextCursor?: string;
}

export async function listProjects(filter: { projectType?: string; status?: string; cursor?: string; limit?: number }, signal?: AbortSignal): Promise<ProjectPage> {
  const { limit = 50, ...rest } = filter;
  const body = await send<ProjectPage>(`/projects${query({ ...rest, limit: String(limit) })}`, {}, signal);
  if (!Array.isArray(body.items)) throw new MigrationApiError(0, "COM-0001", "The response has no items");
  return body;
}

export const getProject = (id: string, signal?: AbortSignal) => send<MigrationProject>(`/projects/${encodeURIComponent(id)}`, {}, signal);

export const createProject = (input: CreateProjectInput, key: string = newKey()) =>
  send<MigrationProject>("/projects", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });

/** The editable fields of a project; an omitted field is unchanged and an empty cutoverAt or scheduleCron clears it. */
export interface ProjectEdit {
  name?: string;
  status?: ProjectStatus;
  cutoverAt?: string;
  scheduleCron?: string;
}

export const updateProject = (id: string, version: number, edit: ProjectEdit) =>
  send<MigrationProject>(`/projects/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ version, ...edit }) });

export const getKeyRewriteRules = (id: string, signal?: AbortSignal) => send<KeyRewriteRules>(`/projects/${encodeURIComponent(id)}/key-rewrite-rules`, {}, signal);

export const replaceKeyRewriteRules = (id: string, version: number, rules: KeyRewrite[]) =>
  send<KeyRewriteRules>(`/projects/${encodeURIComponent(id)}/key-rewrite-rules`, { method: "PUT", body: JSON.stringify({ version, rules }) });

/** Shows what the given rules would do to sample keys; nothing is saved. */
export async function previewKeyRewrite(id: string, keys: string[], rules: KeyRewrite[]): Promise<KeyRewritePreview[]> {
  const body = await send<{ items: KeyRewritePreview[] }>(`/projects/${encodeURIComponent(id)}/key-rewrite-rules:preview`, {
    method: "POST",
    body: JSON.stringify({ keys, rules }),
  });
  return body.items;
}

export type ConnectionKind = "sql" | "rest" | "file";
export type ConnectionStatus = "active" | "disabled";

export interface ConnectionProfile {
  id: string;
  name: string;
  kind: ConnectionKind;
  sourceSystem: SourceSystem;
  /** Non-secret settings only. */
  config: Record<string, string | number>;
  /** Whether a secret is stored. The secret itself is never returned. */
  secretSet: boolean;
  secretUpdatedAt?: string;
  status: ConnectionStatus;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface CreateConnectionInput {
  name: string;
  kind: ConnectionKind;
  sourceSystem: SourceSystem;
  config: Record<string, string | number>;
}

export interface ConnectionEdit {
  name?: string;
  status?: ConnectionStatus;
  config?: Record<string, string | number>;
}

export interface ConnectionTestResult {
  ok: boolean;
  kind: ConnectionKind;
  durationMs: number;
  failure?: { code: string; message: string };
  serverVersion?: string;
  schemas?: string[];
  tables?: { schema: string; name: string; type: string; columns?: { name: string; type: string; nullable: boolean }[] }[];
  httpStatus?: number;
  fileCount?: number;
  files?: { name: string; bytes: number }[];
  truncated?: boolean;
}

export const listConnections = (filter: { kind?: string; status?: string; cursor?: string; limit?: number }, signal?: AbortSignal) => {
  const { limit = 50, ...rest } = filter;
  return page<ConnectionProfile>(`/connection-profiles${query({ ...rest, limit: String(limit) })}`, signal);
};

export const getConnection = (id: string, signal?: AbortSignal) => send<ConnectionProfile>(`/connection-profiles/${encodeURIComponent(id)}`, {}, signal);

export const createConnection = (input: CreateConnectionInput, key: string = newKey()) =>
  send<ConnectionProfile>("/connection-profiles", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });

export const updateConnection = (id: string, version: number, edit: ConnectionEdit) =>
  send<ConnectionProfile>(`/connection-profiles/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ version, ...edit }) });

/** Sets the profile's one secret. Write-only: the answer is the profile, which says only that a secret is set. */
export const setConnectionSecret = (id: string, secret: string) =>
  send<ConnectionProfile>(`/connection-profiles/${encodeURIComponent(id)}/secret`, { method: "PUT", body: JSON.stringify({ secret }) });

export const testConnection = (id: string) => send<ConnectionTestResult>(`/connection-profiles/${encodeURIComponent(id)}:test`, { method: "POST" });

export type CursorType = "timestamp" | "int" | "rowversion";
export type BindingSource = Record<string, unknown>;

/** Which table, query, endpoint or file of a connection feeds which Passage entity. */
export interface EntityBinding {
  id: string;
  connectionProfileId: string;
  entity: string;
  source: BindingSource;
  keyFields: string[];
  cursorField?: string;
  cursorType?: CursorType;
  mappingSpecId?: string;
  status: ConnectionStatus;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface BindingInput {
  connectionProfileId: string;
  entity: string;
  source: BindingSource;
  keyFields: string[];
  cursorField?: string;
  cursorType?: CursorType;
  mappingSpecId?: string;
}

export interface BindingEdit {
  source?: BindingSource;
  keyFields?: string[];
  cursorField?: string;
  cursorType?: CursorType | "";
  mappingSpecId?: string;
  status?: ConnectionStatus;
}

export interface PreviewOptions {
  limit?: number;
  mappingSpecId?: string;
  /** A file connection can be previewed from an upload instead of the drop directory. */
  upload?: { name: string; contentBase64: string };
}

export interface PreviewRow {
  index: number;
  sourceKey?: string;
  source: Record<string, unknown>;
  target?: Record<string, unknown>;
  warnings?: RowIssue[];
  rejects?: RowIssue[];
}

export interface BindingPreview {
  ok: boolean;
  durationMs: number;
  failure?: { code: string; message: string };
  mapping?: { entity: string; version: number; mappingSpecId?: string };
  rows?: PreviewRow[];
  columns?: string[];
  more?: boolean;
  mapped?: number;
  rejected?: number;
  duplicateKeys?: string[];
  watermark?: string;
}

export const listBindings = (filter: { connectionProfileId?: string; entity?: string; status?: string; cursor?: string; limit?: number }, signal?: AbortSignal) => {
  const { limit = 50, ...rest } = filter;
  return page<EntityBinding>(`/entity-bindings${query({ ...rest, limit: String(limit) })}`, signal);
};

export const getBinding = (id: string, signal?: AbortSignal) => send<EntityBinding>(`/entity-bindings/${encodeURIComponent(id)}`, {}, signal);

export const createBinding = (input: BindingInput, key: string = newKey()) =>
  send<EntityBinding>("/entity-bindings", { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) });

export const updateBinding = (id: string, version: number, edit: BindingEdit) =>
  send<EntityBinding>(`/entity-bindings/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ version, ...edit }) });

/** Extracts a few rows through the stored binding and runs its mapping; nothing is loaded. */
export const previewBinding = (id: string, options: PreviewOptions) =>
  send<BindingPreview>(`/entity-bindings/${encodeURIComponent(id)}:preview`, { method: "POST", body: JSON.stringify(options) });

/** The same for a binding that is not saved yet. */
export const previewDraftBinding = (input: BindingInput, options: PreviewOptions) =>
  send<BindingPreview>("/entity-bindings:preview", { method: "POST", body: JSON.stringify({ ...input, ...options }) });

// ---- Delta sync (integration track S4) ----

export type SourceOfTruth = "legacy" | "passage";
export type SyncRunStatus = "succeeded" | "partial" | "failed" | "abandoned";

export interface SyncStatus {
  bindingId: string;
  entity: string;
  connectionProfileId: string;
  connectionProfileName: string;
  bindingStatus: "active" | "disabled";
  projectId?: string;
  /** Empty when no active migrate project on the profile has a schedule. */
  scheduleCron?: string;
  sourceOfTruth: SourceOfTruth;
  paused: boolean;
  watermark: string;
  nextDueAt?: string;
  running: boolean;
  lastRunId?: string;
  lastRunAt?: string;
  lastSuccessAt?: string;
  lastStatus?: SyncRunStatus;
  lastError?: string;
  consecutiveFailures: number;
  rowsLoaded: number;
  rowsRejected: number;
  openConflicts: number;
  version: number;
}

export type ConflictStatus = "open" | "kept" | "accepted";
export type ConflictResolution = "accept_legacy" | "keep_passage";

export interface SyncConflict {
  id: string;
  bindingId: string;
  entity: string;
  runId?: string;
  sourceKey: string;
  targetId?: string;
  legacyFields: Record<string, unknown>;
  status: ConflictStatus;
  detectedAt: string;
  resolvedAt?: string;
  resolvedBy?: string;
  /** Bumped when a run holds different legacy values for the row; a resolution names the version it was decided on. */
  version: number;
}

export const listSync = (filter: { paused?: string; failing?: string; cursor?: string; limit?: string }, signal?: AbortSignal) =>
  page<SyncStatus>(`/sync-status${query(filter)}`, signal);

export const getSync = (bindingId: string, signal?: AbortSignal) => send<SyncStatus>(`/sync-status/${encodeURIComponent(bindingId)}`, {}, signal);

/** Pauses or resumes a schedule, sets the source of truth or makes the binding due now, under the status's version. */
export const updateSync = (bindingId: string, version: number, edit: { paused?: boolean; sourceOfTruth?: SourceOfTruth; runNow?: boolean }) =>
  send<SyncStatus>(`/sync-status/${encodeURIComponent(bindingId)}`, { method: "PATCH", body: JSON.stringify({ version, ...edit }) });

export const listConflicts = (filter: { bindingId?: string; status?: string; cursor?: string; limit?: string }, signal?: AbortSignal) =>
  page<SyncConflict>(`/sync-conflicts${query(filter)}`, signal);

/** Resolves a conflict as reviewed at `version`; a run that replaced the held values since answers 409 MIG-VERSION-CONFLICT. */
export const resolveConflict = (id: string, version: number, resolution: ConflictResolution) =>
  send<SyncConflict>(`/sync-conflicts/${encodeURIComponent(id)}:resolve`, { method: "POST", body: JSON.stringify({ resolution, version }) });
