import { apiFetch } from "../../../shell/api/http";
// API client for the costacct service (services/costacct/api/openapi.yaml).
// Browser calls use /api/v1/controlling/... which the web BFF rewrites to the service's /v1/controlling/... paths.

const BASE = "/api/v1/controlling";

export type CostObjectKind = "CostCenter" | "InternalOrder" | "Product" | "Project";
export type CostElementKind = "Primary" | "Secondary";
export type Behavior = "Fixed" | "Variable";

export interface CostObject {
  id: string;
  legalEntityId: string;
  kind: CostObjectKind;
  code: string;
  name: string;
}

export type NewCostObject = Omit<CostObject, "id">;

export interface CostElement {
  id: string;
  legalEntityId: string;
  kind: CostElementKind;
  code: string;
  name: string;
  mainAccountFrom?: string;
  mainAccountTo?: string;
  /** Class of journal cost on this element when no behavior rule applies (FR-38.16). */
  defaultBehavior?: Behavior;
}

export type NewCostElement = Omit<CostElement, "id">;

/** A Fixed / Variable classification of one element from a date, for one cost object kind or every kind (FR-38.16). */
export interface BehaviorRule {
  id: string;
  legalEntityId: string;
  costElementId: string;
  /** Absent for every kind. */
  objectKind?: CostObjectKind;
  behavior: Behavior;
  /** YYYY-MM-DD; applies to periods starting on or after it. */
  effectiveFrom: string;
  createdAt: string;
}

export type NewBehaviorRule = Omit<BehaviorRule, "id" | "createdAt">;

/** CO-0613: a sender with zero basis kept its cost. CO-0615: journal cost arrived after the run; rerun it. */
export interface RunWarning {
  code: "CO-0613" | "CO-0615";
  detail: string;
}

export interface CostEntry {
  id: string;
  costElementId: string;
  sourceId: string;
  allocationRunId?: string;
  /** Present on entries a top-down run posted (FR-38.19). */
  topDownRunId?: string;
  /** Present on entries an internal order settlement posted (FR-38.9). */
  settlementId?: string;
  /** Present on entries a template allocation run posted (FR-38.17). */
  templateRunId?: string;
  /** Present on entries a cost object hierarchy node owns: collections and distributions (FR-38.18). */
  hierarchyNodeId?: string;
  /** Present on entries a hierarchy distribution run posted. */
  hierarchyRunId?: string;
  period: string;
  currency: string;
  amount: string;
  behavior: "Fixed" | "Variable" | "Unclassified";
  origin: "Journal" | "Allocation" | "Activity" | "Statistical" | "Manual" | "HierarchyDistribution";
}

export interface Flow {
  from: string;
  to: string;
  amount: string;
}

export interface Posting {
  centerId: string;
  amount: string;
}

export interface AllocationResult {
  method: "exact-lu" | "gauss-seidel";
  iterations: number;
  residual: string;
  converged: boolean;
  swept: string;
  totalIn: string;
  totalOut: string;
  flows: Flow[];
  postings: Posting[];
  unallocated: string[];
}

export interface RunInput {
  legalEntityId: string;
  period: string;
  currency: string;
  secondaryElementId: string;
  costObjectIds: string[];
  basis: string[][];
}

export interface RunResponse {
  runId: string;
  period: string;
  /** Set when this run reversed the period's completed run (BR-38.7). */
  reversedRunId?: string;
  result: AllocationResult;
  warnings: RunWarning[];
}

export interface Run extends AllocationResult {
  id: string;
  legalEntityId: string;
  period: string;
  currency: string;
  secondaryElementId: string;
  status: "Completed" | "Reversed";
  costObjectIds: string[];
  createdAt: string;
  /** The run this rerun replaced. */
  reversesRunId?: string;
  /** On a Reversed run: the rerun that replaced it. */
  reversedByRunId?: string;
  warnings: RunWarning[];
}

export interface JournalBalance {
  mainAccount: string;
  amount: string;
}

