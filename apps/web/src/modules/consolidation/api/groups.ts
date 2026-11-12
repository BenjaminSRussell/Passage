// Client for the group structure, the collection of unit balances and the
// consolidated trial balance (FR-40.7, FR-40.8, FR-40.11) in
// services/consolidation/api/openapi.yaml. Amounts and percentages are decimal
// strings; the browser never does arithmetic on a float.
import { call } from "./client";

export interface Group {
  id: string;
  code: string;
  name: string;
  currency: string;
  createdBy: string;
  createdAt: string;
  /** Only in the list. */
  unitCount?: number;
}

export interface Holding {
  id: string;
  holderUnitId?: string;
  effectiveFrom: string;
  effectiveTo?: string;
  percent: string;
}

export interface Unit {
  id: string;
  legalEntityId: string;
  ledgerId: string;
  currency: string;
  /** Total held on today's date. */
  currentPercent: string;
  /** The month (1 to 12) the unit's fiscal year starts in; income is collected from the start of the year the period falls in. */
  fiscalYearStartMonth?: number;
  ownership: Holding[];
}

export interface GroupDetail extends Group {
  units: Unit[];
}

export type AccountType = "balance_sheet" | "income";

/** What a run reads an account for: the balance sheet totals of a unit and its intercompany balances. */
export type AccountRole = "asset" | "liability" | "equity" | "ic_sales" | "ic_cost" | "ic_receivable" | "ic_payable" | "dividend_paid";

export interface AccountMapRow {
  mainAccount: string;
  consolidationAccount: string;
  accountType: AccountType;
  role?: AccountRole;
}

export interface UnitTerms {
  unitId: string;
  investmentCost: string;
  shareCapital: string;
  preAcquisitionRe: string;
  historicalRate: string;
  sellerIsSubsidiary: boolean;
  icNetInvestment: boolean;
  fxGainLossAccount?: string;
  /** full consolidates the unit line by line; equity takes an associate with the equity method. */
  method?: "full" | "equity";
}

export interface MappingVersion {
  version: number;
  createdBy: string;
  createdAt: string;
  accounts: number;
}

export interface DerivedRun {
  id: string;
  groupId: string;
  period: string;
  currency: string;
  collectionId?: string;
  version: number;
  unitCount: number;
  eliminationCount: number;
  ctaAmount: string;
}

export interface CollectionValidations {
  blocked: boolean;
  units: { unit: string; status: string; results: { rule: string; severity: string; difference: string; failed: boolean }[] }[];
}

export interface Collection {
  id: string;
  groupId: string;
  period: string;
  version: number;
  currency: string;
  createdBy: string;
  createdAt: string;
  lineCount?: number;
  unmappedCount?: number;
}

export interface CollectRate {
  currency: string;
  average: string;
  closing: string;
}

export interface TrialBalanceRow {
  account: string;
  accountType: AccountType;
  total: string;
  /** Each unit's contribution, keyed by unit id. */
  units: Record<string, string>;
}

export interface UnmappedLine {
  unit: string;
  mainAccount: string;
  unitCurrency: string;
  unitAmount: string;
  rate: string;
  groupAmount: string;
}

export interface TrialBalance extends Collection {
  rows: TrialBalanceRow[];
  unmapped: UnmappedLine[];
  total: string;
  /** Each unit's translation difference, keyed by unit id: what a run books as CTA. */
  unitDifferences: Record<string, string>;
}

export const listGroups = (signal?: AbortSignal) => call<{ items: Group[] }>("GET", "/groups", { signal });

export const getGroup = (id: string, signal?: AbortSignal) => call<GroupDetail>("GET", `/groups/${encodeURIComponent(id)}`, { signal });

/** Create a group. Send the same key again to retry one submission safely. */
export const createGroup = (input: { code: string; name: string; currency: string }, key: string) =>
  call<Group>("POST", "/groups", { body: input, headers: { "Idempotency-Key": key } });

export const addUnit = (groupId: string, input: { legalEntityId: string; ledgerId: string; currency: string }) =>
  call<Unit>("POST", `/groups/${encodeURIComponent(groupId)}/units`, { body: input });

export const addOwnership = (groupId: string, unitId: string, input: { holderUnitId?: string; effectiveFrom: string; percent: string }) =>
  call<Unit>("POST", `/groups/${encodeURIComponent(groupId)}/units/${encodeURIComponent(unitId)}/ownership`, { body: input });

export const updateUnit = (groupId: string, unitId: string, changes: { ledgerId?: string; currency?: string; fiscalYearStartMonth?: number }) =>
  call<Unit>("PATCH", `/groups/${encodeURIComponent(groupId)}/units/${encodeURIComponent(unitId)}`, { body: changes });

export const removeUnit = (groupId: string, unitId: string) => call<void>("DELETE", `/groups/${encodeURIComponent(groupId)}/units/${encodeURIComponent(unitId)}`);

export const setUnitTerms = (groupId: string, unitId: string, terms: Omit<UnitTerms, "unitId">) =>
  call<UnitTerms>("PUT", `/groups/${encodeURIComponent(groupId)}/units/${encodeURIComponent(unitId)}/terms`, { body: terms });

export const listUnitTerms = (groupId: string, signal?: AbortSignal) => call<{ items: UnitTerms[] }>("GET", `/groups/${encodeURIComponent(groupId)}/unit-terms`, { signal });

export const getAccountMap = (groupId: string, signal?: AbortSignal) =>
  call<{ accounts: AccountMapRow[]; version?: number }>("GET", `/groups/${encodeURIComponent(groupId)}/account-map`, { signal });

/** Every save is a new version; the earlier ones stay readable. */
export const setAccountMap = (groupId: string, accounts: AccountMapRow[]) =>
  call<{ accounts: AccountMapRow[]; version?: number }>("PUT", `/groups/${encodeURIComponent(groupId)}/account-map`, { body: { accounts } });

export const listMappingVersions = (groupId: string, signal?: AbortSignal) =>
  call<{ items: MappingVersion[] }>("GET", `/groups/${encodeURIComponent(groupId)}/account-map/versions`, { signal });

/** Derive a consolidation run from a stored collection; ic supplies the pair amounts a trial balance cannot attribute to one partner. */
export const startRunFromCollection = (input: { collectionId: string; ic?: { unit: string; unsoldShare?: string; payableGroup?: string }[]; openingRates?: { currency: string; rate: string }[] }, key: string) =>
  call<DerivedRun>("POST", "/runs", { body: input, headers: { "Idempotency-Key": key } });

export const getCollectionValidations = (collectionId: string, signal?: AbortSignal) =>
  call<CollectionValidations>("GET", `/collections/${encodeURIComponent(collectionId)}/validations`, { signal });

export const listCollections = (groupId: string, signal?: AbortSignal) =>
  call<{ items: Collection[] }>("GET", `/groups/${encodeURIComponent(groupId)}/collections`, { signal });

/** Collect every unit's balances from its ledger for a period. */
export const collectBalances = (groupId: string, input: { period: string; rates: CollectRate[] }, key: string) =>
  call<Collection>("POST", `/groups/${encodeURIComponent(groupId)}/collections`, { body: input, headers: { "Idempotency-Key": key } });

export const getTrialBalance = (collectionId: string, signal?: AbortSignal) =>
  call<TrialBalance>("GET", `/collections/${encodeURIComponent(collectionId)}/trial-balance`, { signal });

/** A fresh Idempotency-Key for one submission of a form. */
export const newKey = (): string => crypto.randomUUID();
