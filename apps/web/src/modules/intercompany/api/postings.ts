// Client for posted IC journals and charges (FR-40.5, FR-40.13) and stored
// reconciliation runs (FR-40.6) in services/intercompany/api/openapi.yaml.
// Amounts are decimal strings; the browser never does arithmetic on a float.
import { callJson, type ChargePreviewRequest, type JournalPreviewRequest, type ReconItem as ClassifyItem, type ReconStatus } from "./client";

/** The legal entity and ledger one entity code posts to. */
export interface EntityTarget {
  entity: string;
  legalEntityId: string;
  ledgerId: string;
}

export type PostingStatus = "pending" | "posted" | "compensated";

export interface PostedLeg {
  account: string;
  side: "debit" | "credit";
  amount: string;
}

export interface PostedEntry {
  entity: string;
  legalEntityId: string;
  ledgerId: string;
  currency: string;
  legs: PostedLeg[];
  ledgerEntryId?: string;
  reversalEntryId?: string;
}

export interface PostedJournal {
  id: string;
  pairId: string;
  source: "journal" | "charge";
  sourceRef: string;
  postingDate: string;
  status: PostingStatus;
  failureCode?: string;
  failureDetail?: string;
  entries: PostedEntry[];
  createdBy: string;
  createdAt: string;
}

export type ChargeStatus = "pending" | "posted" | "compensated" | "suspense";

export interface ChargeRecord {
  id: string;
  sourceId: string;
  version: number;
  lendingEntity: string;
  borrowingEntity: string;
  category: string;
  resourceRole?: string;
  transactionDate: string;
  quantity: string;
  unitCost: string;
  currency: string;
  status: ChargeStatus;
  transferPriceMethod?: string;
  cost?: string;
  price?: string;
  margin?: string;
  journalId?: string;
  failureCode?: string;
  createdBy: string;
  createdAt: string;
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : "";
}

/** Post one IC line to both entities' ledgers. Send the same key again to retry or resume. */
export async function postJournal(req: JournalPreviewRequest, postingDate: string, entities: EntityTarget[], key: string): Promise<PostedJournal> {
  const { data } = await callJson<PostedJournal>("POST", "/journals", { body: { ...req, postingDate, entities }, headers: { "Idempotency-Key": key } });
  return data;
}

export async function listJournals(f: { status?: string; source?: string; after?: string } = {}, signal?: AbortSignal): Promise<Page<PostedJournal>> {
  return (await callJson<Page<PostedJournal>>("GET", `/journals${query(f)}`, { signal })).data;
}

export async function getJournal(id: string, signal?: AbortSignal): Promise<PostedJournal> {
  return (await callJson<PostedJournal>("GET", `/journals/${encodeURIComponent(id)}`, { signal })).data;
}

/** Post one IC charge. A charge with no transfer price answers with status "suspense" and posts nothing. */
export async function postCharge(req: ChargePreviewRequest, extra: { sourceId: string; version: number; postingDate: string; entities: EntityTarget[] }): Promise<ChargeRecord> {
  const { data } = await callJson<ChargeRecord>("POST", "/charges", { body: { ...req, ...extra } });
  return data;
}

export async function listCharges(f: { status?: string; sourceId?: string; after?: string } = {}, signal?: AbortSignal): Promise<Page<ChargeRecord>> {
  return (await callJson<Page<ChargeRecord>>("GET", `/charges${query(f)}`, { signal })).data;
}

export type Resolution = "none" | "open" | "resolved";

export interface ReconRun {
  id: string;
  period: string;
  currency: string;
  fxTolerancePct: string;
  matched: number;
  timing: number;
  mismatch: number;
  /** Items still to resolve. */
  open: number;
  /** True while the run holds a Mismatch. */
  blocking: boolean;
  /** "ledger" when the pair amounts were read from the ledgers of the stored matrix. */
  source?: string;
  createdBy: string;
  createdAt: string;
}

export interface StoredReconItem {
  id: string;
  runId: string;
  ref?: string;
  entityA: string;
  entityB: string;
  amountA: string;
  amountB: string;
  nextPeriodA: string;
  nextPeriodB: string;
  fxApplies: boolean;
  difference: string;
  status: ReconStatus;
  toleranceApplied: boolean;
  resolution: Resolution;
  note?: string;
  resolvedBy?: string;
  resolvedAt?: string;
}

/** Store a reconciliation run of the items' shared period and currency. */
export async function runReconciliation(req: { period: string; currency: string; fxTolerancePct?: string; items: Omit<ClassifyItem, "period" | "currency">[] }, key: string): Promise<ReconRun> {
  const { data } = await callJson<ReconRun>("POST", "/reconciliations/run", { body: req, headers: { "Idempotency-Key": key } });
  return data;
}

export async function listReconRuns(after?: string, signal?: AbortSignal): Promise<Page<ReconRun>> {
  return (await callJson<Page<ReconRun>>("GET", `/reconciliations${query({ after })}`, { signal })).data;
}

export async function getReconRun(id: string, status?: string, signal?: AbortSignal): Promise<ReconRun & { items: StoredReconItem[] }> {
  return (await callJson<ReconRun & { items: StoredReconItem[] }>("GET", `/reconciliations/${encodeURIComponent(id)}${query({ status })}`, { signal })).data;
}

export async function resolveItem(itemId: string, note: string): Promise<StoredReconItem> {
  return (await callJson<StoredReconItem>("POST", `/reconciliations/items/${encodeURIComponent(itemId)}/resolve`, { body: { note } })).data;
}

/** A fresh Idempotency-Key for one submission of a form. */
export const newKey = (): string => crypto.randomUUID();
