import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listRoutePlans, type RoutePlanSummary } from "./api/client";
import { formatDateTime, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** "12.5 km": a solver distance, to one decimal. */
export const km = (value: number) => `${value.toFixed(1)} km`;

const planColumns: DataTableColumn<RoutePlanSummary>[] = [
  { key: "id", header: "Plan", mono: true, render: (row) => <span title={row.id}>{shortId(row.id)}</span> },
  { key: "stops", header: "Stops", align: "end", render: (row) => String(row.stopCount) },
  { key: "routes", header: "Vehicles used", align: "end", render: (row) => `${row.routeCount} of ${row.vehicleCount}` },
  {
    key: "dropped",
    header: "Dropped",
    render: (row) => (row.droppedCount > 0 ? <Badge tone="warning">{`${row.droppedCount} dropped`}</Badge> : <Badge tone="success">All served</Badge>),
  },
  { key: "distance", header: "Distance", align: "end", render: (row) => km(row.totalDistanceKm) },
  { key: "createdBy", header: "Planned by" },
  { key: "createdAt", header: "Planned", render: (row) => formatDateTime(row.createdAt) },
];

/** Route plans solved by the VRP optimizer (FR-34.2), newest first. */
export function RoutePlansPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listRoutePlans(signal), "route-plans");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Route plans"
        description="Multi-stop routes planned within vehicle capacity, time windows and route duration. Stops that cannot be served are dropped and reported, never left out silently."
        actions={
          <Link className={buttonClassName("primary")} to="/transport/routes/new">
            Optimize routes
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the route plans" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Route plans"
          columns={planColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No route plans yet", description: "Optimize routes for a set of stops and vehicles to plan them." }}
          onRowOpen={(row) => navigate(`/transport/routes/${row.id}`)}
        />
      )}
    </div>
  );
}
