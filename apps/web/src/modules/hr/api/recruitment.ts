import { id as seg, newKey, page, post, query, send, type Page } from "./client";
import type { PersonnelAction } from "./personnel";

export type RequisitionStatus = "DRAFT" | "APPROVED" | "OPEN" | "FILLED" | "CANCELED";
export type RequisitionMove = "APPROVED" | "OPEN" | "CANCELED";
export type CandidateStage = "APPLIED" | "SCREEN" | "INTERVIEW" | "OFFER" | "HIRED" | "REJECTED";
export type CandidateMove = "SCREEN" | "INTERVIEW" | "OFFER" | "REJECTED";
export type OfferStatus = "DRAFT" | "SENT" | "ACCEPTED" | "DECLINED" | "WITHDRAWN";
export type OfferMove = "SENT" | "ACCEPTED" | "DECLINED" | "WITHDRAWN";

export interface Requisition {
  id: string;
  requisitionNumber: string;
  positionId: string;
  legalEntityId: string;
  headcount: number;
  hiringManagerWorkerId: string;
  plannedStartDate: string;
  allowOverfill: boolean;
  status: RequisitionStatus;
  filled: number;
  createdAt: string;
}

export interface NewRequisition {
  requisitionNumber: string;
  positionId: string;
  headcount: number;
  hiringManagerWorkerId: string;
  plannedStartDate: string;
  allowOverfill: boolean;
}

export interface Candidate {
  id: string;
  requisitionId: string;
  stage: CandidateStage;
  source?: string;
  fullName?: string;
  email?: string;
  phone?: string;
  erased: boolean;
  stageChangedAt: string;
}

export interface NewCandidate {
  fullName: string;
  email?: string;
  phone?: string;
  source?: string;
  consentGranted: boolean;
}

export interface Offer {
  id: string;
  candidateId: string;
  requisitionId: string;
  amount: string;
  currency: string;
  startDate: string;
  status: OfferStatus;
  hireActionId?: string;
}

export interface NewOffer {
  amount: string;
  currency: string;
  startDate: string;
}

/** The POST /v1/hr/workers body, created in the hire transaction; legalName defaults to the candidate's name. */
export interface NewWorkerOnHire {
  personnelNumber: string;
  workerType: string;
  partyPersonId: string;
  nationalId: string;
  legalName?: string;
}

/** Give workerId for an existing worker, or newWorker (needs HcmWorkerMaintain) to create one. */
export interface HireFromOffer {
  workerId?: string;
  newWorker?: NewWorkerOnHire;
  legalEntityId: string;
  employmentType: string;
  fteShare?: string;
  compPlanId?: string;
}

export function listRequisitions(filter: { status?: string; cursor?: string }, signal?: AbortSignal): Promise<Page<Requisition>> {
  return page<Requisition>(`/requisitions${query(filter)}`, signal);
}

export function getRequisition(requisitionId: string, signal?: AbortSignal): Promise<Requisition> {
  return send<Requisition>(`/requisitions/${seg(requisitionId)}`, {}, signal);
}

export function createRequisition(input: NewRequisition, key: string = newKey()): Promise<Requisition> {
  return post<Requisition>("/requisitions", input, key);
}

function move<T>(path: string, to: string): Promise<T> {
  return send<T>(path, { method: "POST", body: JSON.stringify({ to }) });
}

export function moveRequisition(requisitionId: string, to: RequisitionMove): Promise<Requisition> {
  return move<Requisition>(`/requisitions/${seg(requisitionId)}/transition`, to);
}

export function listCandidates(requisitionId: string, filter: { stage?: string; cursor?: string }, signal?: AbortSignal): Promise<Page<Candidate>> {
  return page<Candidate>(`/requisitions/${seg(requisitionId)}/candidates${query(filter)}`, signal);
}

export function createCandidate(requisitionId: string, input: NewCandidate, key: string = newKey()): Promise<Candidate> {
  return post<Candidate>(`/requisitions/${seg(requisitionId)}/candidates`, input, key);
}

export function getCandidate(candidateId: string, signal?: AbortSignal): Promise<Candidate> {
  return send<Candidate>(`/candidates/${seg(candidateId)}`, {}, signal);
}

export function moveCandidate(candidateId: string, to: CandidateMove): Promise<Candidate> {
  return move<Candidate>(`/candidates/${seg(candidateId)}/move`, to);
}

export function createOffer(candidateId: string, input: NewOffer, key: string = newKey()): Promise<Offer> {
  return post<Offer>(`/candidates/${seg(candidateId)}/offers`, input, key);
}

