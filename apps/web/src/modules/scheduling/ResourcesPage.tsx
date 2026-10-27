import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listResources, type Resource } from "./api/client";
import { describeShifts } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Resource>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "finite", header: "Capacity", render: (row) => <Badge tone={row.finite ? "info" : "neutral"}>{row.finite ? "Finite" : "Infinite"}</Badge> },
  { key: "timeZone", header: "Time zone" },
  { key: "shifts", header: "Calendar", render: (row) => describeShifts(row.shifts) },
];

/** Resources the scheduler places work on, with their weekly calendars. */
export function ResourcesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listResources(signal), "resources");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Resources"
        description="Machines and work centers with the weekly calendar that defines their open capacity."
        actions={
          <Link className={buttonClassName("primary")} to="/scheduling/resources/new">
            New resource
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load resources" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Resources"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No resources yet", description: "Add a resource and its working calendar before you schedule." }}
          onRowOpen={(row) => navigate(`/scheduling/resources/${row.id}`)}
        />
      )}
    </div>
  );
}
