import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Select, StatGroup, StatTile, Textarea, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem, type ReconStatus } from "./api/client";
import { getReconRun, listReconRuns, resolveItem, type ReconRun, type StoredReconItem } from "./api/postings";
import { amount, isNonZero } from "./format";
import styles from "./pages.module.css";

const statusTone: Record<ReconStatus, BadgeTone> = { Matched: "success", Timing: "warning", Mismatch: "danger" };

const runColumns: DataTableColumn<ReconRun>[] = [
  { key: "period", header: "Period", mono: true, render: (r) => r.period },
  { key: "currency", header: "Currency", mono: true, render: (r) => r.currency },
  { key: "matched", header: "Matched", align: "end", render: (r) => String(r.matched) },
  { key: "timing", header: "Timing", align: "end", render: (r) => String(r.timing) },
  { key: "mismatch", header: "Mismatch", align: "end", render: (r) => <span className={r.mismatch > 0 ? styles.danger : undefined}>{r.mismatch}</span> },
  { key: "open", header: "Still to resolve", align: "end", render: (r) => String(r.open) },
  { key: "at", header: "Run", render: (r) => r.createdAt.slice(0, 16).replace("T", " ") },
];

/** Stored reconciliation runs, newest first (FR-40.6). */
export function ReconRunsPage() {
  const navigate = useNavigate();
  const [state, setState] = useState<{ loading: boolean; items: ReconRun[]; next?: string; failure: Problem | null }>({ loading: true, items: [], failure: null });
  const [reload, setReload] = useState(0);
  const [more, setMore] = useState(false);

  useEffect(() => {
    const ctl = new AbortController();
    setState((s) => ({ ...s, loading: true, failure: null }));
    listReconRuns(undefined, ctl.signal)
      .then((page) => setState({ loading: false, items: page.items, next: page.nextCursor, failure: null }))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setState({ loading: false, items: [], failure: describeProblem(err) });
      });
    return () => ctl.abort();
  }, [reload]);

  async function loadMore() {
    if (!state.next) return;
    setMore(true);
    try {
      const page = await listReconRuns(state.next);
      setState((s) => ({ ...s, items: [...s.items, ...page.items], next: page.nextCursor }));
    } catch (err) {
      setState((s) => ({ ...s, failure: describeProblem(err) }));
    } finally {
      setMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Reconciliation runs"
        description="Each run classified every entity pair of a period. Open a run to see the pairs that need action and resolve them with a note. Start a run from the reconciliation page."
        actions={<Link to="/intercompany/reconciliation">Classify and save a run</Link>}
      />
      {state.failure ? (
        <Alert tone="danger" title="We couldn't load the runs" live="alert" action={<Button size="sm" onClick={() => setReload((n) => n + 1)}>Try again</Button>}>
          <ProblemMessage problem={state.failure} />
        </Alert>
      ) : (
        <DataTable
          label="Reconciliation runs"
          columns={runColumns}
          rows={state.items}
          getRowKey={(r) => r.id}
          loading={state.loading}
          onRowOpen={(r) => navigate(`/intercompany/reconciliation-runs/${r.id}`)}
          empty={{ title: "No runs yet", description: "Classify a period's balance pairs and save the result as a run." }}
        />
      )}
      {state.next && !state.failure ? (
        <div className={styles.formActions}>
          <Button loading={more} onClick={() => void loadMore()}>
            Show more runs
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** One stored run: its pairs, filtered by status, with a way to resolve each open one. */
export function ReconRunPage() {
  const { id = "" } = useParams();
  const [status, setStatus] = useState("");
  const [run, setRun] = useState<(ReconRun & { items: StoredReconItem[] }) | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [resolving, setResolving] = useState<StoredReconItem | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const ctl = new AbortController();
    setFailure(null);
    getReconRun(id, status || undefined, ctl.signal)
      .then(setRun)
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setFailure(describeProblem(err));
      });
    return () => ctl.abort();
  }, [id, status, reload]);
  const resolved = useCallback(() => {
    setResolving(null);
    setReload((n) => n + 1);
  }, []);

  if (failure) {
    return (
      <Alert tone="danger" title="We couldn't load this run" live="alert" action={<Button size="sm" onClick={() => setReload((n) => n + 1)}>Try again</Button>}>
        <ProblemMessage problem={failure} />
      </Alert>
    );
  }
  if (!run) return <p className={styles.note}>Loading the run…</p>;

  const currency = run.currency;
  const columns: DataTableColumn<StoredReconItem>[] = [
    { key: "ref", header: "Reference", mono: true, render: (i) => i.ref ?? "—" },
    { key: "pair", header: "Pair", mono: true, render: (i) => `${i.entityA} / ${i.entityB}` },
    { key: "a", header: `Amount A (${currency})`, align: "end", render: (i) => amount(i.amountA) },
    { key: "b", header: `Amount B (${currency})`, align: "end", render: (i) => amount(i.amountB) },
    { key: "diff", header: "Difference", align: "end", render: (i) => <span className={isNonZero(i.difference) ? styles.danger : undefined}>{amount(i.difference)}</span> },
    {
      key: "status",
      header: "Status",
      render: (i) => (
        <>
          <Badge tone={statusTone[i.status]}>{i.status}</Badge>
          {i.toleranceApplied ? <span className={styles.note}> within FX tolerance</span> : null}
        </>
      ),
    },
    {
      key: "action",
      header: "Resolution",
      render: (i) =>
        i.resolution === "open" ? (
          <Button size="sm" onClick={() => setResolving(i)}>
            Resolve
          </Button>
        ) : i.resolution === "resolved" ? (
          <span>
            Resolved by {i.resolvedBy}: {i.note}
          </span>
        ) : (
          "Nothing to do"
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title={`Reconciliation ${run.period}`} description={`Run in ${currency} with an FX tolerance of ${run.fxTolerancePct} %.`} actions={<Link to="/intercompany/reconciliation-runs">All runs</Link>} />
      <StatGroup label="Run summary">
        <StatTile label="Matched" value={run.matched} />
        <StatTile label="Timing" value={run.timing} tone="warning" />
        <StatTile label="Mismatch" value={run.mismatch} tone="danger" />
        <StatTile label="Still to resolve" value={run.open} tone="warning" />
      </StatGroup>
      {run.blocking ? (
        <Alert tone="warning" title="Mismatches block consolidation">
          Resolve every mismatch before the period's consolidation runs, unless a group approver overrides it (CNS-0901).
        </Alert>
      ) : null}
      <Card>
        <div className={styles.filters}>
          <FormField id="run-status" label="Show">
            {(c) => (
              <Select {...c} value={status} onChange={(e) => setStatus(e.target.value)}>
                <option value="">All pairs</option>
                <option value="Mismatch">Mismatches</option>
                <option value="Timing">Timing differences</option>
                <option value="Matched">Matched</option>
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      <DataTable label="Reconciliation items" columns={columns} rows={run.items} getRowKey={(i) => i.id} empty={{ title: "No pairs", description: "No pair of this run has that status." }} />
      {resolving ? <ResolveForm item={resolving} onDone={resolved} onCancel={() => setResolving(null)} /> : null}
    </div>
  );
}

function ResolveForm({ item, onDone, onCancel }: { item: StoredReconItem; onDone(): void; onCancel(): void }) {
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (note.trim() === "") {
      setError("Say how the difference was settled.");
      return;
    }
    setError("");
    setBusy(true);
    try {
      await resolveItem(item.id, note.trim());
      onDone();
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title={`Resolve ${item.ref ?? "this pair"}`} headingLevel={3}>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
        <p className={styles.note}>
          Entity {item.entityA} and {item.entityB} differ by {amount(item.difference)}. A resolved pair stays in the run with your note; it can't be reopened.
        </p>
        <FormField id="resolve-note" label="How it was settled" error={error}>
          {(c) => <Textarea {...c} value={note} onChange={(e) => setNote(e.target.value)} invalid={Boolean(error)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The pair was not resolved" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Mark resolved
          </Button>
          <Button onClick={onCancel}>Cancel</Button>
        </div>
      </form>
    </Card>
  );
}