export interface ReconcileInput {
  legalEntityId: string;
  period: string;
  currency: string;
}

export interface Reconciliation {
  reconciled: boolean;
  lines: { costElementId: string; journal: string; costLedger: string; difference: string }[];
  unmappedAccounts: JournalBalance[];
}

export interface TopDownSegment {
  costObjectId: string;
  basis: string;
}

export interface TopDownInput {
  legalEntityId: string;
  period: string;
  referencePeriod?: string;
  currency: string;
  sourceCostObjectId: string;
  costElementId: string;
  amount: string;
  segments: TopDownSegment[];
}

export interface TopDownShare {
  segmentId: string;
  basis: string;
  amount: string;
}

export interface TopDownRun {
  id: string;
  legalEntityId: string;
  period: string;
  referencePeriod?: string;
  currency: string;
  sourceCostObjectId: string;
  costElementId: string;
  amount: string;
  shares: TopDownShare[];
  excluded: string[];
  status: "Completed";
  createdAt: string;
}

export interface SettlementInput {
  period: string;
  currency: string;
  receiverCostObjectId: string;
  secondaryElementId: string;
  amount: string;
}

export interface Settlement extends SettlementInput {
  id: string;
  legalEntityId: string;
  orderId: string;
  collectedBefore: string;
  remaining: string;
  createdAt: string;
}

export interface Problem {
  title: string;
  status: number;
  code: string;
  detail?: string;
}

export class CostAcctApiError extends Error {
  constructor(public readonly problem: Problem) {
    super(problem.detail ?? problem.title);
    this.name = "CostAcctApiError";
  }
}

/** One readable sentence for a failed call: the stable code and the detail. */
export function describeError(err: unknown, fallback: string): string {
  return err instanceof CostAcctApiError ? `${err.problem.code}: ${err.message}` : fallback;
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await apiFetch(path, {
    ...init,
    credentials: "same-origin",
    headers: { accept: "application/json", ...(init.headers as Record<string, string> | undefined) },
  });
  if (!res.ok) {
    const problem = (await res.json().catch(() => ({}))) as Partial<Problem>;
    throw new CostAcctApiError({ title: problem.title ?? `HTTP ${res.status}`, status: res.status, code: problem.code ?? "COM-0000", detail: problem.detail });
  }
  return (await res.json()) as T;
}

function post<T>(path: string, body: unknown, idempotencyKey?: string): Promise<T> {
  const headers: Record<string, string> = { "content-type": "application/json" };
  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
  return request<T>(path, { method: "POST", headers, body: JSON.stringify(body) });
}

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : "";
}

interface Items<T> {
  items: T[];
}

export function listCostObjects(filter: { legalEntityId?: string; kind?: CostObjectKind | "" } = {}, signal?: AbortSignal): Promise<Items<CostObject>> {
  return request(`${BASE}/cost-objects${query({ legalEntityId: filter.legalEntityId, kind: filter.kind || undefined })}`, { signal });
}

export function getCostObject(id: string, signal?: AbortSignal): Promise<CostObject> {
  return request(`${BASE}/cost-objects/${encodeURIComponent(id)}`, { signal });
}

export function createCostObject(input: NewCostObject, idempotencyKey: string): Promise<CostObject> {
  return post(`${BASE}/cost-objects`, input, idempotencyKey);
}

export function listEntries(costObjectId: string, period: string, signal?: AbortSignal): Promise<Items<CostEntry>> {
  return request(`${BASE}/cost-objects/${encodeURIComponent(costObjectId)}/entries${query({ period })}`, { signal });
}

export function listCostElements(legalEntityId?: string, signal?: AbortSignal): Promise<Items<CostElement>> {
  return request(`${BASE}/cost-elements${query({ legalEntityId })}`, { signal });
}

export function createCostElement(input: NewCostElement, idempotencyKey: string): Promise<CostElement> {
  return post(`${BASE}/cost-elements`, input, idempotencyKey);
}

