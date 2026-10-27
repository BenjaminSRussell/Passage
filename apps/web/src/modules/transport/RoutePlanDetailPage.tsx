import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, PageHeader, Skeleton } from "../../shell/ui";
import { getRoutePlan, type RoutePlan } from "./api/client";
import { Details } from "./Details";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { km } from "./RoutePlansPage";
import { useLoad } from "./useLoad";

/** One vehicle's route in stop order, with arrival and lateness per stop. */
function RouteCard({ plan, index }: { plan: RoutePlan; index: number }) {
  const route = plan.solution.routes[index];
  const windows = new Map(plan.request.stops.map((s) => [s.id, s]));
  return (
    <Card title={`Vehicle ${route.vehicleId}`} headingLevel={3}>
      <div className={styles.stack}>
        <p className={styles.note}>
          {route.stops.length} {route.stops.length === 1 ? "stop" : "stops"}, {km(route.distanceKm)}, back at the depot after {route.endMin} min.
        </p>
        <DataTable
          label={`Route of vehicle ${route.vehicleId}`}
          columns={[
            { key: "seq", header: "#", align: "end" },
            { key: "stopId", header: "Stop" },
            {
              key: "window",
              header: "Window",
              render: (row) => {
                const w = windows.get(row.stopId);
                return w ? `${w.earliest}–${w.latest} min` : "—";
              },
            },
            { key: "arrivalMin", header: "Arrival", align: "end", render: (row) => `${row.arrivalMin} min` },
            { key: "lateMin", header: "Late", render: (row) => (row.lateMin > 0 ? <Badge tone="warning">{`${row.lateMin} min late`}</Badge> : <Badge tone="success">On time</Badge>) },
          ]}
          rows={route.stops.map((stop, i) => ({ ...stop, seq: i + 1 }))}
          getRowKey={(row) => row.stopId}
          empty={{ title: "Not used", description: "The optimizer did not need this vehicle." }}
        />
      </div>
    </Card>
  );
}

/** One route plan: the routes per vehicle and the stops that could not be served (FR-34.2, BR-34.1). */
export function RoutePlanDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getRoutePlan(id, signal), `route-plan:${id}`);
  const plan = state.data;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Route plan" />
        <Alert tone="danger" title="Could not load the route plan" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/transport/routes">Back to route plans</Link>
      </div>
    );
  }
  if (!plan) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Route plan" />
        <Card>
          <p className={styles.note}>Loading the route plan…</p>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  const used = plan.solution.routes.map((r, i) => ({ r, i })).filter(({ r }) => r.stops.length > 0);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Route plan ${plan.id.slice(0, 8)}`}
        meta={plan.droppedCount > 0 ? <Badge tone="warning">{`${plan.droppedCount} dropped`}</Badge> : <Badge tone="success">All stops served</Badge>}
      />
      {plan.warnings.map((w) => (
        <Alert key={w.code} tone="warning" title={`Stops dropped (${w.code})`}>
          {w.detail} Widen their time windows or add vehicles, then optimize again.
        </Alert>
      ))}
      <Card>
        <Details
          entries={[
            { term: "Stops", value: String(plan.stopCount) },
            { term: "Vehicles used", value: `${plan.routeCount} of ${plan.vehicleCount}` },
            { term: "Total distance", value: km(plan.totalDistanceKm) },
            { term: "Solver time", value: `${plan.timeLimitS} s` },
            { term: "Planned by", value: plan.createdBy },
            { term: "Planned", value: formatDateTime(plan.createdAt) },
            { term: "Request hash", value: plan.requestHash, mono: true },
          ]}
        />
      </Card>
      {plan.solution.dropped.length > 0 ? (
        <Card title="Dropped stops" headingLevel={3}>
          <ul className={styles.trace}>
            {plan.solution.dropped.map((stop) => (
              <li key={stop}>{stop}</li>
            ))}
          </ul>
        </Card>
      ) : null}
      {used.length === 0 ? <p className={styles.note}>No vehicle could serve any stop.</p> : used.map(({ i }) => <RouteCard key={i} plan={plan} index={i} />)}
      <div className={styles.actions}>
        <Link to="/transport/routes/new">Optimize other routes</Link>
        <Link to="/transport/routes">Back to route plans</Link>
      </div>
    </div>
  );
}
