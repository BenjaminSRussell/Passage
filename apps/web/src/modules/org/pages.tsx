import { useEffect, useRef, useState } from "react";
import { Outlet } from "react-router-dom";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, DataTable, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  ALL,
  describeProblem,
  listLegalEntities,
  listOperatingUnits,
  listSites,
  listWarehouses,
  type LegalEntity,
  type ListOptions,
  type OperatingUnit,
  type Page,
  type Problem,
  type Scope,
  type Site,
  type Warehouse,
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

interface ScopedListPageProps<T> {
  title: string;
  label: string;
  emptyTitle: string;
  columns: DataTableColumn<T>[];
  getRowKey(row: T): string;
  load(scope: Scope, options: ListOptions): Promise<Page<T>>;
  scoped?: boolean;
}

export function ScopedListPage<T>({ title, label, emptyTitle, columns, getRowKey, load, scoped = true }: ScopedListPageProps<T>) {
  const { scope: selected, entities, loading: entitiesLoading } = useLegalEntity();
  const scope = scoped ? selected : ALL;
  const waiting = scoped && entitiesLoading;
  const [state, setState] = useState<ListState<T>>({ rows: [], loading: true, error: null });
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
  }, [scope, waiting]);

  const scopeName = scope === ALL ? "All entities" : (entities.find((entity) => entity.id === scope)?.code ?? dash);
  return (
    <>
      <PageHeader
        title={title}
        description={scoped ? `Scope: ${scopeName}` : undefined}
        meta={scoped ? <span role="status">{scope === ALL ? <Badge tone="info">Read-only, all entities</Badge> : null}</span> : undefined}
      />
      {state.error ? (
        <Alert tone="danger" title={`Could not load ${label.toLowerCase()}`} live="alert">
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      <DataTable label={label} columns={columns} rows={state.rows} getRowKey={getRowKey} loading={state.loading} empty={{ title: emptyTitle }} />
    </>
  );
}

const loadAllEntities = (_scope: Scope, options: ListOptions) => listLegalEntities(ALL, options);

const legalEntityColumns: DataTableColumn<LegalEntity>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "country", header: "Country" },
  { key: "currency", header: "Accounting currency" },
  { key: "timeZone", header: "Time zone" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "version", header: "Version", align: "end" },
];

export function LegalEntitiesPage() {
  return (
    <ScopedListPage
      scoped={false}
      title="Legal entities"
      label="Legal entities"
      emptyTitle="No legal entities"
      columns={legalEntityColumns}
      getRowKey={(row) => row.id}
      load={loadAllEntities}
    />
  );
}

function useEntityCode(): (id?: string) => string {
  const { entities } = useLegalEntity();
  return (id) => (id ? (entities.find((entity) => entity.id === id)?.code ?? id) : dash);
}

export function OperatingUnitsPage() {
  const entityCode = useEntityCode();
  const columns: DataTableColumn<OperatingUnit>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "name", header: "Name" },
    { key: "type", header: "Type" },
    { key: "defaultLegalEntityId", header: "Default legal entity", render: (row) => entityCode(row.defaultLegalEntityId) },
  ];
  return (
    <ScopedListPage
      title="Operating units"
      label="Operating units"
      emptyTitle="No operating units"
      columns={columns}
      getRowKey={(row) => row.id}
      load={listOperatingUnits}
    />
  );
}

export function SitesPage() {
  const entityCode = useEntityCode();
  const columns: DataTableColumn<Site>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "name", header: "Name" },
    { key: "legalEntityId", header: "Legal entity", render: (row) => entityCode(row.legalEntityId) },
    { key: "timeZone", header: "Time zone" },
    { key: "defaultWarehouseId", header: "Default warehouse", render: (row) => row.defaultWarehouseId ?? dash },
  ];
  return (
    <ScopedListPage title="Sites" label="Sites" emptyTitle="No sites" columns={columns} getRowKey={(row) => row.id} load={listSites} />
  );
}

export function WarehousesPage() {
  const columns: DataTableColumn<Warehouse>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "name", header: "Name" },
    { key: "siteId", header: "Site" },
    { key: "type", header: "Type" },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  ];
  return (
    <ScopedListPage
      title="Warehouses"
      label="Warehouses"
      emptyTitle="No warehouses"
      columns={columns}
      getRowKey={(row) => row.id}
      load={listWarehouses}
    />
  );
}
