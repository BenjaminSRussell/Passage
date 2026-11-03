import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listPackCatalog, type CatalogEntry } from "./api/client";
import { SOURCE_LABELS, type Pack } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Where a run of an available pack is started. */
const startPath: Partial<Record<Pack, string>> = {
  open_items: "/migration-factory/runs/new",
  gl_balances: "/migration-factory/runs/new-gl",
  onhand: "/migration-factory/runs/new-onhand",
  bank_balances: "/migration-factory/runs/new-bank",
  project_wip: "/migration-factory/runs/new-project",
};

const columns: DataTableColumn<CatalogEntry>[] = [
  { key: "pack", header: "Pack", mono: true },
  { key: "sourceSystems", header: "Legacy systems", render: (row) => row.sourceSystems.map((s) => SOURCE_LABELS[s] ?? s).join(", ") },
  { key: "controlTotals", header: "Control totals (FR-49.2)" },
  {
    key: "available",
    header: "This release",
    render: (row) => (row.available ? <Badge tone="success">Available</Badge> : <Badge tone="neutral">Later slice</Badge>),
  },
  {
    key: "action",
    header: "Action",
    render: (row) => {
      const to = row.available ? startPath[row.pack] : undefined;
      return to ? (
        <Link className={buttonClassName("secondary", "sm")} to={to} aria-label={`Start ${row.pack} run`}>
          Start run
        </Link>
      ) : null;
    },
  },
];

/** Migration factory workspace catalog: the nine transactional packs for AX and SAP (FR-49.1, ticket "Deploy"). */
export function PackCatalogPage() {
  const state = useLoad((signal) => listPackCatalog(signal), "catalog");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Transactional packs"
        description="The nine packs that move open items, balances and work in progress off AX 2012, D365 F&O, SAP ECC and S/4HANA, with the control totals each reconciles."
        actions={
          <Link className={buttonClassName("ghost")} to="/migration-factory">
            Pack runs
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the pack catalog" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Transactional packs"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.pack}
          loading={state.loading}
          empty={{ title: "No packs", description: "The service lists no transactional packs." }}
        />
      )}
    </div>
  );
}
