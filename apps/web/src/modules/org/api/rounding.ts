import { orgFetch, type Scope } from "./client";
import { referenceFetch, withQuery } from "./reference";

export const roundingPurposes = ["general", "price", "purchase", "sales", "tax", "cash"] as const;
export type RoundingPurpose = (typeof roundingPurposes)[number];

export const roundingMethods = ["half_even", "half_up", "down", "up"] as const;
export type RoundingMethod = (typeof roundingMethods)[number];

export interface CurrencyRounding {
  id: string;
  currencyCode: string;
  purpose: RoundingPurpose;
  legalEntityId: string | null;
  unit: string;
  method: RoundingMethod;
  updatedAt: string;
}

export interface RoundResult {
  currency: string;
  purpose: RoundingPurpose;
  legalEntityId: string | null;
  amount: string;
  rounded: string;
  unit: string;
  method: RoundingMethod;
  source: "legal_entity" | "tenant" | "default";
}

export interface PutRoundingInput {
  unit: string;
  method: RoundingMethod;
  // Absent stores the tenant-wide rule.
  legalEntityId?: string;
}

const rulesPath = (currency: string) => `/currencies/${encodeURIComponent(currency)}/rounding-rules`;

// Legal-entity rules sit behind the legal-entity RLS policy, so reads name a scope: the list uses the switcher
// scope (ALL shows every entity's rules, one entity shows its rules and the tenant's), and a test for one legal
// entity is sent under that entity, since the service refuses an unscoped caller rounding for a legal entity.
// Writes stay tenant-level through referenceFetch: the service requires tenant.administer and no legal-entity claim.
export async function listRoundingRules(currency: string, scope: Scope, options?: { signal?: AbortSignal }): Promise<CurrencyRounding[]> {
  const body = await orgFetch<{ items?: unknown }>(rulesPath(currency), { signal: options?.signal }, scope);
  return Array.isArray(body?.items) ? (body.items as CurrencyRounding[]) : [];
}

export const putRoundingRule = (currency: string, purpose: RoundingPurpose, input: PutRoundingInput) =>
  referenceFetch<CurrencyRounding>(`${rulesPath(currency)}/${purpose}`, {
    method: "PUT",
    body: JSON.stringify({ unit: input.unit, method: input.method, ...(input.legalEntityId ? { legalEntityId: input.legalEntityId } : {}) }),
  });

export const deleteRoundingRule = (currency: string, purpose: RoundingPurpose, legalEntityId: string | null) =>
  referenceFetch<void>(withQuery(`${rulesPath(currency)}/${purpose}`, { legalEntityId: legalEntityId ?? undefined }), { method: "DELETE" });

export function roundAmount(currency: string, amount: string, purpose: RoundingPurpose, legalEntityId?: string): Promise<RoundResult> {
  const path = withQuery(`/currencies/${encodeURIComponent(currency)}/round`, { amount, purpose, legalEntityId });
  return legalEntityId ? orgFetch<RoundResult>(path, {}, legalEntityId) : referenceFetch<RoundResult>(path);
}
