import { Link } from "react-router-dom";
import { Alert, Badge, Button, DataTable, PageHeader, StatGroup, StatTile, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { listAndon, type Andon } from "./api/client";
import { dash, durationMinutes, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const SEVERITY: Record<Andon["severity"], { label: string; tone: BadgeTone }> = {
  info: { label: "Info", tone: "info" },
  warning: { label: "Warning", tone: "warning" },
  error: { label: "Error", tone: "danger" },
};

const columns: DataTableColumn<Andon>[] = [
  { key: "resourceId", header: "Resource", mono: true },
  { key: "kind", header: "Condition", render: (row) => (row.kind === "downtime" ? "Machine down over 10 min" : row.kind) },
  { key: "severity", header: "Severity", render: (row) => <Badge tone={SEVERITY[row.severity].tone}>{SEVERITY[row.severity].label}</Badge> },
  { key: "startedAt", header: "Since", render: (row) => formatDateTime(row.startedAt) },
  { key: "open", header: "Open for", align: "end", render: (row) => durationMinutes(row.startedAt) },
  {
    key: "downtime",
    header: "Downtime",
    render: (row) => (row.downtimeId ? <Link to={`/shopfloor/downtimes?downtime=${encodeURIComponent(row.downtimeId)}&open=false`}>Code reason</Link> : dash),
  },
];

/** Open andon alerts across the plant (FR-25.11, BR-25.10, FR-25.14 supervisor view). */
export function AndonPage() {
  const state = useLoad((signal) => listAndon({}, signal), "andon");
  const rows = state.data ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Andon board"
        description="One open alert per resource and condition. An alert clears by itself once its condition has been clear for 5 minutes; coding the downtime reason does not clear it."
        actions={<Button onClick={state.reload} loading={state.loading}>Refresh</Button>}
      />
      {state.data ? (
        <StatGroup label="Andon summary">
          <StatTile label="Open alerts" value={rows.length} tone="danger" />
          <StatTile label="Resources affected" value={new Set(rows.map((a) => a.resourceId)).size} tone="warning" />
        </StatGroup>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load andon alerts" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Open andon alerts"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No open alerts", description: "Every machine is running or was stopped for less than 10 minutes." }}
        />
      )}
    </div>
  );
}