export function listBehaviorRules(filter: { legalEntityId?: string; costElementId?: string } = {}, signal?: AbortSignal): Promise<Items<BehaviorRule>> {
  return request(`${BASE}/cost-behavior-rules${query(filter)}`, { signal });
}

/** Classifies an element's journal cost as Fixed or Variable from a date (FR-38.16). */
export function createBehaviorRule(input: NewBehaviorRule, idempotencyKey: string): Promise<BehaviorRule> {
  return post(`${BASE}/cost-behavior-rules`, input, idempotencyKey);
}

export function listRuns(legalEntityId?: string, signal?: AbortSignal): Promise<Items<Run>> {
  return request(`${BASE}/allocation-runs${query({ legalEntityId })}`, { signal });
}

export function getRun(id: string, signal?: AbortSignal): Promise<Run> {
  return request(`${BASE}/allocation-runs/${encodeURIComponent(id)}`, { signal });
}

/** Runs one reciprocal allocation step for a period (FR-38.7); for a period already allocated it is a rerun that reverses the earlier run (BR-38.7). */
export function runAllocation(input: RunInput, idempotencyKey: string): Promise<RunResponse> {
  return post(`${BASE}/allocation-runs`, input, idempotencyKey);
}

/** Compares primary cost entries with journal balances (FR-38.11). Read-only, so no Idempotency-Key. */
export function reconcile(input: ReconcileInput): Promise<Reconciliation> {
  return post(`${BASE}/reconciliations`, input);
}

export function listTopDownRuns(legalEntityId?: string, signal?: AbortSignal): Promise<Items<TopDownRun>> {
  return request(`${BASE}/top-down-runs${query({ legalEntityId })}`, { signal });
}

export function getTopDownRun(id: string, signal?: AbortSignal): Promise<TopDownRun> {
  return request(`${BASE}/top-down-runs/${encodeURIComponent(id)}`, { signal });
}

/** Distributes an amount booked on an aggregated object to segments by a reference basis (FR-38.19). */
export function runTopDown(input: TopDownInput, idempotencyKey: string): Promise<TopDownRun> {
  return post(`${BASE}/top-down-runs`, input, idempotencyKey);
}

export function listSettlements(orderId: string, period?: string, signal?: AbortSignal): Promise<Items<Settlement>> {
  return request(`${BASE}/internal-orders/${encodeURIComponent(orderId)}/settlements${query({ period })}`, { signal });
}

/** Settles part or all of an internal order's collected cost to a receiver (FR-38.9, BR-38.9). */
export function settleInternalOrder(orderId: string, input: SettlementInput, idempotencyKey: string): Promise<Settlement> {
  return post(`${BASE}/internal-orders/${encodeURIComponent(orderId)}:settle`, input, idempotencyKey);
}

/** A driver quantity per cost object and period; formulas read it as m.<code> (FR-38.17). */
export interface StatisticalMeasure {
  id: string;
  legalEntityId: string;
  code: string;
  name: string;
  unit: string;
}

export type NewStatisticalMeasure = Omit<StatisticalMeasure, "id">;

export interface MeasureValue {
  costObjectId: string;
  value: string;
  updatedAt?: string;
}

export interface MeasureValuesInput {
  legalEntityId: string;
  period: string;
  values: { costObjectId: string; value: string }[];
}

export interface TemplateLine {
  receiverCostObjectId: string;
  secondaryElementId: string;
  formula: string;
}

export interface AllocationTemplate {
  id: string;
  legalEntityId: string;
  code: string;
  name: string;
  currency: string;
  senderCostObjectId: string;
  allowNegative: boolean;
  lines: TemplateLine[];
  version: number;
  updatedAt: string;
}

export interface SaveTemplateInput {
  legalEntityId: string;
  code: string;
  name: string;
  currency: string;
  senderCostObjectId: string;
  allowNegative: boolean;
  lines: TemplateLine[];
  /** 0 for a new code, else the version last read (CST-0018 when stale). */
  expectedVersion: number;
}

