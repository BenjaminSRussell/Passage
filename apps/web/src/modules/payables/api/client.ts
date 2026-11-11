import { readProblem } from "../../../shell/api/http";
import { OPEN_ITEM_STATUSES, type OpenItem, type OpenItemStatus } from "../../../shell/api/openItems";
import { apiFetch } from "../../../shell/api/http";

export { ApiError } from "../../../shell/api/http";
export { OPEN_ITEM_STATUSES, type OpenItem, type OpenItemStatus };

const BASE = "/api/v1/payables";

export const PROPOSAL_STATUSES = ["DRAFT", "APPROVED", "CANCELLED", "EXECUTING", "EXECUTED", "FAILED"] as const;
export type ProposalStatus = (typeof PROPOSAL_STATUSES)[number];

export const PAYMENT_METHODS = ["TRANSFER", "ACH", "CHECK", "CARD", "WIRE"] as const;
export type PaymentMethod = (typeof PAYMENT_METHODS)[number];

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export interface ProposalSummary {
  id: string;
  runDate: string;
  dueBy: string;
  legalEntityCurrency: string;
  paymentMethod: PaymentMethod;
  status: ProposalStatus;
  createdBy: string;
  approvedBy?: string;
  totalValued: string;
}

export interface ProposalLine {
  openItemId: string;
  openAmount: string;
  discount: string;
  pay: string;
  withheld?: string;
  valued: string;
}

export interface ProposalGroup {
  id: string;
  vendorId: string;
  currency: string;
  paymentMethod: PaymentMethod;
  gross: string;
  discount: string;
  withheld?: string;
  net: string;
  valued: string;
  lines: ProposalLine[];
}

export interface ExcludedItem {
  itemId: string;
  vendorId: string;
  reason: "PAYEE_UNVERIFIED";
}

export interface Proposal extends ProposalSummary {
  legalEntityId: string;
  runId: string;
  createdAt: string;
  approvedAt?: string;
  /** The payment run started from the proposal; present once a run starts and kept when it is paid or failed. */
  paymentRunId?: string;
  totalWithheld?: Record<string, string>;
  excludedItems?: ExcludedItem[];
  groups: ProposalGroup[];
}

export interface OpenItemQuery {
  legalEntityId?: string;
  vendorId?: string;
  dueTo?: string;
  status?: OpenItemStatus | "";
  cursor?: string;
}

export interface ProposalQuery {
  legalEntityId?: string;
  status?: ProposalStatus | "";
  cursor?: string;
}

export interface ProposalInput {
  legalEntityId?: string;
  legalEntityCurrency: string;
  runDate: string;
  runId: string;
  dueBy?: string;
  paymentMethod: PaymentMethod;
  vendorPaymentMethods?: Record<string, PaymentMethod>;
}

function query(params: Record<string, string | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value) search.set(key, value);
  }
  const text = search.toString();
  return text === "" ? "" : `?${text}`;
}

