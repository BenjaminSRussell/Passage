import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getLoadTracking, type StopState, type TrackingEvent } from "./api/client";
import { dash, formatDateTime, stateTone, statusLabel, trackingCodeLabels } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const stopColumns: DataTableColumn<StopState>[] = [
  { key: "stopNo", header: "Stop", align: "end" },
  { key: "status", header: "Status", render: (row) => <Badge tone={stateTone(row.status)}>{statusLabel(row.status)}</Badge> },
  { key: "arrivedAt", header: "Arrived", render: (row) => formatDateTime(row.arrivedAt) },
  {
    key: "pickedUpAt",
    header: "Picked up",
    render: (row) =>
      row.pickupInferred ? (
        <span className={styles.flags}>
          {formatDateTime(row.pickedUpAt)}
          <Badge tone="warning">Inferred</Badge>
        </span>
      ) : (
        formatDateTime(row.pickedUpAt)
      ),
  },
  { key: "deliveredAt", header: "Delivered", render: (row) => formatDateTime(row.deliveredAt) },
];

const eventColumns: DataTableColumn<TrackingEvent>[] = [
  { key: "occurredAt", header: "Occurred", render: (row) => formatDateTime(row.occurredAt) },
  { key: "stopNo", header: "Stop", align: "end" },
  { key: "code", header: "Status", render: (row) => trackingCodeLabels[row.code] ?? row.code },
  { key: "location", header: "Location", render: (row) => row.location || dash },
  { key: "source", header: "Source", render: (row) => <Badge tone="neutral">{row.source}</Badge> },
  { key: "ref", header: "Reference", mono: true },
];

/** One load's tracking timeline with the stop states derived from it (BR-34.9). */
export function LoadTrackingPage() {
  const { loadId = "" } = useParams();
  const state = useLoad((signal) => getLoadTracking(loadId, signal), `load-tracking:${loadId}`);
  const tracking = state.data;
  const header = (
    <PageHeader
      title={`Load ${loadId.slice(0, 8)}`}
      description="Events are applied in the order they happened, not the order they arrived. A delivery reported without a pickup marks the pickup as inferred."
      actions={
        <Link className={buttonClassName("primary")} to={`/transport/tracking/new?loadId=${loadId}`}>
          Record status
        </Link>
      }
    />
  );

  if (state.error) {
    return (
      <div className={styles.stack}>
        {header}
        <Alert tone="danger" title="Could not load the timeline" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/transport/tracking">Back to tracking</Link>
      </div>
    );
  }
  if (!tracking) {
    return (
      <div className={styles.stack} aria-busy="true">
        {header}
        <Card>
          <p className={styles.note}>Loading the timeline…</p>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      {header}
      {tracking.events.length === 0 ? (
        <Card>
          <EmptyState compact headingLevel={3} title="No status reports for this load" description="Reports appear once the carrier starts sending EDI 214 or API updates." />
        </Card>
      ) : (
        <>
          <Card title="Stops" headingLevel={3}>
            <DataTable label="Stops" columns={stopColumns} rows={tracking.stops} getRowKey={(row) => String(row.stopNo)} />
          </Card>
          <Card title="Timeline" headingLevel={3}>
            <DataTable label="Status reports" columns={eventColumns} rows={tracking.events} getRowKey={(row) => row.id} />
          </Card>
        </>
      )}
      <Link to="/transport/tracking">Back to tracking</Link>
    </div>
  );
}
