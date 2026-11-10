import { useMemo, useState, type ReactNode } from "react";
import { copy } from "../copy";
import { ProblemMessage } from "../errors";
import { formatOption, resolveOption, type LookupEntityKey } from "../lookup/entities";
import { useWorkContext } from "../lookup/workContextState";
import { localPreferenceStorage, type PreferenceStorage } from "../preferenceStorage";
import { Alert } from "./Alert";
import { Button } from "./Button";
import { downloadCsv, toCsv } from "./csv";
import { DataTable, type DataTableColumn } from "./DataTable";
import { EmptyState } from "./EmptyState";
import { ColumnChooser, FilterBar, FilterChips, ViewsMenu } from "./ListControls";
import styles from "./ListPage.module.css";
import {
  cleanFilters,
  defaultVisible,
  emptyQuery,
  filterValueKeys,
  inOrder,
  loadViewStore,
  reconcileView,
  sameQuery,
  saveViewStore,
  sortRows,
  viewStoreKey,
  type FilterValues,
  type ListFilterDef,
  type ListQuery,
  type SavedView,
  type ViewStore,
} from "./listViews";
import { PageHeader } from "./PageHeader";

export interface ListColumn<T> extends DataTableColumn<T> {
  sortValue?(row: T): unknown;
  csv?(row: T): string | number | null | Promise<string | number | null>;
  defaultHidden?: boolean;
}

export interface ListData<T> {
  rows: T[];
  loading: boolean;
  loadingMore: boolean;
  error?: unknown;
  forbidden?: boolean;
  hasMore: boolean;
  loadMore(): void;
  retry(): void;
}

export interface ListPageProps<T> {
  id: string;
  title: string;
  description?: string;
  actions?: ReactNode;
  noun: string;
  columns: ListColumn<T>[];
  filters?: ListFilterDef[];
  useRows(query: ListQuery): ListData<T>;
  getRowKey(row: T, index: number): string;
  onRowOpen?(row: T): void;
  empty: { title: string; description?: string; action?: ReactNode };
  serverSort?: string[];
  storage?: PreferenceStorage;
}

interface Start {
  store: ViewStore;
  query: ListQuery;
  visible: string[];
  activeId: string | null;
}

export async function entityText(entity: LookupEntityKey, id: string): Promise<string> {
  const found = id === "" ? null : await resolveOption(entity, id);
  return found ? formatOption(found) : id;
}

const NO_FILTERS: ListFilterDef[] = [];
const NO_SORTS: string[] = [];

function rawValue(row: unknown, key: string): unknown {
  return (row as Record<string, unknown>)[key];
}

