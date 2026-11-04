import { useEffect, useRef, useState, type ReactNode } from "react";
import { Outlet } from "react-router-dom";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  ALL,
  describeProblem,
  type ListOptions,
  type Page,
  type Problem,
  type Scope,
} from "./api/client";
import { LegalEntitySwitcher } from "./LegalEntitySwitcher";
import { LegalEntityProvider, useLegalEntity } from "./legalEntityContext";
import styles from "./pages.module.css";

const dash = "—";

function OrgFrame() {
  const { error } = useLegalEntity();
  return (
    <div className={styles.layout}>
      <div className={styles.toolbar}>
        <LegalEntitySwitcher />
      </div>
      {error ? (
        <Alert tone="danger" title="Could not load legal entities" live="alert">
          {error.message} ({error.code})
        </Alert>
      ) : null}
      <Outlet />
    </div>
  );
}

export function OrgLayout({ duties }: { duties?: readonly string[] }) {
  const principalDuties = usePrincipalDuties();
  return (
    <LegalEntityProvider duties={duties ?? principalDuties}>
      <OrgFrame />
    </LegalEntityProvider>
  );
}

export function OrgHome() {
  return <PageHeader title="Organization" description="Legal entities, operating units, sites and warehouses." />;
}

interface ListState<T> {
  rows: T[];
  loading: boolean;
  error: Problem | null;
}

export interface ListControls<T> {
  reload(): void;
  update(change: (rows: T[]) => T[]): void;
}

interface ScopedListPageProps<T> {
  title: string;
  label: string;
  emptyTitle: string;
  columns: DataTableColumn<T>[] | ((rows: ListControls<T>) => DataTableColumn<T>[]);
  getRowKey(row: T): string;
  // Rendered between the header and the table, for create forms; receives the same controls as `columns`.
  toolbar?: (rows: ListControls<T>) => ReactNode;
  load(scope: Scope, options: ListOptions): Promise<Page<T>>;
  scoped?: boolean;
}

export function ScopedListPage<T>({ title, label, emptyTitle, columns, getRowKey, load, toolbar, scoped = true }: ScopedListPageProps<T>) {
  const { scope: selected, entities, loading: entitiesLoading } = useLegalEntity();
  const scope = scoped ? selected : ALL;
  const waiting = scoped && entitiesLoading;
  const [state, setState] = useState<ListState<T>>({ rows: [], loading: true, error: null });
  const [reloads, setReloads] = useState(0);
  const latest = useRef(0);

  useEffect(() => {
    const token = ++latest.current;
    if (waiting || scope === "") {
      setState({ rows: [], loading: waiting, error: null });
      return;
    }
    const controller = new AbortController();
    setState({ rows: [], loading: true, error: null });
    load(scope, { signal: controller.signal }).then(
      ({ items }) => {
        if (token === latest.current) {
          setState({ rows: items, loading: false, error: null });
        }
      },
      (failure) => {
        if (token === latest.current) {
          setState({ rows: [], loading: false, error: describeProblem(failure) });
        }
      },
    );
    return () => controller.abort();
  }, [scope, waiting, reloads]);

  const controls: ListControls<T> = {
    reload: () => setReloads((value) => value + 1),
    update: (change) => setState((current) => ({ ...current, rows: change(current.rows) })),
  };

  const scopeName = scope === ALL ? "All entities" : (entities.find((entity) => entity.id === scope)?.code ?? dash);
  return (
    <>
      <PageHeader
        title={title}
        description={scoped ? `Scope: ${scopeName}` : undefined}
        meta={scoped ? <span role="status">{scope === ALL ? <Badge tone="info">Viewing all entities</Badge> : null}</span> : undefined}
      />
      {toolbar ? toolbar(controls) : null}
      {state.error ? (
        <Alert tone="danger" title={`Could not load ${label.toLowerCase()}`} live="alert">
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      <DataTable label={label} columns={typeof columns === "function" ? columns(controls) : columns} rows={state.rows} getRowKey={getRowKey} loading={state.loading} empty={{ title: emptyTitle }} />
    </>
  );
}
