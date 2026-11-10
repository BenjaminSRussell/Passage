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
  ownership: Holding[];
}

export interface GroupDetail extends Group {
  units: Unit[];
}

export type AccountType = "balance_sheet" | "income";

export interface AccountMapRow {
  mainAccount: string;
  consolidationAccount: string;
  accountType: AccountType;
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

export const getAccountMap = (groupId: string, signal?: AbortSignal) =>
  call<{ accounts: AccountMapRow[] }>("GET", `/groups/${encodeURIComponent(groupId)}/account-map`, { signal });

export const setAccountMap = (groupId: string, accounts: AccountMapRow[]) =>
  call<{ accounts: AccountMapRow[] }>("PUT", `/groups/${encodeURIComponent(groupId)}/account-map`, { body: { accounts } });

export const listCollections = (groupId: string, signal?: AbortSignal) =>
  call<{ items: Collection[] }>("GET", `/groups/${encodeURIComponent(groupId)}/collections`, { signal });

/** Collect every unit's balances from its ledger for a period. */
export const collectBalances = (groupId: string, input: { period: string; rates: CollectRate[] }, key: string) =>
  call<Collection>("POST", `/groups/${encodeURIComponent(groupId)}/collections`, { body: input, headers: { "Idempotency-Key": key } });

export const getTrialBalance = (collectionId: string, signal?: AbortSignal) =>
  call<TrialBalance>("GET", `/collections/${encodeURIComponent(collectionId)}/trial-balance`, { signal });

/** A fresh Idempotency-Key for one submission of a form. */
export const newKey = (): string => crypto.randomUUID();
