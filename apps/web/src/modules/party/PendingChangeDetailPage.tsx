import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, ConfirmDialog, FormField, PageHeader, Skeleton, StatusPill, Textarea } from "../../shell/ui";
import { decidePendingChange, describeProblem, getPendingChange, type PendingChange, type Problem } from "./api/client";
import { displayOrDash, DUTY_APPROVE, formatDateTime, pendingFieldLabel } from "./format";
import styles from "./pages.module.css";
import { Entry, problemText, ProblemAlert } from "./parts";
import { useLoad } from "./useLoad";

const REASON_MAX = 500;

type Outcome = { tone: "success"; text: string } | { tone: "danger"; problem: Problem };

function facts(change: PendingChange): [string, string][] {
  return [
    ["Field", pendingFieldLabel[change.field] ?? change.field],
    ["Current value", displayOrDash(change.currentValue)],
    ["Requested value", change.requestedValue],
    ["Customer", change.customerId],
    ["Legal entity", change.legalEntityId],
    ["Requested by", `${change.requestedBy} at ${formatDateTime(change.requestedAt)}`],
    ...(change.decidedAt ? ([["Decided", `${change.decidedBy ?? "—"} at ${formatDateTime(change.decidedAt)}`]] as [string, string][]) : []),
    ...(change.reason ? ([["Reason", change.reason]] as [string, string][]) : []),
  ];
}

/** One credit-limit pending change: view, then approve or reject with party.approve. */
export function PendingChangeDetailPage() {
  const { id = "" } = useParams();
  // Keyed on the id so an outcome, reason or open dialog never carries over to another change.
  return <PendingChangeDetail key={id} id={id} />;
}

function PendingChangeDetail({ id }: { id: string }) {
  const canDecide = holdsDuty(usePrincipalDuties(), DUTY_APPROVE);
  const state = useLoad((signal) => getPendingChange(id, signal), id);
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<"approve" | "reject" | null>(null);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [confirming, setConfirming] = useState<"approve" | "reject" | null>(null);
  const outcomeRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (outcome) outcomeRef.current?.focus();
  }, [outcome]);

  async function decide(action: "approve" | "reject") {
    const text = reason.trim();
    if (text.length > REASON_MAX) {
      setError(`Use at most ${REASON_MAX} characters.`);
      return;
    }
    setError("");
    setOutcome(null);
    setBusy(action);
    try {
      const change = state.data;
      if (!change) return;
      await decidePendingChange(change.id, action, text);
      setOutcome({
        tone: "success",
        text:
          action === "approve"
            ? "Approved. The stored credit limit is now the requested value."
            : "Rejected. The stored credit limit is unchanged.",
      });
      setReason("");
      state.reload();
    } catch (failure) {
      setOutcome({ tone: "danger", problem: describeProblem(failure) });
    } finally {
      setBusy(null);
    }
  }

  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader
          title="Credit limit change"
          actions={
            <Link className={buttonClassName("ghost")} to="/party/pending-changes">
              Back to credit limit approvals
            </Link>
          }
        />
        <ProblemAlert problem={state.error} title="Could not load the change" retry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Credit limit change" />
        <span className="visually-hidden">Loading the credit limit change…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  const change = state.data;
  const pending = change.status === "pending";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Credit limit change"
        meta={<StatusPill status={change.status} />}
        actions={
          <Link className={buttonClassName("ghost")} to="/party/pending-changes">
            Back to credit limit approvals
          </Link>
        }
      />
      <div role="status" ref={outcomeRef} tabIndex={-1}>
        {outcome?.tone === "danger" ? (
          <Alert tone="danger" title="The decision failed">
            {problemText(outcome.problem)}
          </Alert>
        ) : null}
        {outcome?.tone === "success" ? <Alert tone="success">{outcome.text}</Alert> : null}
      </div>
      <Card>
        <dl className={styles.details} aria-label="Credit limit change">
          {facts(change).map(([term, value]) => (
            <Entry key={term} term={term} mono={term === "Customer" || term === "Legal entity"}>
              {value}
            </Entry>
          ))}
        </dl>
      </Card>
      {pending ? (
        <Card title="Decision">
          {canDecide ? (
            <form className={styles.form} onSubmit={(event) => event.preventDefault()} noValidate aria-label="Decision">
              <FormField
                id="decision-reason"
                label="Reason"
                optional
                hint={`Optional. At most ${REASON_MAX} characters.`}
                error={error || undefined}
              >
                {(control) => (
                  <Textarea
                    {...control}
                    rows={2}
                    maxLength={REASON_MAX}
                    value={reason}
                    onChange={(event) => setReason(event.target.value)}
                    invalid={Boolean(error)}
                  />
                )}
              </FormField>
              <div className={styles.formActions}>
                <Button
                  variant="primary"
                  type="button"
                  loading={busy === "approve"}
                  disabled={busy !== null && busy !== "approve"}
                  onClick={() => setConfirming("approve")}
                >
                  Approve
                </Button>
                <Button
                  type="button"
                  variant="danger"
                  loading={busy === "reject"}
                  disabled={busy !== null && busy !== "reject"}
                  onClick={() => setConfirming("reject")}
                >
                  Reject
                </Button>
              </div>
            </form>
          ) : (
            <p className={styles.muted}>Deciding a credit-limit change needs the party.approve duty.</p>
          )}
        </Card>
      ) : (
        <p className={styles.note}>This change is already {change.status}. The stored limit is not waiting on a decision.</p>
      )}
      {confirming ? (
        <ConfirmDialog
          title={confirming === "approve" ? "Approve this credit limit increase?" : "Reject this credit limit increase?"}
          description={
            confirming === "approve"
              ? `The stored credit limit becomes ${change.requestedValue}.`
              : "The stored credit limit stays unchanged and the request is closed."
          }
          confirmLabel={confirming === "approve" ? "Confirm approval" : "Confirm rejection"}
          cancelLabel="Cancel"
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            const action = confirming;
            setConfirming(null);
            void decide(action);
          }}
        />
      ) : null}
    </div>
  );
}
