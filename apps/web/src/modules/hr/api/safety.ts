import { id as seg, newKey, page, post, query, send, type Page } from "./client";

export type IncidentType = "INJURY" | "SKIN_DISORDER" | "RESPIRATORY" | "POISONING" | "HEARING_LOSS" | "OTHER_ILLNESS";
export type IncidentStatus = "REPORTED" | "UNDER_REVIEW" | "CLOSED";
export type IncidentOutcome = "DEATH" | "DAYS_AWAY" | "RESTRICTED" | "OTHER_RECORDABLE" | "NONE";

export const INCIDENT_TYPES: IncidentType[] = ["INJURY", "SKIN_DISORDER", "RESPIRATORY", "POISONING", "HEARING_LOSS", "OTHER_ILLNESS"];
export const BODY_PARTS = [
  "HEAD", "EYE", "EAR", "FACE", "NECK", "SHOULDER", "ARM", "ELBOW", "WRIST", "HAND", "FINGER", "CHEST", "BACK", "ABDOMEN", "HIP", "LEG", "KNEE", "ANKLE", "FOOT", "TOE", "INTERNAL", "MULTIPLE", "OTHER",
] as const;

export interface IncidentDetails {
  description?: string;
  witnesses?: string[];
  treatments?: string[];
  rootCause?: string;
  correctiveAction?: string;
  overrideReason?: string;
}

export interface Incident {
  id: string;
  caseNo: number;
  workerId?: string;
  workerName?: string;
  legalEntityId: string;
  siteId: string;
  incidentType: IncidentType;
  occurredAt: string;
  reportedAt: string;
  location: string;
  bodyParts?: string[];
  death?: boolean;
  lossOfConsciousness?: boolean;
  medicalTreatment?: boolean;
  daysAway?: number;
  daysRestricted?: number;
  oshaRecordable: boolean;
  outcome: IncidentOutcome;
  privacyCase: boolean;
  status: IncidentStatus;
  supersedesId?: string;
  reportedBy: string;
  reviewedBy?: string;
  closedBy?: string;
  closedAt?: string;
  details?: IncidentDetails;
}

export interface NewIncident {
  workerId: string;
  legalEntityId: string;
  siteId: string;
  incidentType: IncidentType;
  occurredAt: string;
  location: string;
  bodyParts: string[];
  death: boolean;
  lossOfConsciousness: boolean;
  medicalTreatment: boolean;
  daysAway: number;
  daysRestricted: number;
  oshaRecordable?: boolean;
  overrideReason?: string;
  privacyCase: boolean;
  supersedesId?: string;
  description: string;
  witnesses: string[];
  treatments: string[];
  rootCause?: string;
  correctiveAction?: string;
}

export interface OshaLine {
  incidentId: string;
  caseNo: number;
  workerId?: string;
  employeeName: string;
  siteId: string;
  occurredOn: string;
  location: string;
  incidentType: string;
  bodyParts: string[];
  outcome: string;
  daysAway: number;
  daysRestricted: number;
  privacyCase: boolean;
  description?: string;
  witnesses?: string[];
  treatments?: string[];
}

export interface OshaSummary {
  deaths: number;
  daysAwayCases: number;
  restrictedCases: number;
  otherRecordableCases: number;
  totalDaysAway: number;
  totalDaysRestricted: number;
  byType: Record<string, number>;
}

export interface OshaExtract {
  year: number;
  siteId?: string;
  log: OshaLine[];
  summary: OshaSummary;
  truncated: boolean;
}

export function listIncidents(filter: { status?: string; cursor?: string }, signal?: AbortSignal): Promise<Page<Incident>> {
  return page<Incident>(`/incidents${query(filter)}`, signal);
}

export function getIncident(incidentId: string, signal?: AbortSignal): Promise<Incident> {
  return send<Incident>(`/incidents/${seg(incidentId)}`, {}, signal);
}

export function reportIncident(input: NewIncident, key: string = newKey()): Promise<Incident> {
  return post<Incident>("/incidents", input, key);
}

export function advanceIncident(incidentId: string, status: "UNDER_REVIEW" | "CLOSED"): Promise<Incident> {
  return send<Incident>(`/incidents/${seg(incidentId)}/status`, { method: "POST", body: JSON.stringify({ status }) });
}

export function oshaExtract(filter: { year: string; siteId?: string }, signal?: AbortSignal): Promise<OshaExtract> {
  return send<OshaExtract>(`/incidents/osha-extract${query(filter)}`, {}, signal);
}

export type LoanStatus = "OUT" | "RETURNED" | "LOST";