export interface TemplateLineResult {
  receiverCostObjectId: string;
  secondaryElementId: string;
  amount: string;
  divByZero: boolean;
}

export interface DryRunResult {
  templateId: string;
  templateVersion: number;
  period: string;
  lines: TemplateLineResult[];
  totalIn: string;
  warnings: string[];
}

export interface TemplateRun {
  id: string;
  legalEntityId: string;
  templateId: string;
  templateVersion: number;
  period: string;
  currency: string;
  senderCostObjectId: string;
  total: string;
  lines: TemplateLineResult[];
  warnings: string[];
  status: "Completed";
  createdAt: string;
}

export function listMeasures(legalEntityId?: string, signal?: AbortSignal): Promise<Items<StatisticalMeasure>> {
  return request(`${BASE}/statistical-measures${query({ legalEntityId })}`, { signal });
}

export function createMeasure(input: NewStatisticalMeasure, idempotencyKey: string): Promise<StatisticalMeasure> {
  return post(`${BASE}/statistical-measures`, input, idempotencyKey);
}

export function listMeasureValues(measureId: string, period: string, signal?: AbortSignal): Promise<Items<MeasureValue>> {
  return request(`${BASE}/statistical-measures/${encodeURIComponent(measureId)}/values${query({ period })}`, { signal });
}

/** Upserts one period's values per cost object. */
export function loadMeasureValues(measureId: string, input: MeasureValuesInput, idempotencyKey: string): Promise<{ measureId: string; period: string; loaded: number }> {
  return post(`${BASE}/statistical-measures/${encodeURIComponent(measureId)}/values`, input, idempotencyKey);
}

export function listTemplates(legalEntityId?: string, signal?: AbortSignal): Promise<Items<AllocationTemplate>> {
  return request(`${BASE}/allocation-templates${query({ legalEntityId })}`, { signal });
}

export function getTemplate(id: string, signal?: AbortSignal): Promise<AllocationTemplate> {
  return request(`${BASE}/allocation-templates/${encodeURIComponent(id)}`, { signal });
}

/** Saves a template; the service parses every formula and checks its references (CO-0621). */
export function saveTemplate(input: SaveTemplateInput, idempotencyKey: string): Promise<AllocationTemplate> {
  return post(`${BASE}/allocation-templates`, input, idempotencyKey);
}

/** Evaluates a template for a period without posting. Read-only, so no Idempotency-Key. */
export function dryRunTemplate(id: string, period: string): Promise<DryRunResult> {
  return post(`${BASE}/allocation-templates/${encodeURIComponent(id)}:dry-run`, { period });
}

/** Runs a template for a period and posts its entries (FR-38.17). */
export function runTemplate(templateId: string, period: string, idempotencyKey: string): Promise<TemplateRun> {
  return post(`${BASE}/template-allocations/runs`, { templateId, period }, idempotencyKey);
}

export function listTemplateRuns(filter: { legalEntityId?: string; templateId?: string } = {}, signal?: AbortSignal): Promise<Items<TemplateRun>> {
  return request(`${BASE}/template-allocations/runs${query(filter)}`, { signal });
}

export function getTemplateRun(id: string, signal?: AbortSignal): Promise<TemplateRun> {
  return request(`${BASE}/template-allocations/runs/${encodeURIComponent(id)}`, { signal });
}

export type HierarchyType = "Reporting" | "Collective";
export type DistributionBasis = "ActualOutput" | "StandardCost" | "FixedPercent";

/** A tree of cost objects (FR-38.18). Only a Collective hierarchy has collecting nodes. */
export interface Hierarchy {
  id: string;
  legalEntityId: string;
  code: string;
  name: string;
  type: HierarchyType;
  nodeCount: number;
  createdAt: string;
  /** Present on a single read, in creation order (parents before their children). */
  nodes?: HierarchyNode[];
}

export type NewHierarchy = Pick<Hierarchy, "legalEntityId" | "code" | "name" | "type">;

