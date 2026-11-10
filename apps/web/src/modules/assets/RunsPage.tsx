import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getRun, listRuns, newKey, postRun, proposeRun, type DepreciationRun, type Problem, type RunInput, type RunLine } from "./api/client";
import { StatusBadge } from "./badges";
import { amount, dash, PERIOD, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface RunValues {
  legalEntityId: string;
  bookCode: string;
  period: string;
}

type Errors = Partial<Record<keyof RunValues, string>>;

/** Checks that mirror the service (internal/api proposeRun). */
export function validateRun(values: RunValues): { errors: Errors; input?: RunInput } {
  const errors: Errors = {};
  const legalEntityId = values.legalEntityId.trim();
  const bookCode = values.bookCode.trim().toUpperCase();
  const period = values.period.trim();
  if (!UUID.test(legalEntityId)) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!/^[A-Z0-9][A-Z0-9_-]{0,19}$/.test(bookCode)) errors.bookCode = "Enter the book code, such as IFRS or TAX.";
  if (!PERIOD.test(period)) errors.period = "Enter YYYY-MM for monthly books or YYYY for yearly books.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { legalEntityId, bookCode, period } };
}

const runColumns: DataTableColumn<DepreciationRun>[] = [
  { key: "period", header: "Period", mono: true },
  { key: "bookCode", header: "Book", mono: true },
  { key: "status", header: "Status", render: (r) => <StatusBadge status={r.status} /> },
  { key: "assetCount", header: "Books", align: "end" },
  { key: "exceptionCount", header: "Exceptions", align: "end" },
  { key: "totalAmount", header: "Total", align: "end", render: (r) => amount(r.totalAmount, r.currency) },
  { key: "postedAt", header: "Posted", render: (r) => (r.postedAt ? r.postedAt.slice(0, 10) : dash) },
];

/** Depreciation runs (FR-37.5): propose for a book and period, then review and post. */
export function RunsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listRuns(undefined, signal), "runs");
  const [more, setMore] = useState<DepreciationRun[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [values, setValues] = useState<RunValues>({ legalEntityId: "", bookCode: "IFRS", period: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  function update(patch: Partial<RunValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listRuns(cursor);
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRun(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const run = await proposeRun(input, key.current);
      navigate(`/assets/depreciation-runs/${run.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as Errors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const failureTitle: Record<string, string> = {
    "AST-0502": "That period is already posted",
    "AST-0504": "Another run holds this book and period",
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Depreciation runs"
        description="Propose depreciation for one book of a legal entity and one period, review the per-asset amounts and exceptions, then post. Books with an earlier unposted period stay out of the run as exceptions; the others post."
      />
      <Card title="Propose a run">
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Propose a run">
          <FormField id="run-legalEntityId" label="Legal entity" hint="UUID of the legal entity." error={errors.legalEntityId}>
            {(control) => <Input {...control} className={styles.mono} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="run-bookCode" label="Book" error={errors.bookCode}>
            {(control) => <Input {...control} className={styles.mono} value={values.bookCode} onChange={(e) => update({ bookCode: e.target.value.toUpperCase() })} invalid={Boolean(errors.bookCode)} />}
          </FormField>
          <FormField id="run-period" label="Period" hint="2026-03 for monthly books, 2026 for yearly books." error={errors.period}>
            {(control) => <Input {...control} className={styles.mono} value={values.period} onChange={(e) => update({ period: e.target.value })} invalid={Boolean(errors.period)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failureTitle[failure.code] ?? "Could not propose the run"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Propose run
            </Button>
          </div>
        </form>
      </Card>
      {state.error ? (
        <Alert tone="danger" title="Could not load depreciation runs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Depreciation runs"
          columns={runColumns}
          rows={[...(state.data?.items ?? []), ...more]}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No depreciation runs yet", description: "Propose a run above; it appears here with its total and exceptions." }}
          onRowOpen={(r) => navigate(`/assets/depreciation-runs/${r.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more runs" live="alert">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}
      {cursor && !state.error ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function lineColumns(currency: string | null): DataTableColumn<RunLine>[] {
  return [
    { key: "assetNumber", header: "Asset", mono: true },
    { key: "status", header: "Status", render: (l) => <StatusBadge status={l.status} /> },
    { key: "scheduledAmount", header: "Scheduled", align: "end", render: (l) => amount(l.scheduledAmount, currency) },
    { key: "amount", header: "Charge", align: "end", render: (l) => amount(l.amount, currency) },
    { key: "exceptionCode", header: "Exception", render: (l) => (l.exceptionCode ? `${l.exceptionCode}: ${l.detail ?? ""}` : dash) },
  ];
}

/** One run: totals, per-book lines with exceptions, and Post for a proposal. */
export function RunDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getRun(id, signal), id);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [posted, setPosted] = useState<DepreciationRun | null>(null);
  const [busy, setBusy] = useState(false);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Depreciation run" />
        <Alert tone="danger" title="Could not load the run" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link className={buttonClassName("ghost")} to="/assets/depreciation-runs">
          Back to runs
        </Link>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Depreciation run" />
        <span className="visually-hidden">Loading the run…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { etag } = state.data;
  const run = posted ?? state.data.value;

  async function post() {
    setBusy(true);
    setFailure(null);
    try {
      setPosted((await postRun(run.id, etag)).value);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Depreciation ${run.bookCode} ${run.period}`}
        meta={<StatusBadge status={run.status} />}
        actions={
          run.status === "Proposed" ? (
            <Button variant="primary" onClick={post} loading={busy}>
              Post run
            </Button>
          ) : null
        }
      />
      {posted ? (
        <Alert tone="success" title="Run posted" live="status">
          {`${posted.assetCount} books posted ${amount(posted.totalAmount, posted.currency)}${posted.exceptionCount > 0 ? `; ${posted.exceptionCount} stayed out as exceptions` : ""}.`}
        </Alert>
      ) : null}
      {failure ? (
        <Alert
          tone="danger"
          title={failure.code === "AST-0006" ? "The run was proposed again since you opened it" : "Could not post the run"}
          live="alert"
          action={failure.code === "AST-0006" ? <Button size="sm" onClick={state.reload}>Reload</Button> : undefined}
        >
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <StatGroup label="Run totals">
        <StatTile label={run.status === "Posted" ? "Books posted" : "Books due"} value={run.assetCount} />
        <StatTile label="Exceptions" value={run.exceptionCount} tone={run.exceptionCount > 0 ? "warning" : "neutral"} />
        <StatTile label="Total" value={amount(run.totalAmount, run.currency)} />
      </StatGroup>
      <p className={styles.note}>
        {run.journalEntryId ? `Journal entry ${run.journalEntryId}.` : "The general-ledger journal is linked when the ledger integration lands; the asset subledger is posted now."}
      </p>
      <Card title="Lines">
        <DataTable
          label="Run lines"
          columns={lineColumns(run.currency)}
          rows={run.lines ?? []}
          getRowKey={(l) => l.assetBookId}
          empty={{ title: "Nothing to depreciate", description: "No open book with this code has a schedule line in the period." }}
        />
      </Card>
      <div>
        <Link className={buttonClassName("ghost")} to="/assets/depreciation-runs">
          Back to runs
        </Link>
      </div>
    </div>
  );
}
