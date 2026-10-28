import { useEffect, useMemo, useRef, useState, type DragEvent, type KeyboardEvent } from "react";
import { Badge } from "../../shell/ui";
import type { HierarchyNode } from "./api/hierarchy";
import { buildTree, parentChoices, type NodeChange, type TreeNode } from "./hierarchyTree";
import styles from "./pages.module.css";

export interface Label {
  name: string;
  code: string;
}

export type MoveCommand = "up" | "down" | "indent" | "outdent";

export interface TreeEditing {
  selectedId: string | null;
  focusSignal: number;
  onSelect(id: string): void;
  onMove(command: MoveCommand, refocus: boolean): void;
  onDrop(id: string, parentId: string): void;
}

const pluralChildren = (count: number) => `${count} ${count === 1 ? "child" : "children"}`;

const shortcuts: Record<string, MoveCommand> = { ArrowUp: "up", ArrowDown: "down", ArrowRight: "indent", ArrowLeft: "outdent" };

interface VisibleItem {
  id: string;
  parentId: string | null;
  hasChildren: boolean;
}

function flatten(roots: TreeNode[], collapsed: ReadonlySet<string>): VisibleItem[] {
  const items: VisibleItem[] = [];
  const walk = (nodes: TreeNode[], parentId: string | null) => {
    for (const { node, children } of nodes) {
      items.push({ id: node.orgUnitId, parentId, hasChildren: children.length > 0 });
      if (!collapsed.has(node.orgUnitId)) {
        walk(children, node.orgUnitId);
      }
    }
  };
  walk(roots, null);
  return items;
}

interface TreeViewProps {
  nodes: HierarchyNode[];
  labels: ReadonlyMap<string, Label>;
  changes: ReadonlyMap<string, NodeChange>;
  editing?: TreeEditing;
}

