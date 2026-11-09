import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getLag, listRuns, type Problem, type RunPage, type RunQuery, type RunSummary } from "./api/client";
import { periodLabel, shortHash, UUID } from "./format";
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

const PERIOD = /^\d{4}-\d{2}$/;

/** The filters the service accepts: a UUID ledger and a period as YYYY-PP. An invalid value is not sent. */
export function runQuery(definition: string, ledger: string, period: string): { query: RunQuery; errors: { ledger?: string; period?: string } } {
  const query: RunQuery = {};
  const errors: { ledger?: string; period?: string } = {};
  if (definition.trim() !== "") query.definition = definition.trim().toUpperCase();
  if (ledger.trim() !== "") {
    if (UUID.test(ledger.trim())) query.ledger = ledger.trim();
    else errors.ledger = "Enter the ledger id (a UUID), or leave it empty for every ledger.";
  }
  if (period.trim() !== "") {
    if (PERIOD.test(period.trim())) query.period = period.trim();
    else errors.period = "Enter the fiscal year and period number, such as 2026-03.";
  }
  return { query, errors };
}

/** Financial statements: the report library of runs (FR-15.6), keyset paged, and the read-model lag (FR-15.1). */
export function RunsPage() {
  const navigate = useNavigate();
  const [definition, setDefinition] = useState("");
  const [ledger, setLedger] = useState("");
  const [period, setPeriod] = useState("");
  const { query, errors } = runQuery(definition, ledger, period);
  const key = JSON.stringify(query);
  const first = useLoad((signal) => listRuns(query, undefined, signal), key);
  const [more, setMore] = useState<RunPage[]>([]);
  const [moreBusy, setMoreBusy] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  // The filter a Load more answer belongs to: a page that arrives after the
  // filter changed is dropped rather than added to the new list.
  const current = useRef(key);
  useEffect(() => {
    current.current = key;
    setMore([]);
    setMoreError(null);
  }, [key]);

  const pages = first.data ? [first.data, ...more] : [];
  const items = pages.flatMap((p) => p.items);
  const nextCursor = pages.length > 0 ? pages[pages.length - 1].nextCursor : null;

  const loadMore = async () => {
    if (!nextCursor) return;
    const asked = key;
    setMoreBusy(true);
    setMoreError(null);
    try {
      const page = await listRuns(query, nextCursor);
      if (current.current === asked) setMore((m) => [...m, page]);
    } catch (failure) {
      if (current.current === asked) setMoreError(describeProblem(failure));
    } finally {
      setMoreBusy(false);
    }
  };

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
      <div className={styles.form}>
        <FormField id="fr-runs-definition" label="Definition code" optional>
          {(c) => <Input {...c} className={styles.mono} value={definition} onChange={(e) => setDefinition(e.target.value)} />}
        </FormField>
        <FormField id="fr-runs-period" label="Period" hint="Fiscal year and period, such as 2026-03." optional error={errors.period}>
          {(c) => <Input {...c} className={styles.mono} value={period} invalid={Boolean(errors.period)} onChange={(e) => setPeriod(e.target.value)} />}
        </FormField>
        <FormField id="fr-runs-ledger" label="Ledger" hint="Ledger id. Empty means every ledger." optional error={errors.ledger}>
          {(c) => <Input {...c} className={styles.mono} value={ledger} invalid={Boolean(errors.ledger)} onChange={(e) => setLedger(e.target.value)} />}
        </FormField>
      </div>
      {first.error ? (
        <Alert tone="danger" title="Could not load the statement runs" live="alert" action={<Button size="sm" onClick={first.reload}>Try again</Button>}>
          {first.error.message} ({first.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Statement runs"
          columns={columns}
          rows={items}
          getRowKey={(r) => r.id}
          loading={first.loading && !first.data}
          empty={
            key === "{}"
              ? { title: "No statement runs yet", description: "Run a published definition to produce a statement." }
              : { title: "No runs match these filters", description: "Change or clear the definition, period or ledger." }
          }
          onRowOpen={(r) => navigate(`/finreport/runs/${r.id}`)}
        />
      )}
      {nextCursor && !first.error ? (
        <div className={styles.drillFooter}>
          <span className={styles.note}>Showing the newest {items.length} runs.</span>
          <Button onClick={loadMore} loading={moreBusy}>
            Load more
          </Button>
        </div>
      ) : null}
      {moreError ? (
        <Alert tone="danger" live="alert" title="Could not load more runs">
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
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
