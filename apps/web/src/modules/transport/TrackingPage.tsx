import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listTrackingEvents, type TrackingEvent } from "./api/client";
import { dash, formatDateTime, shortId, trackingCodeLabels, UUID } from "./format";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<TrackingEvent>[] = [
  { key: "loadId", header: "Load", mono: true, render: (row) => <span title={row.loadId}>{shortId(row.loadId)}</span> },
  { key: "stopNo", header: "Stop", align: "end" },
  { key: "code", header: "Status", render: (row) => trackingCodeLabels[row.code] ?? row.code },
  { key: "occurredAt", header: "Occurred", render: (row) => formatDateTime(row.occurredAt) },
  { key: "location", header: "Location", render: (row) => row.location || dash },
  { key: "source", header: "Source", render: (row) => <Badge tone="neutral">{row.source}</Badge> },
  { key: "receivedAt", header: "Received", render: (row) => formatDateTime(row.receivedAt) },
];

/** Tracking (PRD 6.34.6): the latest status reports from carriers and drivers, and a way into one load's timeline. */
export function TrackingPage() {
  const navigate = useNavigate();
  const [loadId, setLoadId] = useState("");
  const [error, setError] = useState<string | undefined>();
  const state = useLoad((signal) => listTrackingEvents({}, signal), "tracking-events");

  function open(event: FormEvent) {
    event.preventDefault();
    const id = loadId.trim();
    if (!UUID.test(id)) {
      setError("Choose a load.");
      return;
    }
    setError(undefined);
    navigate(`/transport/tracking/${id}`);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Tracking"
        description="Status reports arrive by EDI 214, carrier APIs, telematics or driver check-in. Each load's timeline shows when every stop was reached, picked up and delivered."
        actions={
          <Link className={buttonClassName("primary")} to="/transport/tracking/new">
            Record status
          </Link>
        }
      />
      <Card title="Load timeline" headingLevel={3}>
        <form className={styles.inlineForm} onSubmit={open} noValidate>
          <FormField id="tracking-load" label="Load" error={error}>
            {(control) => <EntityLookup {...control} entity="load" value={loadId} onChange={(e) => setLoadId(e.target.value)} invalid={Boolean(error)} />}
          </FormField>
          <Button type="submit">Open timeline</Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone="danger" title="Could not load tracking events" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Latest status reports"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No status reports yet", description: "Reports appear here as carriers send them, newest first." }}
          onRowOpen={(row) => navigate(`/transport/tracking/${row.loadId}`)}
        />
      )}
    </div>
  );
}