export function HierarchyTreeView({ nodes, labels, changes, editing }: TreeViewProps) {
  const roots = useMemo(() => buildTree(nodes), [nodes]);
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set());
  const [focusId, setFocusId] = useState<string | null>(null);
  const [dragId, setDragId] = useState<string | null>(null);
  const [overId, setOverId] = useState<string | null>(null);
  const treeRef = useRef<HTMLUListElement>(null);
  const visible = flatten(roots, collapsed);
  const tabStop = visible.some((item) => item.id === focusId) ? focusId : (visible[0]?.id ?? null);
  const dropTargets = useMemo(() => new Set(dragId === null ? [] : parentChoices(nodes, dragId)), [nodes, dragId]);

  const labelText = (id: string | null) => {
    const label = id === null ? undefined : labels.get(id);
    return id === null ? "no parent (root)" : label ? `${label.name} (${label.code})` : id;
  };
  const toggle = (id: string, open: boolean) =>
    setCollapsed((current) => {
      const next = new Set(current);
      if (open) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  const focusItem = (id: string | null | undefined) => {
    if (id) {
      Array.from(treeRef.current?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])
        .find((element) => element.dataset.id === id)
        ?.focus();
    }
  };

  const focusSignal = editing?.focusSignal ?? 0;
  const selectedId = editing?.selectedId ?? null;
  useEffect(() => {
    if (focusSignal > 0) {
      focusItem(selectedId);
    }
  }, [focusSignal]);

  const onKeyDown = (event: KeyboardEvent<HTMLUListElement>) => {
    const id = (event.target as HTMLElement).closest<HTMLElement>('[role="treeitem"]')?.dataset.id;
    const index = visible.findIndex((item) => item.id === id);
    const current = visible[index];
    if (!current) {
      return;
    }
    if (editing && event.altKey && shortcuts[event.key]) {
      event.preventDefault();
      editing.onMove(shortcuts[event.key], true);
      return;
    }
    const expanded = !collapsed.has(current.id);
    const handlers: Record<string, () => void> = {
      ArrowDown: () => focusItem(visible[index + 1]?.id),
      ArrowUp: () => focusItem(visible[index - 1]?.id),
      Home: () => focusItem(visible[0]?.id),
      End: () => focusItem(visible[visible.length - 1]?.id),
      ArrowRight: () => (current.hasChildren && !expanded ? toggle(current.id, true) : current.hasChildren ? focusItem(visible[index + 1]?.id) : undefined),
      ArrowLeft: () => (current.hasChildren && expanded ? toggle(current.id, false) : focusItem(current.parentId)),
    };
    const handler = handlers[event.key];
    if (handler) {
      event.preventDefault();
      handler();
    }
  };

  const clearDrag = () => {
    setDragId(null);
    setOverId(null);
  };

  const dragHandlers = (id: string, isRoot: boolean) =>
    editing
      ? {
          draggable: !isRoot,
          onDragStart: (event: DragEvent) => {
            event.dataTransfer?.setData("text/plain", id);
            if (event.dataTransfer) {
              event.dataTransfer.effectAllowed = "move";
            }
            setDragId(id);
          },
          onDragEnd: clearDrag,
          onDragOver: (event: DragEvent) => {
            if (dropTargets.has(id)) {
              event.preventDefault();
              setOverId(id);
            }
          },
          onDragLeave: () => setOverId((current) => (current === id ? null : current)),
          onDrop: (event: DragEvent) => {
            if (dragId !== null && dropTargets.has(id)) {
              event.preventDefault();
              editing.onDrop(dragId, id);
            }
            clearDrag();
          },
        }
      : {};

  const renderNode = ({ node, children }: TreeNode, level: number) => {
    const id = node.orgUnitId;
    const label = labels.get(id);
    const hasChildren = children.length > 0;
    const expanded = !collapsed.has(id);
    const change = changes.get(id);
    const changeText = change ? (change.kind === "added" ? "Added in this version" : `Previously under ${labelText(change.oldParentOrgUnitId)}`) : "";
    const rowClass = [
      styles.treeRow,
      editing && selectedId === id ? styles.treeRowSelected : "",
      overId === id ? styles.treeRowDrop : "",
      dragId === id ? styles.treeRowDragging : "",
    ]
      .filter(Boolean)
      .join(" ");
    return (
      <li
        key={id}
        role="treeitem"
        data-id={id}
        aria-level={level}
        aria-expanded={hasChildren ? expanded : undefined}
        aria-selected={editing ? selectedId === id : undefined}
        aria-labelledby={`tree-label-${id}`}
        aria-describedby={change ? `tree-change-${id}` : undefined}
        tabIndex={id === tabStop ? 0 : -1}
        className={styles.treeItem}
        onFocus={(event) => {
          if (event.target === event.currentTarget) {
            setFocusId(id);
            editing?.onSelect(id);
          }
        }}
      >
        <div className={rowClass} onClick={() => !editing && hasChildren && toggle(id, !expanded)} {...dragHandlers(id, node.parentOrgUnitId === null)}>
          <span id={`tree-label-${id}`} className={styles.treeName}>
            {label?.name ?? id}
          </span>
          {label ? <span className={styles.treeCode}>{label.code}</span> : null}
          <Badge tone={node.kind === "legal_entity" ? "info" : "neutral"}>{node.kind === "legal_entity" ? "Legal entity" : "Operating unit"}</Badge>
          <span className={styles.treeCount}>{pluralChildren(children.length)}</span>
          {change ? (
            <span title={changeText}>
              <Badge tone="warning">changed</Badge>
              <span id={`tree-change-${id}`} className="visually-hidden">
                {changeText}
              </span>
            </span>
          ) : null}
        </div>
        {hasChildren && expanded ? (
          <ul role="group" className={styles.treeGroup}>
            {children.map((child) => renderNode(child, level + 1))}
          </ul>
        ) : null}
      </li>
    );
  };

  return (
    <ul role="tree" aria-label="Hierarchy tree" className={styles.tree} ref={treeRef} onKeyDown={onKeyDown}>
      {roots.map((root) => renderNode(root, 1))}
    </ul>
  );
}
