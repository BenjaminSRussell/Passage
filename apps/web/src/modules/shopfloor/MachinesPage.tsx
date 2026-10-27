import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listMachines, type Machine } from "./api/client";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Machine>[] = [
  { key: "machineId", header: "Machine", mono: true },
  { key: "resourceId", header: "Resource", mono: true },
  { key: "siteId", header: "Site", mono: true, render: (row) => row.siteId ?? dash },
  { key: "idealCycleSeconds", header: "Ideal cycle", align: "end", render: (row) => `${row.idealCycleSeconds} s` },
  { key: "lastSeq", header: "Last sequence", align: "end", render: (row) => (row.lastSeq > 0 ? String(row.lastSeq) : dash) },
  { key: "createdAt", header: "Registered", render: (row) => formatDateTime(row.createdAt) },
];

/** Machines the edge connector reports for, each mapped to one resource (FR-25.9, machine setup). */
export function MachinesPage() {
  const state = useLoad((signal) => listMachines(signal), "machines");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Machines"
        description="Edge machine codes mapped to the resources whose OEE and downtime they report. The ideal cycle time sets the performance target."
        actions={
          <Link className={buttonClassName("primary")} to="/shopfloor/machines/new">
            Register machine
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load machines" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Machines"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No machines yet", description: "Register a machine before its edge connector sends events; events for unknown machines are refused." }}
        />
      )}
    </div>
  );
}
