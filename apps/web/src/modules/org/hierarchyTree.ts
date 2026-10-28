import type { HierarchyChange, HierarchyComparison, HierarchyNode, HierarchyNodeKind, HierarchyVersionSummary } from "./api/hierarchy";

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

export const todayIso = () => new Date().toISOString().slice(0, 10);

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

const parentOf = (nodes: readonly HierarchyNode[], id: string) => nodes.find((node) => node.orgUnitId === id)?.parentOrgUnitId ?? null;

export function siblingIds(nodes: readonly HierarchyNode[], parentId: string | null): string[] {
  return nodes
    .filter((node) => node.parentOrgUnitId === parentId)
    .sort((a, b) => a.sortOrder - b.sortOrder || a.orgUnitId.localeCompare(b.orgUnitId))
    .map((node) => node.orgUnitId);
}

export function isDescendant(nodes: readonly HierarchyNode[], ancestorId: string, id: string): boolean {
  let current = parentOf(nodes, id);
  for (let hops = 0; current !== null && hops <= nodes.length; hops += 1) {
    if (current === ancestorId) {
      return true;
    }
    current = parentOf(nodes, current);
  }
  return false;
}

function normalizeOrder(nodes: readonly HierarchyNode[]): HierarchyNode[] {
  const order = new Map<string, number>();
  for (const parent of new Set(nodes.map((node) => node.parentOrgUnitId))) {
    siblingIds(nodes, parent).forEach((id, index) => order.set(id, index));
  }
  return nodes.map((node) => ({ ...node, sortOrder: order.get(node.orgUnitId) ?? 0 }));
}

export function moveNode(nodes: readonly HierarchyNode[], id: string, parentId: string, index?: number): HierarchyNode[] | null {
  const moving = nodes.find((node) => node.orgUnitId === id);
  if (!moving || moving.parentOrgUnitId === null || parentId === id || isDescendant(nodes, id, parentId)) {
    return null;
  }
  if (!nodes.some((node) => node.orgUnitId === parentId)) {
    return null;
  }
  const siblings = siblingIds(nodes, parentId).filter((sibling) => sibling !== id);
  siblings.splice(Math.min(Math.max(index ?? siblings.length, 0), siblings.length), 0, id);
  const placed = new Map(siblings.map((sibling, position) => [sibling, position]));
  return normalizeOrder(
    nodes.map((node) => {
      const position = placed.get(node.orgUnitId);
      if (node.orgUnitId === id) {
        return { ...node, parentOrgUnitId: parentId, sortOrder: position ?? 0 };
      }
      return position === undefined ? node : { ...node, sortOrder: position };
    }),
  );
}

export function shiftNode(nodes: readonly HierarchyNode[], id: string, delta: -1 | 1): HierarchyNode[] | null {
  const parentId = parentOf(nodes, id);
  if (parentId === null) {
    return null;
  }
  const siblings = siblingIds(nodes, parentId);
  const target = siblings.indexOf(id) + delta;
  return target < 0 || target >= siblings.length ? null : moveNode(nodes, id, parentId, target);
}

export function indentNode(nodes: readonly HierarchyNode[], id: string): HierarchyNode[] | null {
  const parentId = parentOf(nodes, id);
  if (parentId === null) {
    return null;
  }
  const siblings = siblingIds(nodes, parentId);
  const above = siblings[siblings.indexOf(id) - 1];
  return above === undefined ? null : moveNode(nodes, id, above);
}

export function outdentNode(nodes: readonly HierarchyNode[], id: string): HierarchyNode[] | null {
  const parentId = parentOf(nodes, id);
  const grandparentId = parentId === null ? null : parentOf(nodes, parentId);
  if (parentId === null || grandparentId === null) {
    return null;
  }
  return moveNode(nodes, id, grandparentId, siblingIds(nodes, grandparentId).indexOf(parentId) + 1);
}

export function parentChoices(nodes: readonly HierarchyNode[], id: string): string[] {
  const current = parentOf(nodes, id);
  if (current === null) {
    return [];
  }
  return nodes.filter((node) => node.orgUnitId !== id && node.orgUnitId !== current && !isDescendant(nodes, id, node.orgUnitId)).map((node) => node.orgUnitId);
}

