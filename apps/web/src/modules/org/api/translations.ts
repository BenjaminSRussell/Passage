import { orgFetch, type Scope } from "./client";

export const ownerKinds = [
  { collection: "legal-entities", label: "Legal entity" },
  { collection: "operating-units", label: "Operating unit" },
  { collection: "sites", label: "Site" },
  { collection: "warehouses", label: "Warehouse" },
] as const;

export type OwnerCollection = (typeof ownerKinds)[number]["collection"];

export interface NameTranslation {
  ownerType: string;
  ownerId: string;
  language: string;
  name: string;
  createdAt: string;
  updatedAt: string;
}

export interface NameResolution {
  ownerType: string;
  ownerId: string;
  language?: string;
  name: string;
  source: "translation" | "base";
}

// Translation routes are legal-entity scoped like the owners they name, so every call goes through orgFetch
// with the switcher scope; orgFetch refuses a write under the ALL scope.
const ownerPath = (collection: OwnerCollection, id: string) => `/${collection}/${encodeURIComponent(id)}`;
const translationPath = (collection: OwnerCollection, id: string, language: string) =>
  `${ownerPath(collection, id)}/translations/${encodeURIComponent(language)}`;

export async function listNameTranslations(collection: OwnerCollection, id: string, scope: Scope, options?: { signal?: AbortSignal }): Promise<NameTranslation[]> {
  const body = await orgFetch<{ items?: unknown }>(`${ownerPath(collection, id)}/translations`, { signal: options?.signal }, scope);
  return Array.isArray(body?.items) ? (body.items as NameTranslation[]) : [];
}

export const putNameTranslation = (collection: OwnerCollection, id: string, language: string, name: string, scope: Scope) =>
  orgFetch<NameTranslation>(
    translationPath(collection, id, language),
    { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) },
    scope,
  );

export const deleteNameTranslation = (collection: OwnerCollection, id: string, language: string, scope: Scope) =>
  orgFetch<void>(translationPath(collection, id, language), { method: "DELETE" }, scope);

export const resolveName = (collection: OwnerCollection, id: string, language: string, scope: Scope) =>
  orgFetch<NameResolution>(`${ownerPath(collection, id)}/name?language=${encodeURIComponent(language)}`, {}, scope);
