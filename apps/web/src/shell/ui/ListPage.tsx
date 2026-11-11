import { useId, useMemo, useRef, useState, type ReactNode } from "react";
import { copy } from "../copy";
import { ProblemMessage } from "../errors";
import { filterBySearch, type SearchText } from "../search/match";
import { formatOption, resolveOption, type LookupEntityKey } from "../lookup/entities";
import { useWorkContext } from "../lookup/workContextState";
import { localPreferenceStorage, noPreferenceStorage, type PreferenceStorage } from "../preferenceStorage";
import { Alert } from "./Alert";
import { Button } from "./Button";
import { downloadCsv, toCsv } from "./csv";
import { DataTable, type DataTableColumn } from "./DataTable";
import { EmptyState } from "./EmptyState";
import { Input } from "./Input";
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
  csvWith?: string;
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
  singular?: string;
  columns: ListColumn<T>[];
  filters?: ListFilterDef[];
  useRows(query: ListQuery): ListData<T>;
  getRowKey(row: T, index: number): string;
  onRowOpen?(row: T): void;
  empty: { title: string; description?: string; action?: ReactNode };
  serverSort?: string[];
  /**
   * Adds a search box over the rows loaded so far. It matches the way the record search service does
   * (shell/search/match.ts): a number by its start, a name by what it contains, a typo forgiven. Say what a row is
   * searched by; a row without a name is searched by its number alone.
   */
  search?(row: T): SearchText;
  storage?: PreferenceStorage;
}

interface Start {
  store: ViewStore;
  query: ListQuery;
  visible: string[];
  activeId: string | null;
}

/** What a page that loads its whole list itself (one request, no paging) hands to ListPage. */
export interface LoadedList<T> {
  data: T[] | null | undefined;
  loading: boolean;
  error?: unknown;
  reload(): void;
}

/**
 * ListData for a list the page already loaded in full, such as a setup list it also reloads after a save. Pass it as
 * `useRows={() => loadedRows(state)}`; filters and sort then work on the loaded rows.
 */
