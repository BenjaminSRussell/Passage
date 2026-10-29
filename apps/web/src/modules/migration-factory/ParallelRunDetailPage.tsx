import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  ConfirmDialog,
  DataTable,
  FormField,
  PageHeader,
  Select,
  StatGroup,
  StatTile,
  StatusPill,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import { describeProblem, explainVariance, getParallelRun, newKey, signOffParallelRun, type Problem, type Variance, type VarianceClass } from "./api/client";
import { formatDateTime, money, SOURCE_LABELS } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const CLASSES: VarianceClass[] = ["UNEXPLAINED", "EXPLAINED", "EXPECTED"];

function conflictMessage(problem: Problem): string {
  if (problem.code === "MIG-0006") return "Someone changed this run since you opened it. The run has been reloaded; check it and try again.";
  if (problem.code === "MIG-0008") return `${problem.message}. Another reviewer must explain and sign off this run.`;
  return problem.message;
}

/** Parallel-run review: the variance report with reasons and sign-off (FR-49.5, 9.49.6). */
export function ParallelRunDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getParallelRun(id, signal), id);
  const run = state.data;
  const [filter, setFilter] = useState<VarianceClass | "">("");
  const [selected, setSelected] = useState<Variance | null>(null);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | undefined>();
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const explainKey = useRef(newKey());
  const signKey = useRef(newKey());
  const reviewing = run?.state === "REVIEW";

  const columns: DataTableColumn<Variance>[] = [
    { key: "area", header: "Area", mono: true },
    { key: "key", header: "Key", mono: true },
    { key: "legacy", header: "Legacy", align: "end", mono: true, render: (row) => money(row.legacy) },
    { key: "passage", header: "Passage", align: "end", mono: true, render: (row) => money(row.passage) },
    { key: "delta", header: "Delta", align: "end", mono: true, render: (row) => money(row.delta) },
    { key: "class", header: "Class", render: (row) => <StatusPill status={row.class} /> },
    { key: "rule", header: "Rule or reason", render: (row) => row.ruleId ?? row.reason ?? "" },
    {
      key: "action",
      header: "Action",
      render: (row) =>
        reviewing && row.class === "UNEXPLAINED" ? (
          <Button size="sm" onClick={() => choose(row)} aria-label={`Explain variance ${row.area} ${row.key}`}>
            Explain
          </Button>
        ) : row.explainedBy ? (
          `By ${row.explainedBy}`
        ) : null,
    },
  ];

  function choose(v: Variance) {
    setSelected(v);
    setReason("");
    setReasonError(undefined);
    setFailure(null);
    explainKey.current = newKey();
  }

  function fail(error: unknown) {
    const problem = describeProblem(error);
    setFailure({ ...problem, message: conflictMessage(problem) });
    if (problem.code === "MIG-0006") state.reload();
  }

  async function submitReason(event: FormEvent) {
    event.preventDefault();
    if (!run || !selected) return;
    const text = reason.trim();
    if (text === "" || text.length > 2000) {
      setReasonError("Give the reason for this variance, up to 2000 characters.");
      return;
    }
    setReasonError(undefined);
    setFailure(null);
    setBusy(true);
    try {
      state.set(await explainVariance(run.parallelRunId, selected.varianceId, text, run.version, explainKey.current));
      setSelected(null);
    } catch (error) {
      fail(error);
      explainKey.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  async function doSignOff() {
    if (!run) return;
    setConfirming(false);
    setFailure(null);
    setBusy(true);
    try {
      state.set(await signOffParallelRun(run.parallelRunId, run.version, signKey.current));
    } catch (error) {
      fail(error);
      signKey.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const variances = (run?.variances ?? []).filter((v) => filter === "" || v.class === filter);
  const expected = run ? Object.values(run.expectedByRule).reduce((a, b) => a + b, 0) : 0;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={run ? `Parallel run ${run.period}` : "Parallel run"}
        description={run ? `${SOURCE_LABELS[run.sourceSystem] ?? run.sourceSystem}, ${run.documentsReplayed} documents replayed, started ${formatDateTime(run.createdAt)}.` : undefined}
        meta={run ? <StatusPill status={run.state} /> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/migration-factory/parallel-runs">
            All parallel runs
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the parallel run" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="The change was not saved" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {run ? (
        <>
          <StatGroup label="Variances">
            <StatTile label="Unexplained" value={run.unexplainedCount} tone="danger" hint={money(run.unexplainedAmount)} />
            <StatTile label="Explained amount" value={money(run.explainedAmount)} hint={`Limit ${run.explainedMaxPct} % of ${money(run.revenue)}`} />
            <StatTile label="Expected by rule" value={expected} />
          </StatGroup>
          <Card title="Sign-off">
            {run.state === "SIGNED_OFF" ? (
              <p className={styles.note}>
                Signed off by {run.signedOffBy} on {formatDateTime(run.signedOffAt)}.
              </p>
            ) : (
              <div className={styles.form}>
                <p className={`${styles.note} ${styles.wide}`}>
                  {run.canSignOff
                    ? "No variance is unexplained and the explained amount is within the limit. The reviewer who signs off must not be the person who started the run."
                    : run.signOffBlocker ?? "The run can't be signed off yet."}
                </p>
                <div className={styles.formActions}>
                  <Button variant="primary" disabled={!run.canSignOff} loading={busy && !selected} onClick={() => setConfirming(true)}>
                    Sign off
                  </Button>
                </div>
              </div>
            )}
          </Card>
          {selected ? (
            <Card title={`Explain ${selected.area} ${selected.key}`}>
              <form className={styles.form} onSubmit={submitReason} noValidate>
                <p className={`${styles.note} ${styles.wide}`}>
                  Legacy {money(selected.legacy)}, Passage {money(selected.passage)}, delta {money(selected.delta)}.
                </p>
                <div className={styles.wide}>
                  <FormField id="variance-reason" label="Reason" error={reasonError}>
                    {(control) => <Textarea {...control} rows={3} maxLength={2000} value={reason} invalid={Boolean(reasonError)} onChange={(event) => setReason(event.target.value)} />}
                  </FormField>
                </div>
                <div className={styles.formActions}>
                  <Button type="submit" variant="primary" loading={busy}>
                    Save reason
                  </Button>
                  <Button variant="ghost" onClick={() => setSelected(null)}>
                    Cancel
                  </Button>
                </div>
              </form>
            </Card>
          ) : null}
          <div className={styles.toolbar}>
            <FormField id="variance-class" label="Class">
              {(control) => (
                <Select {...control} value={filter} onChange={(event) => setFilter(event.target.value as VarianceClass | "")}>
                  <option value="">All classes</option>
                  {CLASSES.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </>
      ) : null}
      {!state.error ? (
        <DataTable
          label="Variances"
          columns={columns}
          rows={variances}
          getRowKey={(row) => row.varianceId}
          loading={state.loading}
          empty={{ title: "No variances", description: filter ? "No variance has this class." : "Legacy and Passage agree on every tuple." }}
        />
      ) : null}
      {confirming ? (
        <ConfirmDialog
          title="Sign off this parallel run?"
          description="Your sign-off records that every variance is expected or explained. It can't be undone from this screen."
          confirmLabel="Sign off"
          cancelLabel="Keep reviewing"
          onConfirm={() => void doSignOff()}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </div>
  );
}
