import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getRun, type QuarantinedRecord } from "./api/client";
import { canReconcile, formatDateTime, formatKey, money, SOURCE_LABELS, type ControlTotal } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const totalColumns: DataTableColumn<ControlTotal>[] = [
  { key: "name", header: "Control total", mono: true },
  { key: "key", header: "Key", render: (row) => formatKey(row.key) },
  { key: "source", header: "Legacy", align: "end", mono: true, render: (row) => money(row.source) },
  { key: "target", header: "Passage", align: "end", mono: true, render: (row) => money(row.target) },
  { key: "delta", header: "Delta", align: "end", mono: true, render: (row) => money(row.delta) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

const quarantineColumns: DataTableColumn<QuarantinedRecord>[] = [
  { key: "legacyId", header: "Record", mono: true },
  { key: "rule", header: "Rule failed" },
  { key: "reason", header: "Reason" },
];

/** One pack run: its counts, the control-total board and the quarantined records (9.49.6). */
export function PackRunDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getRun(id, signal), id);
  const run = state.data;
  const mismatches = run ? run.controlTotals.filter((t) => t.status === "MISMATCH").length : 0;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={run ? `Pack run ${run.pack}` : "Pack run"}
        description={
          run
            ? canReconcile(run.controlTotals)
              ? "Every control total matches the legacy extract."
              : `${mismatches} control total(s) do not match; the run can't reconcile until they do.`
            : undefined
        }
        meta={run ? <StatusPill status={run.status} /> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/migration-factory">
            All runs
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the pack run" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      {run ? (
        <>
          {run.message ? (
            <Alert tone="info" title="Nothing new to load">
              {run.message}
            </Alert>
          ) : null}
          <StatGroup label="Records">
            <StatTile label="Records" value={run.recordsIn} />
            <StatTile label="Loaded" value={run.recordsLoaded} />
            <StatTile label="Already present" value={run.alreadyPresent} />
            <StatTile label="Quarantined" value={run.recordsQuarantined} tone={run.recordsQuarantined > 0 ? "warning" : "neutral"} />
          </StatGroup>
          <Card title="Run">
            <dl className={styles.details}>
              <div className={styles.entry}>
                <dt className={styles.term}>Source</dt>
                <dd className={styles.value}>
                  {SOURCE_LABELS[run.sourceSystem] ?? run.sourceSystem} {run.sourceCompany}
                </dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Cutoff</dt>
                <dd className={styles.value}>{formatDateTime(run.cutoffTs)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Started</dt>
                <dd className={styles.value}>{formatDateTime(run.createdAt)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Input hash</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{run.inputHash}</dd>
              </div>
            </dl>
          </Card>
        </>
      ) : null}
      {!state.error ? (
        <DataTable
          label="Control totals"
          columns={totalColumns}
          rows={run?.controlTotals ?? []}
          getRowKey={(row) => `${row.name}|${formatKey(row.key)}`}
          loading={state.loading}
          empty={{ title: "No totals yet", description: "Totals appear once the run has loaded." }}
        />
      ) : null}
      {run && run.quarantined.length > 0 ? (
        <DataTable label="Quarantined records" columns={quarantineColumns} rows={run.quarantined} getRowKey={(row, i) => `${row.legacyId}|${i}`} />
      ) : null}
    </div>
  );
}