export function loadedRows<T>(state: LoadedList<T>, forbidden = false): ListData<T> {
  return {
    rows: state.error ? [] : (state.data ?? []),
    loading: state.loading && !state.data,
    loadingMore: false,
    error: state.error ? state.error : undefined,
    forbidden,
    hasMore: false,
    loadMore: () => undefined,
    retry: state.reload,
  };
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
  singular = noun,
  columns,
  filters = NO_FILTERS,
  useRows,
  getRowKey,
  onRowOpen,
  empty,
  serverSort = NO_SORTS,
  search,
  storage: preferred = localPreferenceStorage,
}: ListPageProps<T>) {
  const { identity } = useWorkContext();
  const storage = identity === "" ? noPreferenceStorage : preferred;
  const filterRef = useRef<HTMLDivElement>(null);
  const sortNoteId = useId();
  const storeKey = viewStoreKey(identity, id);
  const filterKeys = useMemo(() => filters.flatMap(filterValueKeys), [filters]);

  const [start] = useState<Start>(() => {
    const store = loadViewStore(storage, storeKey);
    const preset = store.views.find((v) => v.id === store.defaultId);
    if (preset) {
      return { store, ...reconcileView(preset, columns, filterKeys, filters), activeId: preset.id };
    }
    return { store, query: emptyQuery(), visible: defaultVisible(columns), activeId: null };
  });
  const [store, setStore] = useState(start.store);
  const [query, setQuery] = useState(start.query);
  const [draft, setDraft] = useState<FilterValues>(start.query.filters);
  const [visible, setVisible] = useState(start.visible);
  const [activeId, setActiveId] = useState(start.activeId);
  const [message, setMessage] = useState("");
  const [searchText, setSearchText] = useState("");
  const [undo, setUndo] = useState<{ view: SavedView; index: number; wasDefault: boolean; wasActive: boolean } | null>(null);

  const data = useRows(query);
  const nounFor = (count: number) => (count === 1 ? singular : noun);
  const shownKeys = inOrder(columns, visible);
  const shownColumns = columns.filter((c) => shownKeys.includes(c.key));
  const tableColumns = shownColumns.map((c) => ({ ...c, sortable: c.sortable !== false }));
  const localSort = query.sort !== null && !serverSort.includes(query.sort.key);
  const sorted = useMemo(() => {
    if (!localSort) {
      return data.rows;
    }
    const column = columns.find((c) => c.key === query.sort?.key);
    // An amount with a currency column sorts within each currency, never across them.
    const group = column?.csvWith;
    return sortRows(data.rows, query.sort, (row, key) => (column?.sortValue ? column.sortValue(row) : rawValue(row, key)), group ? (row) => rawValue(row, group) : undefined);
  }, [data.rows, localSort, query.sort, columns]);
  const searching = search !== undefined && searchText.trim() !== "";
  const rows = useMemo(() => (search && searching ? filterBySearch(sorted, searchText, search) : sorted), [sorted, search, searching, searchText]);

  const active = store.views.find((v) => v.id === activeId);
  const filtered = Object.keys(query.filters).length > 0;
  const activeBaseline = active ? reconcileView(active, columns, filterKeys, filters) : null;
  const modified =
    activeBaseline !== null &&
    (!sameQuery(query, activeBaseline.query) || inOrder(columns, activeBaseline.visible).join() !== shownKeys.join());

  function commitStore(next: ViewStore, said: string, keepUndo = false) {
    if (!keepUndo) {
      setUndo(null);
    }
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

  function showColumns(next: string[]) {
    setVisible(next);
    if (query.sort && !next.includes(query.sort.key)) {
      setQuery((q) => ({ ...q, sort: null }));
    }
  }

  function toggleColumn(key: string) {
    showColumns(shownKeys.includes(key) ? shownKeys.filter((k) => k !== key) : [...shownKeys, key]);
  }

  function focusFilters() {
    filterRef.current?.querySelector<HTMLElement>("input, select")?.focus();
  }

  function clearFilters() {
    setFilters({});
    focusFilters();
  }

  function snapshot(view: Pick<SavedView, "id" | "name">): SavedView {
    return { ...view, query, visible: shownKeys, known: columns.map((c) => c.key) };
  }

  function applyView(view: SavedView) {
    const resolved = reconcileView(view, columns, filterKeys, filters);
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
      const view = snapshot({ id: globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`, name });
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
          true,
        );
        setUndo({ view: current, index: store.views.indexOf(current), wasDefault: store.defaultId === viewId, wasActive: activeId === viewId });
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

  function undoDelete() {
    if (!undo) {
      return;
    }
    const views = [...store.views];
    views.splice(Math.min(undo.index, views.length), 0, undo.view);
    commitStore({ views, defaultId: undo.wasDefault ? undo.view.id : store.defaultId }, copy.lists.viewRestored(undo.view.name));
    if (undo.wasActive) {
      setActiveId(undo.view.id);
    }
  }

  async function exportCsv() {
    if (rows.length === 0) {
      setMessage(copy.lists.exportNothing);
      return;
    }
    const exported = shownColumns.flatMap((c) => {
      const companion = c.csvWith && !shownKeys.includes(c.csvWith) ? columns.find((o) => o.key === c.csvWith) : undefined;
      return companion ? [c, companion] : [c];
    });
    const records = await Promise.all(
      rows.map((row) => Promise.all(exported.map((c) => (c.csv ? c.csv(row) : (rawValue(row, c.key) as string | number | null | undefined) ?? "")))),
    );
    downloadCsv(`${id}-${new Date().toISOString().slice(0, 10)}.csv`, toCsv(exported.map((c) => c.header), records));
    const exportedNoun = nounFor(rows.length);
    setMessage(data.hasMore ? copy.lists.exportedPartial(rows.length, exportedNoun) : copy.lists.exported(rows.length, exportedNoun));
  }

  const searchNote = searching && data.hasMore && data.rows.length > 0 ? copy.lists.searchLoadedOnly(data.rows.length) : "";
  const blocked = data.forbidden === true;
  const sortNote = localSort && data.hasMore && rows.length > 0 ? copy.lists.sortedLoadedOnly(rows.length) : "";
  const failed = data.error !== undefined && data.error !== null && !blocked;
  const showTable = !blocked && (data.loading || rows.length > 0);

  return (
    <div className={styles.page}>
      <PageHeader title={title} description={description} actions={actions} />
      {blocked ? null : (
        <>
          {search ? (
            <div className={styles.searchBox} role="search" aria-label={copy.lists.searchLabel(noun)}>
              <label htmlFor={`${id}-search`} className="visually-hidden">
                {copy.lists.searchLabel(noun)}
              </label>
              <Input
                id={`${id}-search`}
                type="search"
                value={searchText}
                placeholder={copy.lists.searchPlaceholder}
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => setSearchText(e.target.value)}
              />
              {searching ? (
                <Button size="sm" onClick={() => setSearchText("")}>
                  {copy.lists.clearSearch}
                </Button>
              ) : null}
            </div>
          ) : null}
          {filters.length > 0 ? (
            <div ref={filterRef}>
              <FilterBar
                listId={id}
                label={`${copy.lists.filtersLabel} ${noun}`}
                defs={filters}
                draft={draft}
                onDraftChange={setDraft}
                onApply={() => setQuery((q) => ({ ...q, filters: cleanFilters(filters, draft) }))}
              />
            </div>
          ) : null}
          <FilterChips defs={filters} applied={query.filters} onChange={setFilters} onClearAll={clearFilters} onEmptied={focusFilters} />
          <div className={styles.tools} role="group" aria-label={copy.lists.tableTools}>
            <p className={styles.status} role="status">
              {data.loading ? copy.lists.loading(noun) : copy.lists.loaded(rows.length, nounFor(rows.length), data.hasMore)}
              {sortNote ? (
                <>
                  {" "}
                  <span id={sortNoteId}>{sortNote}</span>
                </>
              ) : null}
              {searchNote ? ` ${searchNote}` : null}
            </p>
            <span className={styles.grow} />
            <div className={styles.toolsGroup}>
              <ViewsMenu store={store} active={active} modified={modified} actions={viewActions} />
              <ColumnChooser
                columns={columns}
                visible={shownKeys}
                onToggle={toggleColumn}
                onReset={() => showColumns(defaultVisible(columns))}
              />
              <Button size="sm" onClick={() => void exportCsv()}>
                {copy.lists.exportCsv}
              </Button>
            </div>
          </div>
          <p className={styles.status} role="status">
            {message}
            {undo ? (
              <>
                {" "}
                <Button size="sm" onClick={undoDelete}>
                  {copy.lists.undoDelete}
                </Button>
              </>
            ) : null}
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
          sortDescribedBy={sortNote ? sortNoteId : undefined}
        />
      ) : !blocked && !failed ? (
        searching && data.rows.length > 0 ? (
          <EmptyState
            title={copy.lists.noSearchMatchTitle(noun)}
            description={copy.lists.noSearchMatchBody}
            action={<Button onClick={() => setSearchText("")}>{copy.lists.clearSearch}</Button>}
          />
        ) : filtered ? (
          <EmptyState
            title={copy.lists.noMatchTitle(noun)}
            description={copy.lists.noMatchBody}
            action={<Button onClick={clearFilters}>{copy.lists.clearFilters}</Button>}
          />
        ) : (
          <EmptyState title={empty.title} description={empty.description} action={empty.action} />
        )
      ) : null}
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
