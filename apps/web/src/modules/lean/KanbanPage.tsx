import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, Skeleton, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getKanban, type KanbanJob } from "./api/client";
import { dash, formatDateTime, shortId, STATE_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const jobColumns: DataTableColumn<KanbanJob>[] = [
  { key: "sequence", header: "Step", align: "end" },
  { key: "activityName", header: "Activity" },
  {
    key: "workCellResourceId",
    header: "Work cell",
    render: (j) =>
      j.workCellResourceId ? (
        <Link className={styles.mono} to={`/lean/work-cell?workCellId=${j.workCellResourceId}`} aria-label={`Open the work-cell board of ${j.activityName}`}>
          {shortId(j.workCellResourceId)}
        </Link>
      ) : (
        dash
      ),
  },
  { key: "state", header: "State", render: (j) => <StatusPill status={STATE_LABELS[j.state]} /> },
  { key: "startedAt", header: "Started", render: (j) => formatDateTime(j.startedAt) },
  { key: "completedAt", header: "Completed", render: (j) => formatDateTime(j.completedAt) },
  { key: "reportedQty", header: "Reported", align: "end", render: (j) => j.reportedQty ?? dash },
  { key: "scrapQty", header: "Scrap", align: "end", render: (j) => j.scrapQty ?? dash },
  { key: "handlingUnits", header: "Handling units", align: "end", render: (j) => j.handlingUnits ?? dash },
];

/** One kanban card with its jobs, one per activity of the rule's flow (FR-24.10). */
export function KanbanPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getKanban(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Kanban" />
        <Alert tone="danger" title="Could not load the kanban" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Kanban" />
        <span className="visually-hidden">Loading the kanban…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const k = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={k.cardNumber}
        description={`${k.ruleType === "manufacturing" ? "Manufacturing" : "Withdrawal"} kanban of rule ${k.ruleCode}`}
        meta={<StatusPill status={STATE_LABELS[k.state]} />}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/lean/rules/${k.ruleId}`}>
              Open rule
            </Link>
            <Link className={buttonClassName("secondary")} to={`/lean/board?ruleId=${k.ruleId}`}>
              Open board
            </Link>
          </>
        }
      />
      <StatGroup label="Kanban facts">
        <StatTile label="Quantity" value={k.qty} />
        <StatTile label="Due" value={formatDateTime(k.due)} />
        <StatTile label="Handling units" value={k.handlingUnits ?? dash} />
        <StatTile label="Jobs" value={k.jobCount} />
      </StatGroup>
      {k.eventRefId ? <p className={styles.muted}>{`Demand ${`${k.eventRefType ?? ""} ${k.eventRefId}`.trim()}`}</p> : null}
      <DataTable
        label="Kanban jobs"
        columns={jobColumns}
        rows={k.jobs ?? []}
        getRowKey={(j) => j.id}
        empty={{
          title: "No jobs",
          description: "This card's rule has no production flow, so the card moves by hand on the kanban board.",
        }}
      />
      <p className={styles.muted}>{`Item ${k.itemId} · version ${k.version} · updated ${formatDateTime(k.updatedAt)}`}</p>
    </div>
  );
}
