import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, PageHeader, Select, Skeleton, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, etagOf, getScheduleRelease, resolveScheduleRelease, type Problem, type ReleaseBucket, type ScheduleReleaseLine } from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const bucketColumns: DataTableColumn<ReleaseBucket>[] = [
  { key: "date", header: "Date", mono: true },
  { key: "cumRequired", header: "Cumulative required", align: "end" },
];

const lineColumns: DataTableColumn<ScheduleReleaseLine>[] = [
  { key: "dueDate", header: "Due date", mono: true },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "status", header: "Status", render: (l) => <StatusPill status={l.status} /> },
];

/** One stored release with its schedule lines; a HELD release is accepted by override or rejected here. */
export function ScheduleReleaseDetailPage() {
  const { id = "", releaseId = "" } = useParams();
  const state = useLoad((signal) => getScheduleRelease(id, releaseId, signal), `${id}/${releaseId}`);
  const [decision, setDecision] = useState<"ACCEPT" | "REJECT">("REJECT");
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const back = (
    <Link className={buttonClassName("ghost")} to={`/sales/agreements/${id}/schedule-releases`}>
      Back to releases
    </Link>
  );
  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Scheduling release" actions={back} />
        <Alert tone="danger" title="Could not load the release" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Scheduling release" />
        <span className="visually-hidden">Loading the release…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const release = state.data.value;
  const etag = state.data.etag || etagOf(release.version);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const text = reason.trim();
    if (text.length < 1 || text.length > 500) {
      setReasonError("Enter a reason of up to 500 characters.");
      return;
    }
    setReasonError("");
    setBusy(true);
    try {
      await resolveScheduleRelease(id, releaseId, etag, decision, text);
      setReason("");
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const facts: [string, ReactNode][] = [
    ["Agreement", <Link key="a" to={`/sales/agreements/${release.agreementId}`}>{release.agreementNumber}</Link>],
    ["Type", release.releaseType],
    ["Cumulative received", release.cumReceived],
    ["Cumulative shipped", release.cumShipped],
    ...(release.shippedOffset && Number(release.shippedOffset) > 0
      ? ([["Shipped before the reset", release.shippedOffset]] as [string, ReactNode][])
      : []),
    ["Tolerance", release.tolerance],
    ["Difference", release.difference],
    ["Open quantity", release.openQuantity],
    ["Received", `${formatDateTime(release.receivedAt)} by ${release.receivedBy}`],
    ...(release.resolvedAt ? ([["Resolved", `${formatDateTime(release.resolvedAt)} by ${release.resolvedBy ?? ""}: ${release.resolutionReason ?? ""}`]] as [string, ReactNode][]) : []),
  ];
  const openLines = (release.lines ?? []).filter((l) => l.status === "OPEN");

  return (
    <div className={styles.stack}>
      <PageHeader title={`Release ${release.releaseNo}`} meta={<StatusPill status={release.status} />} actions={back} />
      {release.status === "HELD" ? (
        <Alert tone="warning" title="Held: cumulative quantity mismatch" live="status">
          The customer's cumulative received differs from shipped by {release.difference}, beyond the tolerance of {release.tolerance} ({release.code}, {release.serviceCode}). No schedule line was changed.
        </Alert>
      ) : null}
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
      {release.status === "HELD" ? (
        <Card title="Review the held release">
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Review the held release">
            <FormField id="resolve-decision" label="Decision">
              {(control) => (
                <Select {...control} value={decision} onChange={(e) => setDecision(e.target.value as "ACCEPT" | "REJECT")}>
                  <option value="REJECT">Reject the release</option>
                  <option value="ACCEPT">Accept despite the mismatch</option>
                </Select>
              )}
            </FormField>
            <FormField id="resolve-reason" label="Reason" error={reasonError}>
              {(control) => <Textarea {...control} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(reasonError)} />}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not resolve the release" live="alert">
                  {failure.message} ({failure.code})
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                {decision === "ACCEPT" ? "Accept release" : "Reject release"}
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <Card title="Open schedule lines">
        {openLines.length > 0 ? (
          <DataTable label="Open schedule lines" columns={lineColumns} rows={openLines} getRowKey={(l) => l.id} />
        ) : (
          <EmptyState compact headingLevel={3} title="No open lines" description="This release owns no open schedule lines: it is held, rejected, replaced, or everything required has shipped." />
        )}
      </Card>
      <Card title="Requirements as received">
        <DataTable label="Requirements" columns={bucketColumns} rows={release.buckets} getRowKey={(b, i) => `${i}-${b.date}`} />
      </Card>
    </div>
  );
}