export function getOffer(offerId: string, signal?: AbortSignal): Promise<Offer> {
  return send<Offer>(`/offers/${seg(offerId)}`, {}, signal);
}

export function moveOffer(offerId: string, to: OfferMove): Promise<Offer> {
  return move<Offer>(`/offers/${seg(offerId)}/transition`, to);
}

export function hireFromOffer(offerId: string, input: HireFromOffer): Promise<PersonnelAction> {
  return send(`/offers/${seg(offerId)}/hire`, { method: "POST", body: JSON.stringify(input) });
}

export type CycleKind = "MERIT" | "BONUS" | "BOTH";
export type CycleStatus = "PLANNING" | "MANAGER_INPUT" | "APPROVAL" | "APPLIED" | "CANCELED";
export type CycleMove = "MANAGER_INPUT" | "APPROVAL" | "CANCELED";

export interface GuidelineRow {
  rating: number;
  compaMin: string;
  compaMax: string;
  pctMin: string;
  pctMax: string;
}

export interface CycleBudget {
  departmentId: string;
  budgetAmount: string;
}

export interface CompCycle {
  id: string;
  name: string;
  kind: CycleKind;
  effectiveDate: string;
  currency: string;
  allowOverBudget: boolean;
  guidelineMatrix: GuidelineRow[];
  budgets: CycleBudget[];
  status: CycleStatus;
  createdBy?: string;
  approvedBy?: string;
  approvedAt?: string;
  appliedAt?: string;
  actionIds?: string[];
}

export interface NewCompCycle {
  name: string;
  kind: CycleKind;
  effectiveDate: string;
  currency: string;
  allowOverBudget: boolean;
  guidelineMatrix: GuidelineRow[];
  budgets: CycleBudget[];
}

export interface WorksheetRow {
  workerId: string;
  managerWorkerId: string;
  employmentId: string;
  departmentId: string;
  currentPay: string;
  rating: number;
  compaRatio?: string;
  guidelinePctMin?: string;
  guidelinePctMax?: string;
  proposedPct: string;
  proposedAmount: string;
  justification?: string;
  status: string;
  actionId?: string;
}

export interface WorksheetInput {
  workerId: string;
  rating: number;
  proposedPct: string;
  justification?: string;
}

export function listCompCycles(cursor?: string, signal?: AbortSignal): Promise<Page<CompCycle>> {
  return page<CompCycle>(`/compensation/cycles${query({ cursor })}`, signal);
}

export function getCompCycle(cycleId: string, signal?: AbortSignal): Promise<CompCycle> {
  return send<CompCycle>(`/compensation/cycles/${seg(cycleId)}`, {}, signal);
}

export function createCompCycle(input: NewCompCycle, key: string = newKey()): Promise<CompCycle> {
  return post<CompCycle>("/compensation/cycles", input, key);
}

export function advanceCompCycle(cycleId: string, to: CycleMove): Promise<CompCycle> {
  return move<CompCycle>(`/compensation/cycles/${seg(cycleId)}/advance`, to);
}

/** Segregated approval: the approver did not create the cycle, submit a worksheet or appear on a row (HCM-CMP-SOD). */
export function approveCompCycle(cycleId: string): Promise<CompCycle> {
  return send<CompCycle>(`/compensation/cycles/${seg(cycleId)}/approve`, { method: "POST" });
}

export function applyCompCycle(cycleId: string): Promise<CompCycle> {
  return send<CompCycle>(`/compensation/cycles/${seg(cycleId)}/apply`, { method: "POST" });
}

export async function getWorksheet(cycleId: string, managerId: string, signal?: AbortSignal): Promise<WorksheetRow[]> {
  return (await page<WorksheetRow>(`/compensation/cycles/${seg(cycleId)}/worksheets/${seg(managerId)}`, signal)).items;
}

export async function saveWorksheet(cycleId: string, managerId: string, rows: WorksheetInput[]): Promise<WorksheetRow[]> {
  const out = await send<Page<WorksheetRow>>(`/compensation/cycles/${seg(cycleId)}/worksheets/${seg(managerId)}`, {
    method: "PUT",
    body: JSON.stringify({ rows }),
  });
  return out.items;
}

export async function submitWorksheet(cycleId: string, managerId: string): Promise<WorksheetRow[]> {
  const out = await send<Page<WorksheetRow>>(`/compensation/cycles/${seg(cycleId)}/worksheets/${seg(managerId)}/submit`, { method: "POST" });
  return out.items;
}
