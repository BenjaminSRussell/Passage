import { id as seg, newKey, page, post, query, send, type Page } from "./client";

export type ActionType = "HIRE" | "TRANSFER" | "TERMINATE" | "COMP_CHANGE";
export type ActionStatus = "PENDING" | "APPLIED" | "WITHDRAWN" | "FAILED";

export interface ActionPayload {
  legalEntityId?: string;
  employmentType?: string;
  assignmentKind?: string;
  positionId?: string;
  fteShare?: string;
  compPlanId?: string;
  amount?: string;
  currency?: string;
  terminationReason?: string;
}

export interface PersonnelAction {
  id: string;
  actionType: ActionType;
  workerId: string;
  employmentId?: string;
  effectiveDate: string;
  status: ActionStatus;
  payload: ActionPayload;
  reason?: string;
  sourceKind?: string;
  sourceId?: string;
  failureCode?: string;
  failureDetail?: string;
  submittedBy: string;
  submittedAt: string;
  appliedAt?: string;
  withdrawnAt?: string;
}

export interface NewPersonnelAction {
  actionType: ActionType;
  workerId: string;
  employmentId?: string;
  effectiveDate: string;
  reason?: string;
  details: ActionPayload;
}

export interface Job {
  id: string;
  code: string;
  title: string;
  jobType: string;
  jobFunction?: string;
  payCurrency?: string;
  payMin?: string;
  payMax?: string;
}

export interface NewJob {
  code: string;
  title: string;
  jobType: string;
  jobFunction?: string;
  payCurrency?: string;
  payMin?: string;
  payMax?: string;
}

export type PayBasis = "ANNUAL" | "HOURLY";

export interface CompensationPlan {
  id: string;
  code: string;
  name: string;
  payBasis: PayBasis;
  currency: string;
}

export type NewCompensationPlan = Omit<CompensationPlan, "id">;

export function listPersonnelActions(
  filter: { status?: string; workerId?: string; cursor?: string },
  signal?: AbortSignal,
): Promise<Page<PersonnelAction>> {
  return page<PersonnelAction>(`/actions${query(filter)}`, signal);
}

export function getPersonnelAction(actionId: string, signal?: AbortSignal): Promise<PersonnelAction> {
  return send<PersonnelAction>(`/actions/${seg(actionId)}`, {}, signal);
}

export function submitPersonnelAction(input: NewPersonnelAction, key: string = newKey()): Promise<PersonnelAction> {
  return post<PersonnelAction>("/actions", input, key);
}

export function withdrawPersonnelAction(actionId: string): Promise<PersonnelAction> {
  return send<PersonnelAction>(`/actions/${seg(actionId)}/withdraw`, { method: "POST" });
}

export function listJobs(cursor?: string, signal?: AbortSignal): Promise<Page<Job>> {
  return page<Job>(`/jobs${query({ cursor })}`, signal);
}

export function getJob(jobId: string, signal?: AbortSignal): Promise<Job> {
  return send<Job>(`/jobs/${seg(jobId)}`, {}, signal);
}

export function createJob(input: NewJob, key: string = newKey()): Promise<Job> {
  return post<Job>("/jobs", input, key);
}

export function listCompensationPlans(cursor?: string, signal?: AbortSignal): Promise<Page<CompensationPlan>> {
  return page<CompensationPlan>(`/compensation/plans${query({ cursor })}`, signal);
}

export function createCompensationPlan(input: NewCompensationPlan, key: string = newKey()): Promise<CompensationPlan> {
  return post<CompensationPlan>("/compensation/plans", input, key);
}
