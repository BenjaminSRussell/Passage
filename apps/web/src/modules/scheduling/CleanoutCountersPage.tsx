import { Link } from "react-router-dom";
import { Alert, Button, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listCleanoutCounters, type CleanoutCounterRow } from "./api/client";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Batches and run minutes since the last cleanout of every resource (FR-23.15, BR-23.13). */
export function CleanoutCountersPage() {
  const state = useLoad((signal) => listCleanoutCounters(signal), "counters");

  const columns: DataTableColumn<CleanoutCounterRow>[] = [
    { key: "resourceCode", header: "Resource", mono: true },
    { key: "resourceName", header: "Name" },
    { key: "batchesSince", header: "Batches since", align: "end" },
    { key: "minutesSince", header: "Run minutes since", align: "end" },
    { key: "lastResetAt", header: "Last reset", render: (row) => (row.lastResetAt ? formatDateTime(row.lastResetAt) : dash) },
    {
      key: "open",
      header: "Detail",
      render: (row) => (
        <Link to={`/scheduling/cleanout-counters/${row.resourceId}`} aria-label={`Cleanout counter of ${row.resourceCode}`}>
          Open
        </Link>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cleanout counters"
        description="What each resource has run since its last cleanout. A campaign is sequenced from this counter and moves it when it starts. After a cleanout done outside the plan, reset the counter on the resource's page."
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the cleanout counters" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Cleanout counters"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.resourceId}
          loading={state.loading}
          empty={{ title: "No resources", description: "Add a resource to follow its cleanouts." }}
        />
      )}
    </div>
  );
}
