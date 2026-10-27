import { Link } from "react-router-dom";
import { Alert, Button, Card, CardGrid, PageHeader, Skeleton, StatGroup, StatTile } from "../../shell/ui";
import { getWorkspace } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const areas = [
  { to: "/eam/assets", title: "Assets", text: "Register equipment, add counters and condition rules, assess criticality and record warranties." },
  { to: "/eam/plans", title: "Maintenance plans", text: "Time and counter plans that create preventive work orders." },
  { to: "/eam/requests", title: "Requests", text: "Triage, defer or reject requests from people and condition rules, or convert them to work orders." },
  { to: "/eam/work-orders", title: "Work orders", text: "Move orders through their lifecycle, book labor and downtime, and register faults." },
  { to: "/eam/fault-catalogs", title: "Fault catalogs", text: "Design the symptoms, causes and remedies technicians choose from per asset type." },
];

/** Asset management workspace (8.44.6): tiles counted by the service, then the areas of the workspace. */
export function WorkspacePage() {
  const state = useLoad((signal) => getWorkspace(signal), "workspace");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Asset management"
        description="Requests to triage, work orders coming due and open maintenance work for your plant."
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the workspace" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : state.loading || !state.data ? (
        <div aria-busy="true">
          <Skeleton height="tile" />
        </div>
      ) : (
        <StatGroup label="Asset management tiles">
          <StatTile label="Requests to triage" value={state.data.requestsToTriage} tone="warning" />
          <StatTile label="Work orders due in 7 days" value={state.data.dueIn7Days} />
          <StatTile label="Overdue" value={state.data.overdue} tone="danger" />
          <StatTile label="Open work orders" value={state.data.openWorkOrders} />
        </StatGroup>
      )}
      <CardGrid as="ul" label="Asset management areas">
        {areas.map((area) => (
          <li key={area.to}>
            <Card title={area.title} headingLevel={3}>
              <p className={styles.note}>{area.text}</p>
              <p>
                <Link to={area.to}>Open {area.title.toLowerCase()}</Link>
              </p>
            </Card>
          </li>
        ))}
      </CardGrid>
    </div>
  );
}