export function ListPage<T>({
  id,
  title,
  description,
  actions,
  noun,
  columns,
  filters = NO_FILTERS,
  useRows,
  getRowKey,
  onRowOpen,
  empty,
  serverSort = NO_SORTS,
  storage = localPreferenceStorage,
}: ListPageProps<T>) {
  const { identity } = useWorkContext();
  const storeKey = viewStoreKey(identity, id);
  const filterKeys = useMemo(() => filters.flatMap(filterValueKeys), [filters]);

  const [start] = useState<Start>(() => {
    const store = loadViewStore(storage, storeKey);
    const preset = store.views.find((v) => v.id === store.defaultId);
    if (preset) {
      return { store, ...reconcileView(preset, columns, filterKeys), activeId: preset.id };
    }
    return { store, query: emptyQuery(), visible: defaultVisible(columns), activeId: null };
  });
  const [store, setStore] = useState(start.store);
  const [query, setQuery] = useState(start.query);
  const [draft, setDraft] = useState<FilterValues>(start.query.filters);
  const [visible, setVisible] = useState(start.visible);
  const [activeId, setActiveId] = useState(start.activeId);
  const [message, setMessage] = useState("");

  const data = useRows(query);
  const shownKeys = inOrder(columns, visible);
  const shownColumns = columns.filter((c) => shownKeys.includes(c.key));
  const tableColumns = shownColumns.map((c) => ({ ...c, sortable: c.sortable !== false }));
  const localSort = query.sort !== null && !serverSort.includes(query.sort.key);
  const rows = useMemo(() => {
    if (!localSort) {
      return data.rows;
    }
    const column = columns.find((c) => c.key === query.sort?.key);
    return sortRows(data.rows, query.sort, (row, key) => (column?.sortValue ? column.sortValue(row) : rawValue(row, key)));
  }, [data.rows, localSort, query.sort, columns]);

  const active = store.views.find((v) => v.id === activeId);
  const filtered = Object.keys(query.filters).length > 0;
  const activeBaseline = active ? reconcileView(active, columns, filterKeys) : null;
  const modified =
    activeBaseline !== null &&
    (!sameQuery(query, activeBaseline.query) || inOrder(columns, activeBaseline.visible).join() !== shownKeys.join());

  function commitStore(next: ViewStore, said: string) {
    setStore(next);
    setMessage(saveViewStore(storage, storeKey, next) ? said : copy.lists.storageFailed);
  }

  function setFilters(filtersNext: FilterValues) {
    setQuery((q) => ({ ...q, filters: filtersNext }));
    setDraft(filtersNext);
  }

  function cycleSort(key: string) {
    setQuery((q) => {
      if (q.sort?.key !== key) {
        return { ...q, sort: { key, direction: "ascending" } };
      }
      return { ...q, sort: q.sort.direction === "ascending" ? { key, direction: "descending" } : null };
    });
  }

  function toggleColumn(key: string) {
    setVisible((current) => (current.includes(key) ? current.filter((k) => k !== key) : [...current, key]));
  }

  function snapshot(view: Pick<SavedView, "id" | "name">): SavedView {
    return { ...view, query, visible: shownKeys, known: columns.map((c) => c.key) };
  }

  function applyView(view: SavedView) {
    const resolved = reconcileView(view, columns, filterKeys);
    setQuery(resolved.query);
    setDraft(resolved.query.filters);
    setVisible(resolved.visible);
    setActiveId(view.id);
    setMessage(copy.lists.viewApplied(view.name));
  }

  const viewActions = {
    onApply: (viewId: string) => {
      const view = store.views.find((v) => v.id === viewId);
      if (view) {
        applyView(view);
      }
    },
    onSaveNew: (name: string) => {
      const view = snapshot({ id: crypto.randomUUID(), name });
      commitStore({ ...store, views: [...store.views, view] }, copy.lists.viewSaved(name));
      setActiveId(view.id);
    },
    onUpdate: (viewId: string) => {
      const current = store.views.find((v) => v.id === viewId);
      if (current) {
        commitStore({ ...store, views: store.views.map((v) => (v.id === viewId ? snapshot(current) : v)) }, copy.lists.viewUpdated(current.name));
      }
    },
    onDelete: (viewId: string) => {
      const current = store.views.find((v) => v.id === viewId);
      if (current) {
        commitStore(
          { views: store.views.filter((v) => v.id !== viewId), defaultId: store.defaultId === viewId ? null : store.defaultId },
          copy.lists.viewDeleted(current.name),
        );
        if (activeId === viewId) {
          setActiveId(null);
        }
      }
    },
    onSetDefault: (viewId: string | null) => {
      const current = store.views.find((v) => v.id === viewId);
      commitStore({ ...store, defaultId: viewId }, current ? copy.lists.defaultSet(current.name) : copy.lists.defaultCleared);
    },
  };

  async function exportCsv() {
    if (rows.length === 0) {
      setMessage(copy.lists.exportNothing);
      return;
    }
    const records = await Promise.all(
      rows.map((row) => Promise.all(shownColumns.map((c) => (c.csv ? c.csv(row) : (rawValue(row, c.key) as string | number | null | undefined) ?? "")))),
    );
    downloadCsv(`${id}-${new Date().toISOString().slice(0, 10)}.csv`, toCsv(shownColumns.map((c) => c.header), records));
    setMessage(data.hasMore ? copy.lists.exportedPartial(rows.length, noun) : copy.lists.exported(rows.length, noun));
  }

  const blocked = data.forbidden === true;
  const failed = data.error !== undefined && data.error !== null && !blocked;
  const showTable = !blocked && (data.loading || rows.length > 0);

  return (
    <div className={styles.page}>
      <PageHeader title={title} description={description} actions={actions} />
      {blocked ? null : (
        <>
          {filters.length > 0 ? (
            <FilterBar
              listId={id}
              label={`${copy.lists.filtersLabel} ${noun}`}
              defs={filters}
              draft={draft}
              onDraftChange={setDraft}
              onApply={() => setQuery((q) => ({ ...q, filters: cleanFilters(filters, draft) }))}
            />
          ) : null}
          <FilterChips defs={filters} applied={query.filters} onChange={setFilters} onClearAll={() => setFilters({})} />
          <div className={styles.tools} role="group" aria-label={copy.lists.tableTools}>
            <p className={styles.status} role="status">
              {data.loading ? "" : copy.lists.loaded(rows.length, noun, data.hasMore)}
            </p>
            <span className={styles.grow} />
            <div className={styles.toolsGroup}>
              <ViewsMenu store={store} active={active} modified={modified} actions={viewActions} />
              <ColumnChooser
                columns={columns}
                visible={shownKeys}
                onToggle={toggleColumn}
                onReset={() => setVisible(defaultVisible(columns))}
              />
              <Button size="sm" onClick={() => void exportCsv()}>
                {copy.lists.exportCsv}
              </Button>
            </div>
          </div>
          <p className={styles.status} role="status">
            {message}
          </p>
        </>
      )}
      {blocked ? (
        <Alert tone="danger" title={copy.lists.forbidden(noun)} live="alert">
          {copy.lists.forbiddenBody}
        </Alert>
      ) : failed ? (
        <Alert tone="danger" title={copy.lists.loadFailed(noun)} live="alert" action={<Button size="sm" onClick={data.retry}>{copy.lists.retry}</Button>}>
          <ProblemMessage problem={data.error} />
        </Alert>
      ) : null}
      {showTable ? (
        <DataTable
          label={title}
          columns={tableColumns}
          rows={rows}
          getRowKey={getRowKey}
          loading={data.loading}
          onRowOpen={onRowOpen}
          sort={query.sort}
          onSortChange={cycleSort}
        />
      ) : !blocked && !failed ? (
        filtered ? (
          <EmptyState
            title={copy.lists.noMatchTitle(noun)}
            description={copy.lists.noMatchBody}
            action={<Button onClick={() => setFilters({})}>{copy.lists.clearFilters}</Button>}
          />
        ) : (
          <EmptyState title={empty.title} description={empty.description} action={empty.action} />
        )
      ) : null}
      {localSort && data.hasMore && rows.length > 0 ? <p className={styles.status}>{copy.lists.sortedLoadedOnly(rows.length)}</p> : null}
      {data.hasMore && !blocked ? (
        <div className={styles.toolsGroup}>
          <Button loading={data.loadingMore} onClick={data.loadMore}>
            {copy.lists.loadMore(noun)}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
