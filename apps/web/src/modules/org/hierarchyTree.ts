import type { HierarchyComparison, HierarchyNode, HierarchyVersionSummary } from "./api/hierarchy";

export interface TreeNode {
  node: HierarchyNode;
  children: TreeNode[];
}

export type NodeChange = { kind: "added" } | { kind: "moved"; oldParentOrgUnitId: string | null };

export function buildTree(nodes: readonly HierarchyNode[]): TreeNode[] {
  const known = new Set(nodes.map((node) => node.orgUnitId));
  const byParent = new Map<string | null, HierarchyNode[]>();
  for (const node of nodes) {
    const parent = node.parentOrgUnitId !== null && known.has(node.parentOrgUnitId) ? node.parentOrgUnitId : null;
    byParent.set(parent, [...(byParent.get(parent) ?? []), node]);
  }
  const build = (parent: string | null): TreeNode[] =>
    (byParent.get(parent) ?? [])
      .sort((a, b) => a.sortOrder - b.sortOrder || a.orgUnitId.localeCompare(b.orgUnitId))
      .map((node) => ({ node, children: build(node.orgUnitId) }));
  return build(null);
}

const ISO_DATE = /^(\d{4})-(\d{2})-(\d{2})$/;

export function dateFromDayNumber(days: number): string {
  const z = days + 719468;
  const era = Math.floor(z / 146097);
  const doe = z - era * 146097;
  const yoe = Math.floor((doe - Math.floor(doe / 1460) + Math.floor(doe / 36524) - Math.floor(doe / 146096)) / 365);
  const doy = doe - (365 * yoe + Math.floor(yoe / 4) - Math.floor(yoe / 100));
  const mp = Math.floor((5 * doy + 2) / 153);
  const day = doy - Math.floor((153 * mp + 2) / 5) + 1;
  const month = mp < 10 ? mp + 3 : mp - 9;
  const year = yoe + era * 400 + (month <= 2 ? 1 : 0);
  return `${String(year).padStart(4, "0")}-${String(month).padStart(2, "0")}-${String(day).padStart(2, "0")}`;
}

export function dayNumber(iso: string): number | null {
  const match = ISO_DATE.exec(iso);
  if (!match) {
    return null;
  }
  const [year, month, day] = [Number(match[1]), Number(match[2]), Number(match[3])];
  const y = month <= 2 ? year - 1 : year;
  const era = Math.floor(y / 400);
  const yoe = y - era * 400;
  const doy = Math.floor((153 * (month + (month > 2 ? -3 : 9)) + 2) / 5) + day - 1;
  const days = era * 146097 + yoe * 365 + Math.floor(yoe / 4) - Math.floor(yoe / 100) + doy - 719468;
  return dateFromDayNumber(days) === iso ? days : null;
}

export const addDays = (iso: string, delta: number): string => dateFromDayNumber((dayNumber(iso) ?? 0) + delta);

export function versionAsOf<T extends HierarchyVersionSummary>(versions: readonly T[], date: string): T | null {
  if (dayNumber(date) === null) {
    return null;
  }
  const inForce = versions.filter(
    (version) =>
      version.status !== "draft" &&
      version.effectiveFrom !== null &&
      version.effectiveFrom <= date &&
      (version.effectiveTo === null || date <= version.effectiveTo),
  );
  return inForce.reduce<T | null>((best, version) => (best === null || version.versionNo > best.versionNo ? version : best), null);
}

export function previousVersion<T extends HierarchyVersionSummary>(versions: readonly T[], version: T): T | undefined {
  return versions.filter((candidate) => candidate.versionNo < version.versionNo).sort((a, b) => b.versionNo - a.versionNo)[0];
}

export function changeMap(comparison: HierarchyComparison): Map<string, NodeChange> {
  const changes = new Map<string, NodeChange>();
  for (const added of comparison.added ?? []) {
    changes.set(added.orgUnitId, { kind: "added" });
  }
  for (const moved of comparison.moved ?? []) {
    changes.set(moved.orgUnitId, { kind: "moved", oldParentOrgUnitId: moved.oldParentOrgUnitId });
  }
  return changes;
}

export function sliderRange(versions: readonly HierarchyVersionSummary[], fallbackDate: string): { min: string; max: string } {
  const starts = versions.flatMap((version) => (version.effectiveFrom === null ? [] : [version.effectiveFrom])).sort();
  if (starts.length === 0) {
    return { min: fallbackDate, max: addDays(fallbackDate, 365) };
  }
  const ends = versions
    .flatMap((version) => (version.effectiveFrom === null ? [] : [version.effectiveTo ?? addDays(version.effectiveFrom, 365)]))
    .sort();
  return { min: starts[0], max: ends[ends.length - 1] };
}