export interface HierarchyNode {
  id: string;
  hierarchyId: string;
  /** Absent on a root node. */
  parentNodeId?: string;
  costObjectId: string;
  collects: boolean;
  /** Set on collecting nodes only. */
  distributionBasis?: DistributionBasis;
  createdAt: string;
}

export interface NewHierarchyNode {
  parentNodeId?: string;
  costObjectId: string;
  collects: boolean;
  distributionBasis?: DistributionBasis;
}

/** What a node holds in a period for one element and currency. */
export interface NodeBalance {
  nodeId: string;
  costElementId: string;
  currency: string;
  amount: string;
}

export interface CollectInput {
  period: string;
  currency: string;
  costElementId: string;
  /** Negative to correct an earlier collection (CST-0028 below zero). */
  amount: string;
}

export interface Collection extends CollectInput {
  entryId: string;
  nodeId: string;
  costObjectId: string;
  behavior: "Fixed" | "Variable" | "Unclassified";
  balance: string;
}

export interface DistributionInput {
  nodeId: string;
  period: string;
  currency: string;
  costElementId: string;
  children: { nodeId: string; basis: string }[];
}

/** CO-0645: the node keeps an undistributed residual. */
export interface DistributionWarning {
  code: "CO-0645";
  detail: string;
}

export interface DistributionRun {
  id: string;
  legalEntityId: string;
  hierarchyId: string;
  nodeId: string;
  costElementId: string;
  period: string;
  currency: string;
  basis: DistributionBasis;
  /** What the node held before the run. */
  amount: string;
  distributed: string;
  residual: string;
  shares: { nodeId: string; costObjectId: string; basis: string; amount: string }[];
  warnings: DistributionWarning[];
  status: "Completed";
  createdAt: string;
}

export function listHierarchies(legalEntityId?: string, signal?: AbortSignal): Promise<Items<Hierarchy>> {
  return request(`${BASE}/cost-object-hierarchies${query({ legalEntityId })}`, { signal });
}

export function getHierarchy(id: string, signal?: AbortSignal): Promise<Hierarchy> {
  return request(`${BASE}/cost-object-hierarchies/${encodeURIComponent(id)}`, { signal });
}

export function createHierarchy(input: NewHierarchy, idempotencyKey: string): Promise<Hierarchy> {
  return post(`${BASE}/cost-object-hierarchies`, input, idempotencyKey);
}

export function createHierarchyNode(hierarchyId: string, input: NewHierarchyNode, idempotencyKey: string): Promise<HierarchyNode> {
  return post(`${BASE}/cost-object-hierarchies/${encodeURIComponent(hierarchyId)}/nodes`, input, idempotencyKey);
}

export function listHierarchyBalances(hierarchyId: string, period: string, signal?: AbortSignal): Promise<Items<NodeBalance>> {
  return request(`${BASE}/cost-object-hierarchies/${encodeURIComponent(hierarchyId)}/balances${query({ period })}`, { signal });
}

/** Collects a cost on a collecting node that no lower object can carry (FR-38.18). */
export function collectAtNode(nodeId: string, input: CollectInput, idempotencyKey: string): Promise<Collection> {
  return post(`${BASE}/hierarchy-nodes/${encodeURIComponent(nodeId)}:collect`, input, idempotencyKey);
}

/** Distributes what a collecting node holds to its children at period end (FR-38.18). */
export function runHierarchyDistribution(input: DistributionInput, idempotencyKey: string): Promise<DistributionRun> {
  return post(`${BASE}/hierarchy-distributions`, input, idempotencyKey);
}

export function listDistributionRuns(filter: { legalEntityId?: string; hierarchyId?: string } = {}, signal?: AbortSignal): Promise<Items<DistributionRun>> {
  return request(`${BASE}/hierarchy-distributions${query(filter)}`, { signal });
}

export function getDistributionRun(id: string, signal?: AbortSignal): Promise<DistributionRun> {
  return request(`${BASE}/hierarchy-distributions/${encodeURIComponent(id)}`, { signal });
}
