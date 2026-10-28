import { referenceFetch, withQuery } from "./reference";

export type HierarchyStatus = "draft" | "published" | "superseded";
export type HierarchyNodeKind = "legal_entity" | "operating_unit";

export interface HierarchyType {
  id: string;
  code: string;
  name: string;
  purposes: string[];
  backdateAllowed: boolean;
  status: string;
}

export interface HierarchyVersionSummary {
  id: string;
  hierarchyTypeId: string;
  versionNo: number;
  status: HierarchyStatus;
  effectiveFrom: string | null;
  effectiveTo: string | null;
  nodeCount: number;
}

export interface HierarchyNode {
  orgUnitId: string;
  kind: HierarchyNodeKind;
  parentOrgUnitId: string | null;
  sortOrder: number;
}

export interface HierarchyVersion extends HierarchyVersionSummary {
  version: number;
  nodes: HierarchyNode[];
}

export interface PublishInput {
  version: number;
  effectiveFrom: string;
  reason: string;
}

export interface HierarchyChange {
  orgUnitId: string;
  kind: HierarchyNodeKind;
  parentOrgUnitId: string | null;
}

export interface HierarchyMove {
  orgUnitId: string;
  kind: HierarchyNodeKind;
  oldParentOrgUnitId: string | null;
  newParentOrgUnitId: string | null;
}

export interface HierarchyComparison {
  hierarchyTypeId: string;
  fromVersionNo: number;
  toVersionNo: number;
  added: HierarchyChange[];
  removed: HierarchyChange[];
  moved: HierarchyMove[];
}

interface ReadOptions {
  signal?: AbortSignal;
}

async function listPaged<T>(path: string, filters: Record<string, string | undefined>, signal?: AbortSignal): Promise<T[]> {
  const items: T[] = [];
  let cursor: string | undefined;
  do {
    const body = await referenceFetch<{ items?: unknown; nextCursor?: unknown }>(withQuery(path, { limit: "100", ...filters, cursor }), { signal });
    if (Array.isArray(body?.items)) {
      items.push(...(body.items as T[]));
    }
    const next = body?.nextCursor;
    cursor = typeof next === "string" && next !== "" && next !== cursor ? next : undefined;
  } while (cursor);
  return items;
}

export const listHierarchyTypes = (filters: { purpose?: string; status?: string } = {}, options?: ReadOptions) =>
  listPaged<HierarchyType>("/hierarchy-types", filters, options?.signal);

export const listHierarchyVersions = (typeId: string, status?: HierarchyStatus, options?: ReadOptions) =>
  listPaged<HierarchyVersionSummary>(`/hierarchy-types/${encodeURIComponent(typeId)}/versions`, { status }, options?.signal);

export async function getHierarchyVersion(id: string, options?: ReadOptions): Promise<HierarchyVersion> {
  return normalizeVersion(await referenceFetch<HierarchyVersion>(`/hierarchy-versions/${encodeURIComponent(id)}`, { signal: options?.signal }));
}

export async function compareHierarchyVersions(typeId: string, from: number, to: number, options?: ReadOptions): Promise<HierarchyComparison> {
  const path = withQuery(`/hierarchy-types/${encodeURIComponent(typeId)}/compare`, { from: String(from), to: String(to) });
  const comparison = await referenceFetch<HierarchyComparison>(path, { signal: options?.signal });
  const list = <T,>(value: T[] | null | undefined): T[] => (Array.isArray(value) ? value : []);
  return { ...comparison, added: list(comparison?.added), removed: list(comparison?.removed), moved: list(comparison?.moved) };
}

const normalizeVersion = (version: HierarchyVersion): HierarchyVersion => ({ ...version, nodes: Array.isArray(version?.nodes) ? version.nodes : [] });

export const createHierarchyDraft = async (typeId: string): Promise<HierarchyVersion> =>
  normalizeVersion(await referenceFetch<HierarchyVersion>(`/hierarchy-types/${encodeURIComponent(typeId)}/versions`, { method: "POST" }));

export const replaceHierarchyNodes = async (versionId: string, version: number, nodes: readonly HierarchyNode[]): Promise<HierarchyVersion> =>
  normalizeVersion(
    await referenceFetch<HierarchyVersion>(`/hierarchy-versions/${encodeURIComponent(versionId)}/nodes`, {
      method: "PUT",
      body: JSON.stringify({
        version,
        nodes: nodes.map(({ orgUnitId, parentOrgUnitId, sortOrder }) => ({ orgUnitId, parentOrgUnitId, sortOrder })),
      }),
    }),
  );

export const publishHierarchyVersion = async (versionId: string, input: PublishInput): Promise<HierarchyVersion> =>
  normalizeVersion(
    await referenceFetch<HierarchyVersion>(`/hierarchy-versions/${encodeURIComponent(versionId)}:publish`, {
      method: "POST",
      body: JSON.stringify(input),
    }),
  );
