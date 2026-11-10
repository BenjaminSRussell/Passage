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
