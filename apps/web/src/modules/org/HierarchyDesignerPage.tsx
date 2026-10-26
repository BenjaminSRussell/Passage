import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Alert, Badge, Button, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { ALL, describeProblem, listOperatingUnits, type Problem } from "./api/client";
import {
  compareHierarchyVersions,
  getHierarchyVersion,
  listHierarchyTypes,
  listHierarchyVersions,
  type HierarchyNode,
  type HierarchyVersionSummary,
} from "./api/hierarchy";
import { useLegalEntity } from "./legalEntityContext";
import {
  buildTree,
  changeMap,
  dateFromDayNumber,
  dayNumber,
  previousVersion,
  sliderRange,
  versionAsOf,
  type NodeChange,
  type TreeNode,
} from "./hierarchyTree";
import styles from "./pages.module.css";

interface LoadState<T> {
  key: string;
  data: T | null;
  loading: boolean;
  error: Problem | null;
}

const idle: LoadState<never> = { key: "", data: null, loading: false, error: null };

function useLoad<T>(key: string, load: (signal: AbortSignal) => Promise<T>): LoadState<T> {
  const [state, setState] = useState<LoadState<T>>(idle);
  const loader = useRef(load);
  loader.current = load;
  useEffect(() => {
    if (key === "") {
      return;
    }
    const controller = new AbortController();
    setState({ key, data: null, loading: true, error: null });
    loader.current(controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) {
          setState({ key, data, loading: false, error: null });
        }
      },
      (failure) => {
        if (!controller.signal.aborted) {
          setState({ key, data: null, loading: false, error: describeProblem(failure) });
        }
      },
    );
    return () => controller.abort();
  }, [key]);
  if (key === "") {
    return idle;
  }
  return state.key === key ? state : { key, data: null, loading: true, error: null };
}

interface Label {
  name: string;
  code: string;
}

const pluralChildren = (count: number) => `${count} ${count === 1 ? "child" : "children"}`;

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
}