export interface LoanedItem {
  id: string;
  workerId: string;
  loanType: string;
  itemDescription: string;
  assetId?: string;
  serial?: string;
  issuedOn: string;
  dueOn?: string;
  closedOn?: string;
  conditionOut?: string;
  conditionIn?: string;
  legalEntityId?: string;
  status: LoanStatus;
}

export interface NewLoanedItem {
  loanType: string;
  itemDescription: string;
  serial?: string;
  issuedOn: string;
  dueOn?: string;
  conditionOut?: string;
  legalEntityId?: string;
}

export async function listWorkerLoans(workerId: string, signal?: AbortSignal): Promise<LoanedItem[]> {
  return (await send<Page<LoanedItem>>(`/workers/${seg(workerId)}/loaned-items`, {}, signal)).items;
}

export async function outstandingLoans(dueBy: string, signal?: AbortSignal): Promise<LoanedItem[]> {
  return (await send<Page<LoanedItem>>(`/loaned-items/outstanding${query({ dueBy })}`, {}, signal)).items;
}

export function issueLoanedItem(workerId: string, input: NewLoanedItem, key: string = newKey()): Promise<LoanedItem> {
  return post<LoanedItem>(`/workers/${seg(workerId)}/loaned-items`, input, key);
}

export function closeLoanedItem(itemId: string, input: { status: "RETURNED" | "LOST"; closedOn: string; conditionIn?: string }): Promise<LoanedItem> {
  return send<LoanedItem>(`/loaned-items/${seg(itemId)}/close`, { method: "POST", body: JSON.stringify(input) });
}

export type ChecklistKind = "ONBOARDING" | "OFFBOARDING" | "TRANSFER";
export type OwnerRole = "MANAGER" | "WORKER" | "HR_PARTNER" | "IT";
export const OWNER_ROLES: OwnerRole[] = ["MANAGER", "WORKER", "HR_PARTNER", "IT"];
export const RETURN_LOANED_ITEMS = "RETURN_LOANED_ITEMS";

export interface TemplateTask {
  code: string;
  title: string;
  ownerRole: OwnerRole;
  dueOffsetDays: number;
  instructions?: string;
  evidenceRequired: boolean;
}

export interface ChecklistTemplate {
  id: string;
  code: string;
  kind: ChecklistKind;
  name: string;
  tasks: TemplateTask[];
}

export interface NewChecklistTemplate {
  code: string;
  kind: ChecklistKind;
  name: string;
  tasks: TemplateTask[];
}

export interface WorkerTask {
  id: string;
  checklistId: string;
  workerId: string;
  taskCode: string;
  title: string;
  ownerRole: string;
  ownerWorkerId?: string;
  dueOn: string;
  evidenceRequired: boolean;
  status: "OPEN" | "DONE" | "SKIPPED";
  evidenceRef?: string;
}

export interface Checklist {
  id: string;
  workerId: string;
  templateId: string;
  kind: ChecklistKind;
  effectiveDate: string;
  status: "OPEN" | "COMPLETED";
  tasks: WorkerTask[];
}

export async function listChecklistTemplates(kind?: string, signal?: AbortSignal): Promise<ChecklistTemplate[]> {
  return (await send<Page<ChecklistTemplate>>(`/checklist-templates${query({ kind })}`, {}, signal)).items;
}

export function createChecklistTemplate(input: NewChecklistTemplate, key: string = newKey()): Promise<ChecklistTemplate> {
  return post<ChecklistTemplate>("/checklist-templates", input, key);
}

export async function listWorkerChecklists(workerId: string, signal?: AbortSignal): Promise<Checklist[]> {
  return (await send<Page<Checklist>>(`/workers/${seg(workerId)}/checklists`, {}, signal)).items;
}

export function startChecklist(workerId: string, input: { templateId: string; effectiveDate: string; legalEntityId?: string }): Promise<Checklist> {
  return send<Checklist>(`/workers/${seg(workerId)}/checklists`, { method: "POST", body: JSON.stringify(input) });
}

export function getChecklist(checklistId: string, signal?: AbortSignal): Promise<Checklist> {
  return send<Checklist>(`/checklists/${seg(checklistId)}`, {}, signal);
}

export function completeChecklist(checklistId: string): Promise<Checklist> {
  return send<Checklist>(`/checklists/${seg(checklistId)}/complete`, { method: "POST" });
}

export async function listOwnedTasks(filter: { ownerWorkerId: string; status?: string }, signal?: AbortSignal): Promise<WorkerTask[]> {
  return (await send<Page<WorkerTask>>(`/tasks${query(filter)}`, {}, signal)).items;
}

export function finishTask(taskId: string, input: { status: "DONE" | "SKIPPED"; evidenceRef?: string }): Promise<WorkerTask> {
  return send<WorkerTask>(`/tasks/${seg(taskId)}/finish`, { method: "POST", body: JSON.stringify(input) });
}
