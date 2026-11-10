import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getOrderImport, ORDER_IMPORT_SKIP_LABELS, reimportOrder, scheduleOrder, type OrderImport, type Run, type OrderImportSkipped, type Problem } from "./api/client";
import { dash, formatDateTime, shortId } from "./format";
import { ImportStatusBadge } from "./OrderImportsPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const skippedColumns: DataTableColumn<OrderImportSkipped>[] = [
  { key: "operNumber", header: "Operation", align: "end" },
  { key: "code", header: "Reason", render: (row) => <Badge tone={row.code === "firmed" ? "info" : "warning"}>{ORDER_IMPORT_SKIP_LABELS[row.code] ?? row.code}</Badge> },
  { key: "detail", header: "Detail" },
];

/** What a per-order run did, in words: firmed jobs are not part of the count. */
export function runSummary(run: Run): string {
  if (run.operationsCount === 0) return "Every operation is held by a firmed job, so nothing was moved.";
  const unplaced = run.messages.length;
  const placed = run.operationsCount - unplaced;
  return unplaced === 0 ? `${placed} of ${placed} operations placed.` : `${placed} of ${run.operationsCount} operations placed; ${unplaced} could not be placed (see the run).`;
}

/** One received production order: what happened to each of its operations (FR-23.10). */
export function OrderImportDetailPage() {
  const { orderId = "" } = useParams();
  const state = useLoad((signal) => getOrderImport(orderId, signal), orderId);
  const [again, setAgain] = useState<OrderImport | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const o = again ?? state.data;
  const [scheduling, setScheduling] = useState(false);
  const [scheduleFailure, setScheduleFailure] = useState<Problem | null>(null);
  const [run, setRun] = useState<Run | null>(null);

  async function scheduleNow() {
    setScheduling(true);
    setScheduleFailure(null);
    try {
      setRun(await scheduleOrder(orderId));
    } catch (error) {
      setScheduleFailure(describeProblem(error));
    } finally {
      setScheduling(false);
    }
  }

  async function importAgain() {
    setBusy(true);
    setFailure(null);
    try {
      setAgain(await reimportOrder(orderId));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Production order" />
        <Alert tone="danger" title="Could not load the production order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!o) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Production order" />
        <span className="visually-hidden">Loading the production order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  const missingResource = o.skipped.some((row) => row.code === "unknown-resource");
  const facts: [string, string][] = [
    ["Order", o.orderNumber || shortId(o.orderId)],
    ["Order id", o.orderId],
    ["Order version", String(o.orderVersion)],
    ["Event", o.reason === "redate" ? "Re-dated after a change" : "Scheduled"],
    ["Operations in the event", String(o.operationsTotal)],
    ["Created", String(o.created)],
    ["Updated (jobs dropped)", String(o.updated)],
    ["Unchanged", String(o.unchanged)],
    ["Removed from the route", String(o.removed)],
    ["Kept, held by a firmed job", String(o.keptFirmed)],
    ["Baseline start", o.orderStart ? formatDateTime(o.orderStart) : dash],
    ["Baseline end", o.orderEnd ? formatDateTime(o.orderEnd) : dash],
    ["Received", formatDateTime(o.receivedAt)],
  ];
  if (o.status === "withdrawn") {
    facts.push(["Withdrawn", o.withdrawnAt ? formatDateTime(o.withdrawnAt) : dash], ["Production status", o.withdrawnTo ?? dash]);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Production order ${o.orderNumber || shortId(o.orderId)}`}
        meta={<ImportStatusBadge status={o.status} />}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/scheduling/operations">
              Operations
            </Link>
            {o.status !== "withdrawn" && (
              <Button variant="secondary" loading={scheduling} onClick={scheduleNow}>
                Schedule this order
              </Button>
            )}
            {o.replanRequired && (
              <Link className={buttonClassName("primary")} to="/scheduling/runs/new">
                Start run
              </Link>
            )}
          </>
        }
      />
      {o.status === "withdrawn" && (
        <Alert tone="info" title="Production took this order out of schedule">
          Production moved the order to {o.withdrawnTo ?? "a status without dates"} (reset, cancelled or merged), so its operations were removed from scheduling.
          {o.keptFirmed > 0 ? " Operations held by a firmed job were kept and still hold their capacity; unfirming one lets the next run schedule it again." : ""} The order returns here when production schedules it again.
        </Alert>
      )}
      {o.replanRequired && (
        <Alert tone="warning" title="Schedule this order again">
          An operation changed after it was placed, so its jobs were removed and it is open again. Schedule this order, or start a run, to place it.
        </Alert>
      )}
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={term === "Order id" ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      {missingResource && (
        <Alert
          tone="warning"
          title="Some operations name a resource scheduling does not know"
          action={
            o.reimportable ? (
              <Button size="sm" loading={busy} onClick={importAgain}>
                Import again
              </Button>
            ) : undefined
          }
        >
          Map the engineering resource to a scheduling resource (Edit calendar, Engineering resource id), then import the order again.{" "}
          {o.reimportable ? "" : "This order was received before scheduling kept events; it is imported again on its next event."}{" "}
          <Link to="/scheduling/resources">Resources</Link>
        </Alert>
      )}
      {scheduleFailure && (
        <Alert tone="danger" title="Could not schedule the order" live="alert">
          <ProblemMessage problem={scheduleFailure} />
        </Alert>
      )}
      {run && (
        <Alert tone="success" title="The order was scheduled" live="status">
          {runSummary(run)} <Link to={`/scheduling/runs/${run.id}`}>Open the run</Link>
        </Alert>
      )}
      {failure && (
        <Alert tone="danger" title="Could not import the order again" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      )}
      <Alert tone="success" live="status">
        {again ? `The order was imported again: ${again.created} created, ${again.updated} updated, ${again.unchanged} unchanged, ${again.skipped.length} left out.` : ""}
      </Alert>
      <Card title="Operations not registered">
        {o.skipped.length === 0 ? (
          <EmptyState compact headingLevel={3} title="Nothing left out" description="Every operation of the order is registered for scheduling." />
        ) : (
          <DataTable label="Operations not registered" columns={skippedColumns} rows={o.skipped} getRowKey={(row) => `${row.operNumber}-${row.code}`} />
        )}
      </Card>
    </div>
  );
}
