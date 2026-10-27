import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listWorkers, type Worker } from "./api/client";
import { dash, fixed } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Worker>[] = [
  { key: "name", header: "Name" },
  { key: "badgeId", header: "Badge", mono: true, render: (row) => row.badgeId ?? dash },
  { key: "costRate", header: "Cost rate", align: "end", render: (row) => `${fixed(row.costRate, 2)} ${row.currency}/h` },
  { key: "active", header: "Status", render: (row) => <StatusPill status={row.active ? "active" : "inactive"} /> },
];

/** Workers who register time on the shop floor (PRD 4.25.4 Worker). */
export function WorkersPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listWorkers({}, signal), "workers");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Workers"
        description="People who clock in and register time on the shop floor, with the cost rate their hours are paid and costed at."
        actions={
          <Link className={buttonClassName("primary")} to="/shopfloor/workers/new">
            New worker
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load workers" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Workers"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No workers yet", description: "Add the people who register time before they clock in on a kiosk." }}
          onRowOpen={(row) => navigate(`/shopfloor/workers/${row.id}`)}
        />
      )}
    </div>
  );
}
