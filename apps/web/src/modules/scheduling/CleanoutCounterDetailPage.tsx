import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getCleanoutCounter, newKey, resetCleanoutCounter, type CleanoutReset, type CleanoutResetResult, type Problem } from "./api/client";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const MOVED = "SCHED-0148";

/** One resource's cleanout counter, the form that resets it after a cleanout outside the plan, and the resets so far (FR-23.15). */
export function CleanoutCounterDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getCleanoutCounter(id, signal), id);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<CleanoutResetResult | null>(null);
  const key = useRef(newKey());
  const counter = state.data;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!counter) return;
    const text = reason.trim();
    if (text === "" || text.length > 200) {
      setReasonError(text === "" ? "Say why the resource was cleaned out." : "Use at most 200 characters.");
      return;
    }
    setReasonError(undefined);
    setFailure(null);
    setDone(null);
    setBusy(true);
    try {
      const result = await resetCleanoutCounter(id, { batchesSince: counter.batchesSince, minutesSince: counter.minutesSince, reason: text }, key.current);
      setDone(result);
      setReason("");
      key.current = newKey();
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
      if (problem.code === MOVED) state.reload();
    } finally {
      setBusy(false);
    }
  }

  const back = <Link className={buttonClassName("secondary")} to="/scheduling/cleanout-counters">All counters</Link>;

  if (state.error && !counter) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Cleanout counter" actions={back} />
        {state.error.code === "SCHED-0102" ? (
          <EmptyState title="Resource not found" description="No resource has this id." />
        ) : (
          <Alert tone="danger" title="Could not load the cleanout counter" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        )}
      </div>
    );
  }
  if (!counter) {
    return (
      <div className={styles.loading} aria-busy="true">
        <span className="visually-hidden">Loading the cleanout counter…</span>
        <Skeleton height="control" />
        <Skeleton height="control" />
      </div>
    );
  }

  const facts: [string, string][] = [
    ["Batches since the last cleanout", String(counter.batchesSince)],
    ["Run minutes since the last cleanout", String(counter.minutesSince)],
    ["Left by campaign", counter.lastCampaignId ?? dash],
    ["Counter changed", formatDateTime(counter.updatedAt)],
    ["Last reset", counter.lastResetAt ? formatDateTime(counter.lastResetAt) : dash],
  ];
  const columns: DataTableColumn<CleanoutReset>[] = [
    { key: "createdAt", header: "Reset", render: (row) => formatDateTime(row.createdAt) },
    { key: "previousBatches", header: "Batches cleared", align: "end" },
    { key: "previousMinutes", header: "Minutes cleared", align: "end" },
    { key: "reason", header: "Reason" },
    { key: "resetBy", header: "By", mono: true, render: (row) => row.resetBy || dash },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Cleanout counter of ${counter.resourceCode}`}
        description={counter.resourceName}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/scheduling/resources/${counter.resourceId}`}>
              Open the resource
            </Link>
            {back}
          </>
        }
      />
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <p className={styles.note}>
            Use this when the resource was cleaned out outside the plan. The counter goes back to zero. Planned campaigns sequenced from the old counter
            cannot start until they are sequenced again.
          </p>
          <FormField id="cleanout-reason" label="Reason" error={reasonError}>
            {(control) => <Input {...control} maxLength={200} value={reason} onChange={(event) => setReason(event.target.value)} invalid={Boolean(reasonError)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === MOVED ? "The counter moved" : "Could not reset the counter"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Reset counter
            </Button>
          </div>
        </form>
        <Alert tone="success" live="status">
          {done ? "The counter was reset." : ""}
        </Alert>
        {done && done.staleCampaignIds.length > 0 ? (
          <Alert tone="warning" title={`${done.staleCampaignIds.length} planned campaign(s) must be sequenced again`}>
            {done.staleCampaignIds.map((campaignId) => (
              <span key={campaignId}>
                <Link to={`/scheduling/campaigns/${campaignId}`}>{campaignId.slice(0, 8)}</Link>{" "}
              </span>
            ))}
          </Alert>
        ) : null}
      </Card>
      <DataTable
        label="Resets"
        columns={columns}
        rows={counter.resets}
        getRowKey={(row) => row.id}
        empty={{ title: "No resets", description: "Nobody has reset this counter by hand." }}
      />
    </div>
  );
}
