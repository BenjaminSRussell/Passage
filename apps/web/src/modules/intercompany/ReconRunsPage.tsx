import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, ListPage, PageHeader, Select, StatGroup, StatTile, Textarea, type BadgeTone, type DataTableColumn, type ListData } from "../../shell/ui";
import { describeProblem, type Problem, type ReconStatus } from "./api/client";
import { getReconRun, listReconRuns, resolveItem, type ReconRun, type StoredReconItem } from "./api/postings";
import { adjustFx, type FxAdjustment } from "./api/registry";
import { amount, isNonZero } from "./format";
import { usePagedRows } from "./usePagedRows";
import styles from "./pages.module.css";

const statusTone: Record<ReconStatus, BadgeTone> = { Matched: "success", Timing: "warning", Mismatch: "danger" };

const runColumns: DataTableColumn<ReconRun>[] = [
  { key: "period", header: "Period", mono: true, render: (r) => r.period },
  { key: "currency", header: "Currency", mono: true, render: (r) => r.currency },
  { key: "matched", header: "Matched", align: "end", render: (r) => String(r.matched) },
  { key: "timing", header: "Timing", align: "end", render: (r) => String(r.timing) },
  { key: "mismatch", header: "Mismatch", align: "end", render: (r) => <span className={r.mismatch > 0 ? styles.danger : undefined}>{r.mismatch}</span> },
  { key: "open", header: "Still to resolve", align: "end", render: (r) => String(r.open) },
  { key: "source", header: "Amounts from", render: (r) => (r.source === "ledger" ? "Ledgers" : "Entered pairs") },
  { key: "at", header: "Run", render: (r) => r.createdAt.slice(0, 16).replace("T", " ") },
];

/** Stored reconciliation runs, newest first (FR-40.6). */
export function ReconRunsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="intercompany-recon-runs"
      title="Reconciliation runs"
      description="Each run classified every entity pair of a period. Open a run to see the pairs that need action and resolve them with a note, or book the exchange difference of a pair in two currencies."
      actions={
        <>
          <Link className={buttonClassName("primary")} to="/intercompany/ledger-reconciliation">
            Reconcile from the ledgers
          </Link>
          <Link className={buttonClassName("secondary")} to="/intercompany/reconciliation">
            Classify and save a run
          </Link>
        </>
      }
      noun="runs"
      singular="run"
      columns={runColumns}
      useRows={useRunRows}
      getRowKey={(r) => r.id}
      onRowOpen={(r) => navigate(`/intercompany/reconciliation-runs/${r.id}`)}
      empty={{ title: "No runs yet", description: "Reconcile a period from the ledgers, or classify its balance pairs and save the result as a run." }}
    />
  );
}

function useRunRows(): ListData<ReconRun> {
  return usePagedRows((after, signal) => listReconRuns(after, signal), "runs");
}

/** One stored run: its pairs, filtered by status, with a way to resolve each open one. */
export function ReconRunPage() {
  const { id = "" } = useParams();
  const [status, setStatus] = useState("");
  const [run, setRun] = useState<(ReconRun & { items: StoredReconItem[] }) | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [resolving, setResolving] = useState<StoredReconItem | null>(null);
  const [reload, setReload] = useState(0);
  const [adjusting, setAdjusting] = useState<string | null>(null);
  const [adjusted, setAdjusted] = useState<FxAdjustment | null>(null);
  const [adjustFailure, setAdjustFailure] = useState<Problem | null>(null);

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
  async function bookFx(item: StoredReconItem) {
    setAdjusting(item.id);
    setAdjustFailure(null);
    setAdjusted(null);
    try {
      setAdjusted(await adjustFx(item.id));
      setReload((n) => n + 1);
    } catch (err) {
      setAdjustFailure(describeProblem(err));
    } finally {
      setAdjusting(null);
    }
  }
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
          <span className={styles.links}>
            <Button size="sm" onClick={() => setResolving(i)}>
              Resolve
            </Button>
            {i.fxApplies && i.status === "Mismatch" && run.source === "ledger" ? (
              <Button size="sm" loading={adjusting === i.id} disabled={adjusting !== null} aria-label={`Book the exchange difference of ${i.ref ?? "this pair"}`} onClick={() => void bookFx(i)}>
                Book the exchange difference
              </Button>
            ) : null}
          </span>
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
      {adjusted ? (
        <Alert tone="success" title="Exchange difference booked" live="status">
          {adjusted.entity} booked {amount(adjusted.amount)} {adjusted.currency} on {adjusted.postedOn}: its account {adjusted.account} went {adjusted.direction === "increase" ? "up" : "down"} against the FX difference account {adjusted.fxAccount}. The pair is resolved.
        </Alert>
      ) : null}
      {adjustFailure ? (
        <Alert tone="danger" title="The exchange difference was not booked" live="alert">
          <ProblemMessage problem={adjustFailure} />
        </Alert>
      ) : null}
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
