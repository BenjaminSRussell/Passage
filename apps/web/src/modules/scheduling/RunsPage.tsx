import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listRuns, type Run } from "./api/client";
import { formatDateTime, formatMinutes } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export const scopeLabels: Record<Run["scope"], string> = { site: "Whole site", selection: "Selection", order: "One order", move: "Gantt move", rollback: "Rollback" };
export const directionLabels: Record<string, string> = { forward: "Forward", backward: "Backward" };

const columns: DataTableColumn<Run>[] = [
  { key: "createdAt", header: "Started", render: (row) => formatDateTime(row.createdAt) },
  { key: "scope", header: "Scope", render: (row) => scopeLabels[row.scope] ?? row.scope },
  { key: "direction", header: "Direction", render: (row) => directionLabels[row.direction] ?? row.direction },
  { key: "finite", header: "Capacity", render: (row) => <Badge tone={row.finite ? "info" : "neutral"}>{row.finite ? "Finite" : "Infinite"}</Badge> },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "operationsCount", header: "Operations", align: "end" },
  { key: "makespanMinutes", header: "Makespan", align: "end", render: (row) => formatMinutes(row.makespanMinutes) },
  { key: "totalLatenessMinutes", header: "Lateness", align: "end", render: (row) => formatMinutes(row.totalLatenessMinutes) },
];

/** Schedule runs, newest first (PRD 4.23.6 "Schedule runs"). */
export function RunsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listRuns(signal), "runs");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Schedule runs"
        description="Every heuristic run and every saved Gantt move, with its makespan and total lateness."
        actions={
          <Link className={buttonClassName("primary")} to="/scheduling/runs/new">
            Start run
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load runs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Schedule runs"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No schedule runs yet", description: "Start a run to place open operations on your resources." }}
          onRowOpen={(row) => navigate(`/scheduling/runs/${row.id}`)}
        />
      )}
    </div>
  );
}
