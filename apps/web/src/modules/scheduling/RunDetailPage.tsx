import type { ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, EmptyState, PageHeader, Skeleton, StatusPill } from "../../shell/ui";
import { getRun, RUN_WARNING_LABELS } from "./api/client";
import { dash, formatDateTime, formatMinutes, sentence, shortId } from "./format";
import styles from "./pages.module.css";
import { scopeLabels } from "./RunsPage";
import { useLoad } from "./useLoad";

/**
 * One schedule run with its metrics, the operations it could not place (with
 * SCHED-0009 NETWORK_INFEASIBLE among the reasons) and the warnings on placed
 * operations: network wait (SCHED-0010), missing setup matrix entry
 * (SCHED-0012) and no matching capacity block (SCHED-0014).
 */
export function RunDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getRun(id, signal), id);
  const run = state.data;

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

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Schedule run"
        meta={<StatusPill status={run.status} />}
        actions={
          <Link className={buttonClassName("secondary")} to="/scheduling">
            Open the Gantt
          </Link>
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
