import { useRef, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getRun, getRunKpis, newKey, rollbackRun, RUN_WARNING_LABELS, type Problem, type RunResourceKpi, type RunRollback } from "./api/client";
import { dash, formatDateTime, formatMinutes, sentence, shortId } from "./format";
import styles from "./pages.module.css";
import { scopeLabels } from "./RunsPage";
import { useLoad } from "./useLoad";

const skipLabels: Record<string, string> = {
  firmed: "firmed, so it stays where it is",
  operation_missing: "its operation no longer exists",
  operation_changed: "its operation was changed since the run, so the old placement is out of date",
  resource_missing: "a resource it used no longer exists",
};

const rollbackFailures: Record<string, string> = {
  "SCHED-0143": "This run kept no job snapshot",
  "SCHED-0144": "The plan changed since this run",
  "SCHED-0145": "Nothing can be restored",
  "SCHED-0146": "The restored plan would break scheduling rules",
};

/** Restores the jobs this run placed as the current plan (PRD 4.23.14 rollback). */
function RollbackCard({ runId }: { runId: string }) {
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<RunRollback | null>(null);
  const key = useRef(newKey());

  async function rollBack() {
    setBusy(true);
    setFailure(null);
    try {
      const result = await rollbackRun(runId, key.current);
      key.current = newKey();
      setDone(result);
      setConfirming(false);
      if (result.skipped.length === 0) navigate(`/scheduling/runs/${result.run.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
      setConfirming(false);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Roll back to this run">
      <div className={styles.stack}>
        <p className={styles.note}>
          Puts the jobs this run placed back as the current plan, under a new run. Firmed jobs, and operations changed since the run, stay as they are. If a restored job
          now overlaps a booking made since, or would break the calendar, a block, its order&apos;s sequence, a peg or a changeover, nothing changes.
        </p>
        {failure ? (
          <Alert tone="danger" title={rollbackFailures[failure.code] ?? "The rollback failed"} live="alert">
            {failure.message} ({failure.code})
            {Object.keys(failure.fields).length > 0 ? (
              <ul className={styles.messages} aria-label="Jobs that block the rollback">
                {Object.entries(failure.fields).map(([job, why]) => (
                  <li key={job}>{`${shortId(job)}: ${why}`}</li>
                ))}
              </ul>
            ) : null}
          </Alert>
        ) : null}
        {done ? (
          <Alert tone="success" live="status" title={`${done.restored} job(s) restored`}>
            {done.skipped.length > 0 ? (
              <>
                <span>Left alone:</span>
                <ul className={styles.messages}>
                  {done.skipped.map((sk) => (
                    <li key={sk.jobId}>{`${shortId(sk.jobId)}: ${skipLabels[sk.reason] ?? sk.reason}`}</li>
                  ))}
                </ul>
                <Link to={`/scheduling/runs/${done.run.id}`}>Open the rollback run</Link>
              </>
            ) : null}
          </Alert>
        ) : null}
        {confirming ? (
          <div className={styles.formActions}>
            <Button variant="primary" loading={busy} onClick={rollBack}>
              Roll back the plan
            </Button>
            <Button variant="ghost" disabled={busy} onClick={() => setConfirming(false)}>
              Keep the current plan
            </Button>
          </div>
        ) : (
          <div className={styles.formActions}>
            <Button variant="secondary" onClick={() => setConfirming(true)}>
              Roll back to this run
            </Button>
          </div>
        )}
      </div>
    </Card>
  );
}

/**
 * One schedule run with its metrics, the operations it could not place (with
 * SCHED-0009 NETWORK_INFEASIBLE among the reasons) and the warnings on placed
 * operations: late order after backward scheduling fell back to forward
 * (SCHED-0003), network wait (SCHED-0010), missing setup matrix entry
 * (SCHED-0012) and no matching capacity block (SCHED-0014).
 */
export function RunDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getRun(id, signal), id);
  const run = state.data;
  const kpis = useLoad((signal) => getRunKpis(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Schedule run" />
        <Alert tone="danger" title="Could not load the run" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!run) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Schedule run" />
        <span className="visually-hidden">Loading the run…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  const facts: [string, ReactNode][] = [
    ["Run", run.id],
    ["Scope", scopeLabels[run.scope] ?? run.scope],
    ["Mode", sentence(run.mode)],
    ["Direction", sentence(run.direction)],
    ["Capacity", <Badge key="cap" tone={run.finite ? "info" : "neutral"}>{run.finite ? "Finite" : "Infinite"}</Badge>],
    ["Algorithm", sentence(run.algorithm)],
    ["Operations scheduled", String(run.operationsCount)],
    ["Makespan", formatMinutes(run.makespanMinutes)],
    ["Total lateness", formatMinutes(run.totalLatenessMinutes)],
    ["Solver status", run.solverStatus ?? dash],
    ["Started", formatDateTime(run.createdAt)],
  ];
  if (run.rollbackOf) {
    facts.push(["Restored run", <Link key="rb" to={`/scheduling/runs/${run.rollbackOf}`}>{shortId(run.rollbackOf)}</Link>]);
  }

  const k = kpis.data;
  const kpiFacts: [string, string][] = k
    ? [
        ["Jobs still holding this run", `${k.placedJobs} of ${run.operationsCount} operations`],
        ["Orders", String(k.orders)],
        ["Late orders", `${k.lateOrders} (${k.lateJobs} late jobs)`],
        ["Latest job late by", k.maxLateMinutes > 0 ? formatMinutes(k.maxLateMinutes) : dash],
        ["Changeover time", formatMinutes(k.changeoverMinutes)],
        ["Booked time", formatMinutes(k.bookedMinutes)],
        ["Span", k.spanStart && k.spanEnd ? `${formatDateTime(k.spanStart)} to ${formatDateTime(k.spanEnd)}` : dash],
      ]
    : [];
  const kpiColumns: DataTableColumn<RunResourceKpi>[] = [
    { key: "resourceCode", header: "Resource", mono: true },
    { key: "jobs", header: "Jobs", align: "end" },
    { key: "bookedMinutes", header: "Booked", align: "end", render: (row) => formatMinutes(row.bookedMinutes) },
    { key: "openMinutes", header: "Open", align: "end", render: (row) => formatMinutes(row.openMinutes) },
    { key: "utilizationPct", header: "Utilization %", align: "end", render: (row) => row.utilizationPct.toFixed(1) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Schedule run"
        meta={<StatusPill status={run.status} />}
        actions={
          <>
            {run.snapshot ? (
              <Link className={buttonClassName("secondary")} to={`/scheduling/runs/${run.id}/compare`}>
                Compare with another run
              </Link>
            ) : null}
            <Link className={buttonClassName("secondary")} to="/scheduling">
              Open the Gantt
            </Link>
          </>
        }
      />
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={term === "Run" ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Plan measures">
        {kpis.error ? (
          <Alert tone="danger" title="Could not load the plan measures" live="alert" action={<Button size="sm" onClick={kpis.reload}>Try again</Button>}>
            {kpis.error.message} ({kpis.error.code})
          </Alert>
        ) : kpis.loading || !k ? (
          <div aria-busy="true">
            <span className="visually-hidden">Loading the plan measures…</span>
            <Skeleton height="tile" />
          </div>
        ) : k.placedJobs === 0 ? (
          <EmptyState
            compact
            headingLevel={3}
            title="No jobs carry this run"
            description="A later run or a move replaced its jobs, or it placed nothing. Measures describe the jobs a run still holds."
          />
        ) : (
          <div className={styles.stack}>
            <dl className={styles.details} aria-label="Plan measures">
              {kpiFacts.map(([term, value]) => (
                <div key={term} className={styles.entry}>
                  <dt className={styles.term}>{term}</dt>
                  <dd className={styles.value}>{value}</dd>
                </div>
              ))}
            </dl>
            <DataTable label="Resource utilization" columns={kpiColumns} rows={k.resources} getRowKey={(row) => row.resourceId} />
          </div>
        )}
      </Card>
      {run.snapshot ? (
        <RollbackCard key={run.id} runId={run.id} />
      ) : (
        <Card title="Roll back to this run">
          <EmptyState compact headingLevel={3} title="No job snapshot" description="Gantt moves and runs from before snapshots kept no jobs, so they can be neither compared nor rolled back to." />
        </Card>
      )}
      <Card title="Warnings">
        {(run.warnings ?? []).length === 0 ? (
          <EmptyState
            compact
            headingLevel={3}
            title="No warnings"
            description="Changeovers without a setup matrix entry, jobs outside every capacity block and network waits over the maximum would be listed here."
          />
        ) : (
          <ul className={styles.warnings} aria-label="Run warnings">
            {run.warnings.map((w) => (
              <li key={`${w.code}-${w.operationId}`} className={styles.warning}>
                <Badge tone="warning">{`${w.code} ${RUN_WARNING_LABELS[w.code] ?? ""}`.trim()}</Badge>
                <span className={styles.mono}>{shortId(w.operationId)}</span>
                <span>{w.detail}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <Card title="Unplaced operations">
        {run.messages.length === 0 ? (
          <EmptyState compact headingLevel={3} title="Every operation was placed" description="Operations without capacity in the horizon would be listed here." />
        ) : (
          <ul className={styles.messages}>
            {run.messages.map((m) => (
              <li key={m}>{m}</li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
