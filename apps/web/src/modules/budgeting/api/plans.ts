// Planning half of the budgeting client (FR-38.2): plans, scenarios, the plan grid, spreading and comparison.
// Money stays a decimal string end to end.
import { command, query, request } from "./client";

const BASE = "/api/v1/budget";

export const PLAN_STATUSES = ["Draft", "Submitted", "Reviewed", "Approved"] as const;
export type PlanStatus = (typeof PLAN_STATUSES)[number];
export const SCENARIO_KINDS = ["Baseline", "Optimistic", "Pessimistic", "Custom"] as const;
export type ScenarioKind = (typeof SCENARIO_KINDS)[number];
export type PlanStage = "Review" | "Approve";

export interface Plan {
  id: string;
  legalEntityId: string;
  ruleVersionId: string;
  ledgerId?: string;
  name: string;
  fiscalYear: number;
  currency: string;
  status: PlanStatus;
  submission: number;
  submittedBy?: string;
  approvedScenarioId?: string;
  version: number;
  createdBy: string;
  createdAt: string;
}

export interface PlanScenario {
  id: string;
  name: string;
  kind: ScenarioKind;
  copiedFromId?: string;
  /** The grid ETag: a grid write sends it in If-Match. */
  version: number;
  total: string;
}

export interface StageOwner {
  stage: PlanStage;
  orgUnitId: string;
  owner: string;
  signedBy?: string;
  signedAt?: string;
}

export interface PlanView extends Plan {
  scenarios: PlanScenario[];
  stageOwners: StageOwner[];
}

export interface PlanCell {
  account: string;
  costCenter: string;
  period: string;
  amount: string;
}

export interface PlanGrid {
  scenario: PlanScenario;
  cells: PlanCell[];
  total: string;
  truncated: boolean;
}

export interface GridWritten {
  scenarioId: string;
  version: number;
  cells: number;
  total: string;
  priorYearFallback?: boolean;
}

export interface NewPlanInput {
  legalEntityId: string;
  ruleVersionId: string;
  name: string;
  fiscalYear: number;
}

export interface NewScenarioInput {
  name: string;
  kind: ScenarioKind;
  copyFromScenarioId?: string;
  adjustmentPct?: string;
}

export type SpreadMethod = "even" | "prior-year-actuals" | "custom";

export interface SpreadInput {
  account: string;
  total: string;
  method: SpreadMethod;
  periodWeights?: string[];
  costCenters: { costCenter: string; weight: string }[];
}

export type CompareGroup = "account" | "costCenter" | "period";

export interface ComparisonRow {
  key: string;
  base: string;
  other: string;
  delta: string;
  deltaPct?: string;
}

export interface Comparison {
  groupBy: CompareGroup;
  base: PlanScenario;
  other: PlanScenario;
  rows: ComparisonRow[];
  delta: string;
}

export interface OrgUnit {
  id: string;
  code: string;
  name: string;
}

/** Operating units of a legal entity, for naming the org units that own the review and approval stages. */
export function listOrgUnits(legalEntityId: string, signal?: AbortSignal): Promise<{ items: OrgUnit[] }> {
  return request(`/api/v1/org/operating-units${query({ legalEntityId, limit: "200" })}`, { signal });
}

export function listPlans(legalEntityId?: string, signal?: AbortSignal): Promise<{ items: Plan[] }> {
  return request(`${BASE}/plans${query({ legalEntityId })}`, { signal });
}

export function getPlan(id: string, signal?: AbortSignal): Promise<PlanView> {
  return request(`${BASE}/plans/${encodeURIComponent(id)}`, { signal });
}

export function createPlan(input: NewPlanInput, key: string): Promise<PlanView> {
  return command(`${BASE}/plans`, input, key);
}

export function createScenario(planId: string, input: NewScenarioInput, key: string): Promise<PlanScenario> {
  return command(`${BASE}/plans/${encodeURIComponent(planId)}/scenarios`, input, key);
}

export function setStageOwners(planId: string, owners: StageOwner[], key: string): Promise<PlanView> {
  return request(`${BASE}/plans/${encodeURIComponent(planId)}/stage-owners`, {
    method: "PUT",
    headers: { "content-type": "application/json", "Idempotency-Key": key },
    body: JSON.stringify({ owners: owners.map(({ stage, orgUnitId, owner }) => ({ stage, orgUnitId, owner })) }),
  });
}

export function submitPlan(planId: string, key: string): Promise<PlanView> {
  return command(`${BASE}/plans/${encodeURIComponent(planId)}:submit`, {}, key);
}

export interface GridFilter {
  account?: string;
  costCenter?: string;
  period?: string;
}

export function getGrid(planId: string, scenarioId: string, filter: GridFilter = {}, signal?: AbortSignal): Promise<PlanGrid> {
  return request(`${BASE}/plans/${encodeURIComponent(planId)}/scenarios/${encodeURIComponent(scenarioId)}/grid${query({ ...filter })}`, { signal });
}

export function putGrid(planId: string, scenarioId: string, version: number, cells: PlanCell[], key: string): Promise<GridWritten> {
  return request(`${BASE}/plans/${encodeURIComponent(planId)}/scenarios/${encodeURIComponent(scenarioId)}/grid`, {
    method: "PUT",
    headers: { "content-type": "application/json", "If-Match": `"${version}"`, "Idempotency-Key": key },
    body: JSON.stringify({ cells }),
  });
}

export function spreadGrid(planId: string, scenarioId: string, version: number, input: SpreadInput, key: string): Promise<GridWritten> {
  return request(`${BASE}/plans/${encodeURIComponent(planId)}/scenarios/${encodeURIComponent(scenarioId)}:spread`, {
    method: "POST",
    headers: { "content-type": "application/json", "If-Match": `"${version}"`, "Idempotency-Key": key },
    body: JSON.stringify(input),
  });
}

export function compareScenarios(planId: string, base: string, other: string, groupBy: CompareGroup, signal?: AbortSignal): Promise<Comparison> {
  return request(`${BASE}/plans/${encodeURIComponent(planId)}/scenarios:compare${query({ base, other, groupBy })}`, { signal });
}