function HierarchyTreeView({ nodes, labels, changes }: TreeViewProps) {
  const roots = useMemo(() => buildTree(nodes), [nodes]);
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set());
  const [focusId, setFocusId] = useState<string | null>(null);
  const treeRef = useRef<HTMLUListElement>(null);
  const visible = flatten(roots, collapsed);
  const tabStop = visible.some((item) => item.id === focusId) ? focusId : (visible[0]?.id ?? null);

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
  const focusItem = (id: string | undefined) => {
    if (id !== undefined) {
      Array.from(treeRef.current?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])
        .find((element) => element.dataset.id === id)
        ?.focus();
    }
  };

  const onKeyDown = (event: KeyboardEvent<HTMLUListElement>) => {
    const id = (event.target as HTMLElement).closest<HTMLElement>('[role="treeitem"]')?.dataset.id;
    const index = visible.findIndex((item) => item.id === id);
    const current = visible[index];
    if (!current) {
      return;
    }
    const expanded = !collapsed.has(current.id);
    const handlers: Record<string, () => void> = {
      ArrowDown: () => focusItem(visible[index + 1]?.id),
      ArrowUp: () => focusItem(visible[index - 1]?.id),
      Home: () => focusItem(visible[0]?.id),
      End: () => focusItem(visible[visible.length - 1]?.id),
      ArrowRight: () => (current.hasChildren && !expanded ? toggle(current.id, true) : current.hasChildren ? focusItem(visible[index + 1]?.id) : undefined),
      ArrowLeft: () => (current.hasChildren && expanded ? toggle(current.id, false) : focusItem(current.parentId ?? undefined)),
    };
    const handler = handlers[event.key];
    if (handler) {
      event.preventDefault();
      handler();
    }
  };

  const renderNode = ({ node, children }: TreeNode, level: number) => {
    const id = node.orgUnitId;
    const label = labels.get(id);
    const hasChildren = children.length > 0;
    const expanded = !collapsed.has(id);
    const change = changes.get(id);
    const changeText = change ? (change.kind === "added" ? "Added in this version" : `Previously under ${labelText(change.oldParentOrgUnitId)}`) : "";
    return (
      <li
        key={id}
        role="treeitem"
        data-id={id}
        aria-level={level}
        aria-expanded={hasChildren ? expanded : undefined}
        aria-labelledby={`tree-label-${id}`}
        aria-describedby={change ? `tree-change-${id}` : undefined}
        tabIndex={id === tabStop ? 0 : -1}
        className={styles.treeItem}
        onFocus={(event) => event.target === event.currentTarget && setFocusId(id)}
      >
        <div className={styles.treeRow} onClick={() => hasChildren && toggle(id, !expanded)}>
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

const rangeText = (version: HierarchyVersionSummary) =>
  version.effectiveFrom === null ? "Not yet effective" : `${version.effectiveFrom} to ${version.effectiveTo ?? "open"}`;

const todayIso = () => new Date().toISOString().slice(0, 10);

export function HierarchyDesignerPage() {
  const { entities } = useLegalEntity();
  const [typeId, setTypeId] = useState("");
  const [chosenDate, setChosenDate] = useState<string | null>(null);
  const [showDraft, setShowDraft] = useState(false);

  const types = useLoad("types", (signal) => listHierarchyTypes({}, { signal }));
  const units = useLoad("units", (signal) => listOperatingUnits(ALL, { signal }));
  const versionsLoad = useLoad(typeId, (signal) => listHierarchyVersions(typeId, undefined, { signal }));
  const versions = versionsLoad.data ?? [];

  const labels = useMemo(() => {
    const map = new Map<string, Label>();
    for (const entity of entities) {
      map.set(entity.id, { name: entity.name, code: entity.code });
    }
    for (const unit of units.data?.items ?? []) {
      map.set(unit.id, { name: unit.name, code: unit.code });
    }
    return map;
  }, [entities, units.data]);

  const draft = versions.find((version) => version.status === "draft");
  const defaultDate = versions.find((version) => version.status !== "draft" && version.effectiveFrom !== null)?.effectiveFrom ?? "";
  const asOf = chosenDate ?? defaultDate;
  const displayed = showDraft && draft ? draft : versionAsOf(versions, asOf);
  const previous = displayed ? previousVersion(versions, displayed) : undefined;

  const detail = useLoad(displayed ? displayed.id : "", (signal) => getHierarchyVersion(displayed?.id ?? "", { signal }));
  const comparison = useLoad(displayed && previous ? `${typeId}:${previous.versionNo}:${displayed.versionNo}` : "", (signal) =>
    compareHierarchyVersions(typeId, previous?.versionNo ?? 0, displayed?.versionNo ?? 0, { signal }),
  );
  const changes = useMemo(() => (comparison.data ? changeMap(comparison.data) : new Map<string, NodeChange>()), [comparison.data]);

  const selectedType = types.data?.find((type) => type.id === typeId);
  const range = sliderRange(versions, dayNumber(asOf) === null ? todayIso() : asOf);
  const rangeStart = dayNumber(range.min) ?? 0;
  const sliderValue = Math.min(Math.max((dayNumber(asOf) ?? rangeStart) - rangeStart, 0), (dayNumber(range.max) ?? rangeStart) - rangeStart);

  const pickType = (id: string) => {
    setTypeId(id);
    setChosenDate(null);
    setShowDraft(false);
  };
  const pickDate = (value: string) => {
    if (dayNumber(value) !== null) {
      setChosenDate(value);
      setShowDraft(false);
    }
  };
  const pickVersion = (version: HierarchyVersionSummary) => {
    if (version.effectiveFrom === null) {
      setShowDraft(true);
    } else {
      setChosenDate(version.effectiveFrom);
      setShowDraft(false);
    }
  };

  const columns: DataTableColumn<HierarchyVersionSummary>[] = [
    { key: "versionNo", header: "Version", align: "end" },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    { key: "range", header: "Effective range", render: rangeText },
    { key: "nodeCount", header: "Nodes", align: "end" },
    {
      key: "view",
      header: "View",
      render: (row) => (
        <Button size="sm" aria-label={`View version ${row.versionNo}`} aria-pressed={displayed?.id === row.id} onClick={() => pickVersion(row)}>
          View
        </Button>
      ),
    },
  ];

  const banner = displayed
    ? displayed.effectiveFrom === null
      ? `Version ${displayed.versionNo} (draft), not yet effective`
      : `Version ${displayed.versionNo}, effective from ${displayed.effectiveFrom} to ${displayed.effectiveTo ?? "open"}`
    : "No version in force on this date";

  return (
    <>
      <PageHeader title="Hierarchies" description="Read-only view of hierarchy versions, as of a chosen date." />
      {types.error ? (
        <Alert tone="danger" title="Could not load hierarchy types" live="alert">
          {types.error.message} ({types.error.code})
        </Alert>
      ) : null}
      <div className={styles.filters}>
        <FormField id="hierarchy-type" label="Hierarchy type">
          {(control) => (
            <Select {...control} value={typeId} disabled={types.loading} onChange={(event) => pickType(event.target.value)}>
              <option value="">Select a hierarchy type</option>
              {(types.data ?? []).map((type) => (
                <option key={type.id} value={type.id}>
                  {type.code} · {type.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {selectedType ? (
          <div className={styles.purposes}>
            {selectedType.purposes.map((purpose) => (
              <Badge key={purpose} tone="neutral">
                {purpose}
              </Badge>
            ))}
          </div>
        ) : null}
      </div>
      {typeId === "" ? null : versionsLoad.error ? (
        <Alert tone="danger" title="Could not load versions" live="alert">
          {versionsLoad.error.message} ({versionsLoad.error.code})
        </Alert>
      ) : versionsLoad.loading ? (
        <Skeleton height="tile" />
      ) : versions.length === 0 ? (
        <EmptyState title="No versions yet" />
      ) : (
        <>
          <DataTable label="Hierarchy versions" columns={columns} rows={versions} getRowKey={(row) => row.id} />
          <div className={styles.sliderRow}>
            <FormField id="hierarchy-as-of" label="As of date">
              {(control) => <Input {...control} type="date" value={asOf} onChange={(event) => pickDate(event.target.value)} />}
            </FormField>
            <FormField id="hierarchy-slider" label="Effective date slider">
              {(control) => (
                <input
                  {...control}
                  type="range"
                  min={0}
                  max={(dayNumber(range.max) ?? rangeStart) - rangeStart}
                  value={sliderValue}
                  aria-valuetext={asOf || "No date"}
                  onChange={(event) => pickDate(dateFromDayNumber(rangeStart + Number(event.target.value)))}
                />
              )}
            </FormField>
            {draft ? (
              <label className={styles.filter}>
                <Input type="checkbox" checked={showDraft} onChange={(event) => setShowDraft(event.target.checked)} />
                <span>Show draft</span>
              </label>
            ) : null}
          </div>
          <div role="status" className={styles.banner}>
            {banner}
          </div>
          {displayed ? (
            <>
              {comparison.error ? (
                <Alert tone="danger" title="Could not load changes" live="alert">
                  {comparison.error.message} ({comparison.error.code})
                </Alert>
              ) : null}
              {detail.error ? (
                <Alert tone="danger" title="Could not load version" live="alert">
                  {detail.error.message} ({detail.error.code})
                </Alert>
              ) : detail.loading || !detail.data ? (
                <Skeleton height="tile" />
              ) : (
                <HierarchyTreeView key={detail.data.id} nodes={detail.data.nodes} labels={labels} changes={changes} />
              )}
            </>
          ) : null}
        </>
      )}
    </>
  );
}
