import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getLag, listRuns, type RunSummary } from "./api/client";
import { periodLabel, shortHash } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<RunSummary>[] = [
  { key: "definitionCode", header: "Definition", mono: true, render: (r) => `${r.definitionCode} v${r.definitionVersion}` },
  { key: "period", header: "Period", mono: true, render: (r) => periodLabel(r.periodYear, r.periodNo) },
  { key: "currencyType", header: "Currency type", mono: true },
  { key: "status", header: "Status", render: (r) => <StatusPill status={r.status} /> },
  { key: "hwm", header: "As of sequence", mono: true, align: "end", render: (r) => Object.values(r.hwmByLedger).join(", ") },
  { key: "resultHash", header: "Result hash", mono: true, render: (r) => shortHash(r.resultHash) },
  { key: "startedAt", header: "Generated", mono: true, render: (r) => <time dateTime={r.startedAt}>{r.startedAt}</time> },
];

/** Financial statements: the report library of runs (FR-15.6) and the read-model lag (FR-15.1). */
export function RunsPage() {
  const navigate = useNavigate();
  const [definition, setDefinition] = useState("");
  const filter = definition.trim().toUpperCase();
  const runs = useLoad((signal) => listRuns(filter, signal), filter);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Financial statements"
        description="Statement runs produced from the journal. Open a run to see the statement and drill from any cell to the balances behind it."
        actions={
          <Link className={buttonClassName("primary")} to="/finreport/runs/new">
            Run a statement
          </Link>
        }
      />
      <FormField id="fr-runs-definition" label="Definition code" optional>
        {(c) => <Input {...c} className={styles.mono} value={definition} onChange={(e) => setDefinition(e.target.value)} />}
      </FormField>
      {runs.error ? (
        <Alert tone="danger" title="Could not load the statement runs" live="alert" action={<Button size="sm" onClick={runs.reload}>Try again</Button>}>
          {runs.error.message} ({runs.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Statement runs"
          columns={columns}
          rows={runs.data ?? []}
          getRowKey={(r) => r.id}
          loading={runs.loading}
          empty={{ title: "No statement runs yet", description: "Run a published definition to produce a statement." }}
          onRowOpen={(r) => navigate(`/finreport/runs/${r.id}`)}
        />
      )}
      <LagPanel />
    </div>
  );
}

/** Read-model high-water mark per ledger: what sequence every new statement reflects. */
export function LagPanel() {
  const lag = useLoad((signal) => getLag(signal), "lag");
  return (
    <Card title="Read-model lag">
      {lag.error ? (
        <Alert tone="danger" title="Lag is unavailable" live="alert" action={<Button size="sm" onClick={lag.reload}>Try again</Button>}>
          {lag.error.message} ({lag.error.code})
        </Alert>
      ) : lag.loading && !lag.data ? (
        <div aria-busy="true">
          <Skeleton width="3/4" />
        </div>
      ) : (lag.data?.items ?? []).length === 0 ? (
        <EmptyState compact title="No postings yet" description="The read model has not received any journal events." />
      ) : (
        <ul className={styles.lagList} aria-label="Read-model lag">
          {(lag.data?.items ?? []).map((r) => {
            const gate = lag.data?.maxLagSeconds;
            const stale = gate !== undefined && (r.lagSeconds ?? 0) > gate;
            return (
            <li key={r.ledgerId} className={styles.lagItem}>
              <span className={styles.mono}>Ledger {r.ledgerId}</span>
              <span>
                As of sequence <span className={styles.mono}>{r.postingSequence}</span>
                {r.entriesApplied !== undefined ? <>, {r.entriesApplied} {r.entriesApplied === 1 ? "entry" : "entries"} applied</> : null}, updated <time dateTime={r.updatedAt}>{r.updatedAt}</time>
              </span>
              {r.entriesPending ? (
                <span>
                  {r.entriesPending} later {r.entriesPending === 1 ? "entry waits" : "entries wait"} for sequence <span className={styles.mono}>{r.postingSequence + 1}</span>
                </span>
              ) : null}
              {stale ? (
                <Alert tone="warning" title="Statements are stale for this ledger">
                  The read model is at least {r.lagSeconds} s behind the ledger (limit {gate} s). A run is refused with REPORT-0004 until it catches up, unless it accepts stale results.
                </Alert>
              ) : null}
            </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}
