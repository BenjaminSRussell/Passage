// The stored intercompany setup (services/intercompany, FR-40.6, BR-40.4, BR-40.12): the entity registry, the accounting matrix,
// effective-dated transfer prices, the tenant's settings, ledger-read reconciliation runs and the FX adjustment of an open item.
import { callJson, type ChargeCategory, type MatrixRow, type TransferMethod } from "./client";
import type { ReconRun } from "./postings";

/** Where an entity code's books are: its legal entity, ledger and currency, and the accounts the service posts to. */
export interface RegistryEntity {
  entity: string;
  legalEntityId: string;
  ledgerId: string;
  currency: string;
  country?: string;
  fxDifferenceAccount?: string;
  outputTaxAccount?: string;
  inputTaxAccount?: string;
  taxGroup?: string;
}

export interface StoredTransferPrice {
  id?: string;
  lendingEntity: string;
  borrowingEntity: string;
  category: ChargeCategory;
  resourceRole: string;
  method: TransferMethod;
  markupPct?: string;
  unitPrice?: string;
  currency: string;
  effectiveFrom: string;
  createdBy?: string;
  createdAt?: string;
}

export type SettlementMode = "Journal" | "Invoice";

export interface IcSettings {
  chargeSettlementMode: SettlementMode;
  crossBorderTax: boolean;
  /** The currency the scheduled reconciliation compares in; empty switches the schedule off. */
  reconCurrency: string;
  fxTolerancePct: string;
  /** The largest difference, in percent of the larger side, an FX adjustment may book (0 to 10). */
  fxAdjustMaxPct: string;
}

export interface FxAdjustment {
  id: string;
  itemId: string;
  entity: string;
  currency: string;
  amount: string;
  direction: "increase" | "decrease";
  account: string;
  fxAccount: string;
  ledgerEntryId: string;
  postedOn: string;
  periodEnd: string;
}

const qs = (params: Record<string, string | undefined>): string => {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : "";
};

export async function listEntities(signal?: AbortSignal): Promise<{ items: RegistryEntity[] }> {
  return (await callJson<{ items: RegistryEntity[] }>("GET", "/entities", { signal })).data;
}

export async function putEntity(e: RegistryEntity): Promise<RegistryEntity> {
  const { entity, ...body } = e;
  return (await callJson<RegistryEntity>("PUT", `/entities/${encodeURIComponent(entity)}`, { body })).data;
}

export async function getMatrix(signal?: AbortSignal): Promise<{ rows: MatrixRow[] }> {
  return (await callJson<{ rows: MatrixRow[] }>("GET", "/matrix", { signal })).data;
}

export async function putMatrix(rows: MatrixRow[]): Promise<{ rows: MatrixRow[] }> {
  return (await callJson<{ rows: MatrixRow[] }>("PUT", "/matrix", { body: { rows } })).data;
}

export async function listTransferPrices(f: { lender?: string; borrower?: string } = {}, signal?: AbortSignal): Promise<{ items: StoredTransferPrice[] }> {
  return (await callJson<{ items: StoredTransferPrice[] }>("GET", `/transfer-prices${qs(f)}`, { signal })).data;
}

export async function addTransferPrice(p: StoredTransferPrice): Promise<StoredTransferPrice> {
  return (await callJson<StoredTransferPrice>("POST", "/transfer-prices", { body: p })).data;
}

export async function getSettings(signal?: AbortSignal): Promise<IcSettings> {
  return (await callJson<IcSettings>("GET", "/settings", { signal })).data;
}

export async function putSettings(s: IcSettings): Promise<IcSettings> {
  return (await callJson<IcSettings>("PUT", "/settings", { body: s })).data;
}

/** Reads every pair of the stored matrix from the ledgers, classifies it and stores the run. */
export async function runLedgerReconciliation(req: { period: string; currency: string; fxTolerancePct?: string }, key: string): Promise<ReconRun> {
  return (await callJson<ReconRun>("POST", "/reconciliations/run", { body: { ...req, source: "ledger" }, headers: { "Idempotency-Key": key } })).data;
}

/** Books an open item's exchange difference to its entity's FX-difference account and resolves the item. */
export async function adjustFx(itemId: string): Promise<FxAdjustment> {
  return (await callJson<FxAdjustment>("POST", `/reconciliations/items/${encodeURIComponent(itemId)}/fx-adjust`, {})).data;
}
