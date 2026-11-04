import { useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  AGREEMENT_NEXT,
  describeProblem,
  newKey,
  readAgreement,
  transitionAgreement,
  type AgreementLine,
  type AgreementTarget,
  type Problem,
  type Versioned,
  type Agreement,
} from "./api/client";
import { AgreementVisitsPanel } from "./AgreementVisitsPanel";
import { Details } from "./Details";
import { dash, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** The verb on each status button. */
const actionLabel = (from: Agreement["status"], to: AgreementTarget) => (to === "ACTIVE" ? (from === "SUSPENDED" ? "Resume" : "Activate") : to === "SUSPENDED" ? "Suspend" : "Cancel agreement");
const needsReason = (to: AgreementTarget) => to !== "ACTIVE";

type Row = AgreementLine & { id: string };

const yes = (on: boolean) => (on ? <Badge tone="success">Covered</Badge> : <span className={styles.muted}>Billable</span>);

const lineColumns: DataTableColumn<Row>[] = [
  { key: "scope", header: "Covers", mono: true, render: (row) => row.serviceObjectId ?? row.categoryId ?? "Any object" },
  { key: "includedVisits", header: "Visits", align: "end" },
  { key: "includedHours", header: "Hours", align: "end" },
  { key: "partsCovered", header: "Parts", render: (row) => yes(row.partsCovered) },
  { key: "laborCovered", header: "Labor", render: (row) => yes(row.laborCovered) },
  { key: "travelCovered", header: "Travel", render: (row) => yes(row.travelCovered) },
];

/** One agreement: validity, SLA profile, billing, lines and entitlement usage (PRD 8.45.6 "Agreement detail"). */
export function AgreementDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => readAgreement(id, signal), id);
  const [latest, setLatest] = useState<Versioned<Agreement> | null>(null);
  const [pending, setPending] = useState<AgreementTarget | null>(null);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const key = useRef(newKey());
  const current = latest && latest.value.id === id ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Service agreement" />
        <Alert tone="danger" title="Could not load the agreement" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Service agreement" />
        <span className="visually-hidden">Loading the agreement…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: a, etag } = current;

  async function change(to: AgreementTarget, why: string) {
    setBusy(to);
    setFailure(null);
    setNotice("");
    try {
      const next = await transitionAgreement(a.id, etag, to, why, key.current);
      key.current = newKey();
      setLatest(next);
      setPending(null);
      setReason("");
      setNotice(`Agreement is now ${sentence(next.value.status).toLowerCase()}.`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "conflict_version") {
        // Drop the answer of an earlier move too, or it would hide the reload.
        setLatest(null);
        setPending(null);
        state.reload();
      }
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(null);
    }
  }

  function choose(to: AgreementTarget) {
    setFailure(null);
    setNotice("");
    if (needsReason(to)) {
      setPending(to);
      setReasonError("");
    } else {
      void change(to, "");
    }
  }

  function confirm() {
    if (!pending) return;
    if (!reason.trim()) {
      setReasonError("Give a reason; it is kept in the status history.");
      return;
    }
    if (reason.length > 500) {
      setReasonError("Keep the reason to 500 characters.");
      return;
    }
    void change(pending, reason.trim());
  }

  const moves = AGREEMENT_NEXT[a.status];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Service agreement ${a.number}`}
        meta={<StatusPill status={a.status} />}
        actions={
          moves.length > 0 ? (
            <div className={styles.actions}>
              {moves.map((to) => (
                <Button key={to} variant={to === "CANCELED" ? "danger" : to === "ACTIVE" ? "primary" : "secondary"} loading={busy === to} onClick={() => choose(to)}>
                  {actionLabel(a.status, to)}
                </Button>
              ))}
            </div>
          ) : undefined
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.code === "conflict_version" ? "Someone else changed this agreement" : "The action failed"} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {pending ? (
        <Card title={pending === "SUSPENDED" ? "Suspend the agreement" : "Cancel the agreement"} headingLevel={3}>
          <form
            className={styles.form}
            noValidate
            aria-label="Status change reason"
            onSubmit={(e) => {
              e.preventDefault();
              confirm();
            }}
          >
            <FormField id="agreement-reason" label="Reason" hint={pending === "CANCELED" ? "A canceled agreement is final." : "A suspended agreement entitles nothing until it is resumed."} error={reasonError}>
              {(control) => <Input {...control} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(reasonError)} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant={pending === "CANCELED" ? "danger" : "primary"} loading={busy === pending}>
                {pending === "SUSPENDED" ? "Confirm suspension" : "Confirm cancellation"}
              </Button>
              <Button onClick={() => setPending(null)}>Keep as is</Button>
            </div>
          </form>
        </Card>
      ) : null}
      <Card title="Agreement" headingLevel={3}>
        <Details
          label="Agreement"
          items={[
            ["Customer", a.customerPartyId, true],
            ["Legal entity", a.legalEntityId, true],
            ["Valid from", a.validFrom, true],
            ["Valid to", a.validTo ?? dash, true],
            ["SLA profile", a.slaProfileId, true],
            ["Billing model", sentence(a.billingModel)],
            ["Priority rank", a.priorityRank],
            ["Included hours used", a.hoursConsumed],
            ["Visits used", a.visitsConsumed],
            ["Status reason", a.statusReason ?? dash],
          ]}
        />
      </Card>
      <Card title="Lines" headingLevel={3}>
        <DataTable
          label="Agreement lines"
          columns={lineColumns}
          rows={a.lines.map((l, i) => ({ ...l, id: String(i) }))}
          getRowKey={(row) => row.id}
          empty={{ title: "No lines" }}
        />
      </Card>
      <AgreementVisitsPanel agreement={a} onRecorded={() => {
          setLatest(null);
          state.reload();
        }} />
    </div>
  );
}
