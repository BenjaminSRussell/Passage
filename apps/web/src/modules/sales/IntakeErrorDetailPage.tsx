import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, buttonClassName, Card, FormField, PageHeader, Skeleton, Textarea } from "../../shell/ui";
import { describeProblem, getIntakeError, replayIntakeError, resolveIntakeError, type IntakeReplayResult, type Problem } from "./api/client";
import { formatDateTime } from "./format";
import { IntakeErrorStatusBadge } from "./IntakeErrorsPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Closing an intake error needs this duty (SalesOrderMaintain); the service enforces it. */
export const DUTY_ORDER_WRITE = "sales.order.write";
const NOTE_MAX = 500;

/** One intake error and, while it is open, the form that closes it with a note. */
export function IntakeErrorDetailPage() {
  const { id = "" } = useParams();
  const canResolve = holdsDuty(usePrincipalDuties(), DUTY_ORDER_WRITE);
  const state = useLoad((signal) => getIntakeError(id, signal), id);
  const [note, setNote] = useState("");
  const [fieldError, setFieldError] = useState("");
  const [problem, setProblem] = useState<Problem | null>(null);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  const [replaying, setReplaying] = useState(false);
  const [replay, setReplay] = useState<IntakeReplayResult | null>(null);

  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Intake error" />
        <Alert tone="danger" title="Could not load the intake error" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Intake error" />
        <span className="visually-hidden">Loading the intake error…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: e, etag } = state.data;

  async function resolve(event: FormEvent) {
    event.preventDefault();
    const text = note.trim();
    if (text.length === 0 || text.length > NOTE_MAX) {
      setFieldError(`Give a note of 1 to ${NOTE_MAX} characters.`);
      return;
    }
    setFieldError("");
    setProblem(null);
    setBusy(true);
    try {
      await resolveIntakeError(e.id, etag, text);
      setDone(true);
      setNote("");
      state.reload();
    } catch (failure) {
      setProblem(describeProblem(failure));
    } finally {
      setBusy(false);
    }
  }

  async function replayOrder() {
    setProblem(null);
    setReplay(null);
    setReplaying(true);
    try {
      setReplay(await replayIntakeError(e.id));
      state.reload();
    } catch (failure) {
      setProblem(describeProblem(failure));
    } finally {
      setReplaying(false);
    }
  }

  const facts: [string, string][] = [
    ["Channel", e.channel],
    ["External reference", e.externalRef ?? "—"],
    ["Error", e.errorCode],
    ["What was wrong", e.message],
    ["Attempts", String(e.attempts)],
    ["First seen", formatDateTime(e.firstSeenAt)],
    ["Last seen", formatDateTime(e.lastSeenAt)],
    ["Replays tried", String(e.replayCount)],
    ["Payload hash", e.requestHash],
    ...(e.resolvedAt ? ([["Closed", `${formatDateTime(e.resolvedAt)} by ${e.resolvedBy ?? "—"}`]] as [string, string][]) : []),
    ...(e.resolutionNote ? ([["Note", e.resolutionNote]] as [string, string][]) : []),
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Intake error from ${e.channel}`}
        meta={<IntakeErrorStatusBadge status={e.status} />}
        actions={
          <Link className={buttonClassName("ghost")} to="/sales/intake-errors">
            Back to intake errors
          </Link>
        }
      />
      {problem ? (
        <Alert
          tone="danger"
          title={
            problem.code === "SLS-0006"
              ? "The intake error was changed by someone else"
              : problem.code === "SLS-0116"
                ? "Already closed"
                : problem.code === "SLS-0118"
                  ? "This error cannot be replayed"
                  : "The request could not be completed"
          }
          live="alert"
          action={problem.code === "SLS-0006" || problem.code === "SLS-0116" ? <Button size="sm" onClick={() => { setProblem(null); state.reload(); }}>Reload</Button> : undefined}
        >
          {problem.message} ({problem.code})
        </Alert>
      ) : null}
      <Alert tone="success" live="status">
        {done ? "Closed. A retry of the same payload would open it again." : replay?.replayed ? `Replayed as order ${replay.orderNumber ?? ""}. The error is closed.` : null}
      </Alert>
      {replay && !replay.replayed && replay.failure ? (
        <Alert tone="warning" title="Intake refused the order again" live="alert">
          {replay.failure.message} ({replay.failure.code})
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
      {e.replayedOrderId ? (
        <p>
          <Link to={`/sales/orders/${e.replayedOrderId}`}>Open the order this error was replayed as</Link>
        </p>
      ) : null}
      {e.status === "OPEN" && e.replayable ? (
        <Card title="Replay the order">
          {canResolve ? (
            <>
              <p className={styles.muted}>Send the refused order through intake again, once the cause is fixed. If it is stored, this error is closed with the order number.</p>
              <div className={styles.formActions}>
                <Button variant="primary" loading={replaying} onClick={() => void replayOrder()}>
                  Replay order
                </Button>
              </div>
            </>
          ) : (
            <p className={styles.muted}>Replaying an order needs the {DUTY_ORDER_WRITE} duty.</p>
          )}
        </Card>
      ) : null}
      {e.status === "OPEN" && !e.replayable ? (
        <p className={styles.muted}>The refused request was not kept for this error, so it cannot be replayed; ask the sender to send it again.</p>
      ) : null}
      {e.status === "OPEN" ? (
        <Card title="Close this error">
          {canResolve ? (
            <form className={styles.form} onSubmit={(ev) => void resolve(ev)} noValidate aria-label="Close this error">
              <FormField id="resolution-note" label="Note" hint="What was fixed, or why the sender will not retry." error={fieldError || undefined}>
                {(control) => <Textarea {...control} rows={2} maxLength={NOTE_MAX} value={note} onChange={(ev) => setNote(ev.target.value)} invalid={Boolean(fieldError)} />}
              </FormField>
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy}>
                  Close error
                </Button>
              </div>
            </form>
          ) : (
            <p className={styles.muted}>Closing an intake error needs the {DUTY_ORDER_WRITE} duty.</p>
          )}
        </Card>
      ) : null}
    </div>
  );
}