export function addNode(nodes: readonly HierarchyNode[], orgUnitId: string, kind: HierarchyNodeKind, parentId: string | null): HierarchyNode[] | null {
  const parentValid = nodes.length === 0 ? parentId === null : nodes.some((node) => node.orgUnitId === parentId);
  if (!parentValid || nodes.some((node) => node.orgUnitId === orgUnitId)) {
    return null;
  }
  const last = Math.max(-1, ...nodes.filter((node) => node.parentOrgUnitId === parentId).map((node) => node.sortOrder));
  return normalizeOrder([...nodes, { orgUnitId, kind, parentOrgUnitId: parentId, sortOrder: last + 1 }]);
}

export function removeNode(nodes: readonly HierarchyNode[], id: string): HierarchyNode[] | null {
  const removing = nodes.find((node) => node.orgUnitId === id);
  if (!removing || removing.parentOrgUnitId === null) {
    return null;
  }
  const parentId = removing.parentOrgUnitId;
  const adopted = siblingIds(nodes, id);
  const lineup = siblingIds(nodes, parentId).flatMap((sibling) => (sibling === id ? adopted : [sibling]));
  const place = new Map(lineup.map((sibling, position) => [sibling, position]));
  return normalizeOrder(
    nodes
      .filter((node) => node.orgUnitId !== id)
      .map((node) => ({
        ...node,
        parentOrgUnitId: node.parentOrgUnitId === id ? parentId : node.parentOrgUnitId,
        sortOrder: place.get(node.orgUnitId) ?? node.sortOrder,
      })),
  );
}

export function treeSignature(nodes: readonly HierarchyNode[]): string {
  const parts: string[] = [];
  const walk = (level: TreeNode[]) => {
    for (const { node, children } of level) {
      parts.push(`${node.parentOrgUnitId ?? ""}>${node.orgUnitId}`);
      walk(children);
    }
  };
  walk(buildTree(nodes));
  return parts.join("|");
}

export function diffNodes(before: readonly HierarchyNode[], after: readonly HierarchyNode[]): { changes: Map<string, NodeChange>; removed: HierarchyChange[] } {
  const was = new Map(before.map((node) => [node.orgUnitId, node]));
  const changes = new Map<string, NodeChange>();
  for (const node of after) {
    const old = was.get(node.orgUnitId);
    if (!old) {
      changes.set(node.orgUnitId, { kind: "added" });
    } else if (old.parentOrgUnitId !== node.parentOrgUnitId) {
      changes.set(node.orgUnitId, { kind: "moved", oldParentOrgUnitId: old.parentOrgUnitId });
    }
  }
  const now = new Set(after.map((node) => node.orgUnitId));
  const removed = before
    .filter((node) => !now.has(node.orgUnitId))
    .map(({ orgUnitId, kind, parentOrgUnitId }) => ({ orgUnitId, kind, parentOrgUnitId }));
  return { changes, removed };
}

export function replaceNode(nodes: readonly HierarchyNode[], oldId: string, newId: string, kind: HierarchyNodeKind): HierarchyNode[] | null {
  if (!nodes.some((node) => node.orgUnitId === oldId) || nodes.some((node) => node.orgUnitId === newId)) {
    return null;
  }
  return normalizeOrder(
    nodes.map((node) => ({
      ...node,
      orgUnitId: node.orgUnitId === oldId ? newId : node.orgUnitId,
      kind: node.orgUnitId === oldId ? kind : node.kind,
      parentOrgUnitId: node.parentOrgUnitId === oldId ? newId : node.parentOrgUnitId,
    })),
  );
}

const csvCell = (value: string) => {
  const safe = /^[=+\-@\t\r]/.test(value) ? `'${value}` : value;
  return /[",\n\r]/.test(safe) ? `"${safe.replaceAll('"', '""')}"` : safe;
};

export function hierarchyCsv(roots: readonly TreeNode[], describe: (id: string) => { name: string; code: string }): string {
  const rows = [["Level", "Name", "Code", "Kind", "Parent code"]];
  const walk = (nodes: readonly TreeNode[], level: number, parentCode: string) => {
    for (const { node, children } of nodes) {
      const { name, code } = describe(node.orgUnitId);
      rows.push([String(level), name, code, node.kind, parentCode]);
      walk(children, level + 1, code);
    }
  };
  walk(roots, 1, "");
  return rows.map((row) => row.map(csvCell).join(",")).join("\r\n");
}
