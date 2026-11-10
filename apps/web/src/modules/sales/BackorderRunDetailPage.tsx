import { useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { commitBackorderRun, describeProblem, getBackorderRun, type BackorderRun, type BackorderRunLine, type Problem, type Versioned } from "./api/client";
import { RunStatusBadge } from "./BackorderRunsPage";
import { dash, formatDateTime, sentence, shortId, subtractDecimal } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** "kept", "gains 20", "loses 20": what the run does to a line's promised quantity. */
export function lineEffect(line: BackorderRunLine): string {
  if (!line.changed) return "Unchanged";
  const diff = subtractDecimal(line.after, line.before);
  return diff.startsWith("-") ? `Loses ${diff.slice(1)}` : `Gains ${diff}`;
}

const columns: DataTableColumn<BackorderRunLine>[] = [
  { key: "seq", header: "#", align: "end" },
  { key: "orderNumber", header: "Order", mono: true, render: (l) => <Link to={`/sales/orders/${l.orderId}`}>{`${l.orderNumber} line ${l.lineNo}`}</Link> },
  { key: "customerId", header: "Customer", mono: true, render: (l) => shortId(l.customerId) },
  { key: "priorityClass", header: "Priority", align: "end" },
  { key: "requestedDate", header: "Requested", mono: true },
  { key: "demand", header: "Quantity", align: "end" },
  { key: "before", header: "Promised now", align: "end" },
  { key: "after", header: "After the run", align: "end" },
  {
    key: "changed",
    header: "Effect",
    render: (l) => <Badge tone={l.deconfirmed ? "danger" : l.changed ? "success" : "neutral"}>{lineEffect(l)}</Badge>,
  },
  { key: "gainDate", header: "Promised on", mono: true, render: (l) => l.gainDate ?? dash },
];

/** One stored run with its lines in processing order, and the commit (FR-27.17, BR-27.11, AC-27.12). */
export function BackorderRunDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getBackorderRun(id, signal), id);
  const [latest, setLatest] = useState<Versioned<BackorderRun> | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const current = latest ?? state.data;
  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Backorder run" />
        <Alert tone="danger" title="Could not load the run" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Backorder run" />
        <span className="visually-hidden">Loading the run…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: run, etag } = current;
  const lines = run.lines ?? [];

  async function commit() {
    setFailure(null);
    setBusy(true);
    try {
      setLatest(await commitBackorderRun(run.id, etag));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const facts: [string, ReactNode][] = [
    ["Status", <RunStatusBadge key="s" status={run.status} />],
    ["Strategy", sentence(run.strategy)],
    ["Product", run.productId],
    ["Site", run.siteId],
    ["Warehouse", run.warehouseId],
    ["Supply", `${run.supply} available on ${run.supplyDate}`],
    ["Promised after the run", run.allocated],
    ["Lines changed", `${run.changedCount} of ${lines.length}`],
    ["Lines deconfirmed", String(run.deconfirmedCount)],
    ["Planned", `${formatDateTime(run.createdAt)} by ${run.createdBy}`],
    ["Committed", run.committedAt ? `${formatDateTime(run.committedAt)} by ${run.committedBy ?? ""}` : dash],
  ];
  const mono = new Set(["Product", "Site", "Warehouse"]);
  const stale = failure?.code === "SLS-0042";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Backorder run"
        actions={
          <Link className={buttonClassName("ghost")} to="/sales/backorder-processing">
            Back to runs
          </Link>
        }
      />
      {run.status === "PREVIEW" && run.warning ? (
        <Alert tone="warning" title={run.warning} live="status">
          Review the deconfirmed lines before committing ({run.warningCode}).
        </Alert>
      ) : null}
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={mono.has(term) ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      {run.status === "PREVIEW" ? (
        <Card title="Commit the run">
          <p className={styles.muted}>
            Committing rewrites the schedule lines of the {run.changedCount} changed {run.changedCount === 1 ? "line" : "lines"} in one step, gives each order a new version and
            tells planning and fulfillment. If an order changed since this preview, nothing is applied.
          </p>
          {failure ? (
            <Alert tone="danger" title={stale ? "An order changed since the preview" : "Could not commit the run"} live="alert">
              <ProblemMessage problem={failure} />
              {stale ? (
                <>
                  {" "}
                  <Link to="/sales/backorder-processing/new">Plan a new run</Link>.
                </>
              ) : null}
            </Alert>
          ) : null}
          <div className={styles.formActions}>
            <Button variant="primary" onClick={commit} loading={busy} disabled={run.changedCount === 0}>
              Commit run
            </Button>
          </div>
        </Card>
      ) : (
        <Alert tone="success" live="status">
          Committed: the schedule lines were rewritten.
        </Alert>
      )}
      <Card title="Lines in processing order">
        <DataTable
          label="Run lines"
          columns={columns}
          rows={lines}
          getRowKey={(l) => l.lineId}
          empty={{ title: "No lines", description: "The run has no open lines." }}
        />
      </Card>
    </div>
  );
}