async function send<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await apiFetch(`${BASE}${path}`, {
    method,
    headers: { Accept: "application/json", ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await readProblem(res);
  return (await res.json()) as T;
}

export function listOpenItems(q: OpenItemQuery): Promise<Page<OpenItem>> {
  return send("GET", `/open-items${query({ legal_entity: q.legalEntityId, vendor: q.vendorId, due_to: q.dueTo, status: q.status, limit: "50", cursor: q.cursor })}`);
}

export function getOpenItem(id: string): Promise<OpenItem> {
  return send("GET", `/open-items/${encodeURIComponent(id)}`);
}

export function holdOpenItem(id: string, reason: string): Promise<OpenItem> {
  return send("POST", `/open-items/${encodeURIComponent(id)}:hold`, reason === "" ? {} : { reason });
}

export function releaseOpenItem(id: string): Promise<OpenItem> {
  return send("POST", `/open-items/${encodeURIComponent(id)}:release`, {});
}

/** A vendor whose payments are on hold in one legal entity (the service calls it a vendor block). */
export interface VendorBlock {
  vendorId: string;
  legalEntityId: string;
  reason: string;
  blockedBy?: string;
  blockedAt: string;
}

export function listVendorBlocks(legalEntityId: string | undefined): Promise<{ items: VendorBlock[] }> {
  return send("GET", `/vendor-blocks${query({ legal_entity: legalEntityId })}`);
}

export function blockVendor(vendorId: string, legalEntityId: string | undefined, reason: string): Promise<unknown> {
  return send("POST", `/vendors/${encodeURIComponent(vendorId)}:block`, { ...(legalEntityId ? { legalEntityId } : {}), reason });
}

export function unblockVendor(vendorId: string, legalEntityId: string | undefined): Promise<unknown> {
  return send("POST", `/vendors/${encodeURIComponent(vendorId)}:unblock`, legalEntityId ? { legalEntityId } : {});
}

export function listProposals(q: ProposalQuery): Promise<Page<ProposalSummary>> {
  return send("GET", `/payment-proposals${query({ legal_entity: q.legalEntityId, status: q.status, limit: "50", cursor: q.cursor })}`);
}

export function getProposal(id: string): Promise<Proposal> {
  return send("GET", `/payment-proposals/${encodeURIComponent(id)}`);
}

export function createProposal(input: ProposalInput): Promise<Proposal> {
  return send("POST", "/payment-proposals", input);
}

export function approveProposal(id: string): Promise<Proposal> {
  return send("POST", `/payment-proposals/${encodeURIComponent(id)}:approve`, {});
}

export function cancelProposal(id: string): Promise<Proposal> {
  return send("POST", `/payment-proposals/${encodeURIComponent(id)}:cancel`, {});
}

export const RUN_STATUSES = ["RUNNING", "COMPLETED", "FAILED"] as const;
export type RunStatus = (typeof RUN_STATUSES)[number];

export interface RunPayment {
  id?: string;
  groupId: string;
  vendorId: string;
  currency: string;
  paymentMethod: PaymentMethod;
  endToEndId: string;
  amount: string;
  valuedAmount: string;
}

export interface PaymentRun {
  id: string;
  legalEntityId: string;
  proposalId: string;
  status: RunStatus;
  requestedBy: string;
  fileNumber?: string;
  fileSha256?: string;
  paymentCount?: number;
  fileGeneratedAt?: string;
  journalRequestedAt?: string;
  journalEntryId?: string;
  journalPostedAt?: string;
  settledAt?: string;
  failureReason?: string;
  createdAt: string;
  completedAt?: string;
  payments: RunPayment[];
}

export function startRun(legalEntityId: string | undefined, proposalId: string): Promise<PaymentRun> {
  return send("POST", "/payment-runs", { ...(legalEntityId ? { legalEntityId } : {}), proposalId });
}

export function getRun(id: string): Promise<PaymentRun> {
  return send("GET", `/payment-runs/${encodeURIComponent(id)}`);
}

export type RunStep = "generate-file" | "post-journal" | "settle" | "complete";

export function runStep(id: string, step: RunStep): Promise<PaymentRun> {
  return send("POST", `/payment-runs/${encodeURIComponent(id)}:${step}`, {});
}

export function failRun(id: string, reason: string): Promise<PaymentRun> {
  return send("POST", `/payment-runs/${encodeURIComponent(id)}:fail`, { reason });
}

export async function downloadRunFile(id: string): Promise<Blob> {
  const res = await apiFetch(`${BASE}/payment-runs/${encodeURIComponent(id)}/file`, { headers: { Accept: "application/xml" } });
  if (!res.ok) throw await readProblem(res);
  return res.blob();
}

export interface RemittanceLine {
  openItemId: string;
  invoiceNumber: string;
  invoiceDate: string;
  openAmount: string;
  discount: string;
  withheld: string;
  paid: string;
}

export interface RemittancePayment {
  endToEndId: string;
  currency: string;
  paymentMethod: PaymentMethod;
  amount: string;
  lines: RemittanceLine[];
}

/** What one vendor was paid in a completed payment run: each payment and the invoices it settled. */
export interface RemittanceAdvice {
  runId: string;
  proposalId: string;
  legalEntityId: string;
  vendorId: string;
  paymentDate: string;
  completedAt?: string;
  payments: RemittancePayment[];
}

const remittancePath = (runId: string, vendorId: string) => `/payment-runs/${encodeURIComponent(runId)}/remittances/${encodeURIComponent(vendorId)}`;

export function getRemittance(runId: string, vendorId: string): Promise<RemittanceAdvice> {
  return send("GET", remittancePath(runId, vendorId));
}

export async function downloadRemittanceCsv(runId: string, vendorId: string): Promise<Blob> {
  const res = await apiFetch(`${BASE}${remittancePath(runId, vendorId)}?format=csv`, { headers: { Accept: "text/csv" } });
  if (!res.ok) throw await readProblem(res);
  return res.blob();
}
