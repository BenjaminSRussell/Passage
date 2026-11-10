import { useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, FormField, Input, Select, Textarea } from "../../shell/ui";
import {
  approveNonconformance,
  checkEffectiveness,
  completeCorrectionStep,
  describeProblem,
  newKey,
  refuseNonconformance,
  startCorrection,
  type Correction,
  type EffectivenessResult,
  type Nonconformance,
  type Problem,
} from "./api/client";
import { CORRECTION_STATUS_LABELS, formatDate, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

interface ChangeProps {
  ncr: Nonconformance;
  onChanged(next: Nonconformance): void;
}

/** Runs one change and keeps its failure; the caller shows the refreshed NCR. */
function useChange(onChanged: (next: Nonconformance) => void) {
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  async function run(name: string, call: () => Promise<Nonconformance>): Promise<boolean> {
    setFailure(null);
    setBusy(name);
    try {
      onChanged(await call());
      return true;
    } catch (error) {
      setFailure(describeProblem(error));
      return false;
    } finally {
      setBusy(null);
    }
  }
  return { failure, busy, run };
}

function Failure({ failure, title }: { failure: Problem | null; title: string }) {
  if (!failure) return null;
  const stale = failure.code === "conflict_version";
  return (
    <div className={styles.formActions}>
      <Alert tone="danger" title={stale ? "Someone else changed this nonconformance" : title} live="alert">
        {stale ? "Reload the page to see the latest version, then try again." : <ProblemMessage problem={failure} />}
      </Alert>
    </div>
  );
}

/** Review of a NEW report (approve or refuse with a reason), and starting the 8D of an approved one. */
export function NcrActions({ ncr, onChanged }: ChangeProps) {
  const { failure, busy, run } = useChange(onChanged);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string>();
  const [days, setDays] = useState("90");
  const [daysError, setDaysError] = useState<string>();
  const key = useRef(newKey());

  if (ncr.status === "NEW") {
    const refuse = async (event: FormEvent) => {
      event.preventDefault();
      const r = reason.trim();
      if (r === "" || r.length > 500) {
        setReasonError("Enter why the report is refused, up to 500 characters.");
        return;
      }
      setReasonError(undefined);
      await run("refuse", () => refuseNonconformance(ncr.id, ncr.version, r));
    };
    return (
      <Card title="Review">
        <form className={styles.form} onSubmit={refuse} noValidate aria-label="Review the nonconformance">
          <div className={styles.wide}>
            <FormField id="ncr-refuse-reason" label="Refusal reason" hint="Needed only to refuse." error={reasonError}>
              {(control) => <Textarea {...control} rows={2} maxLength={500} value={reason} invalid={Boolean(reasonError)} onChange={(event) => setReason(event.target.value)} />}
            </FormField>
          </div>
          <Failure failure={failure} title="Could not review the nonconformance" />
          <div className={styles.formActions}>
            <Button variant="primary" loading={busy === "approve"} onClick={() => run("approve", () => approveNonconformance(ncr.id, ncr.version))}>
              Approve
            </Button>
            <Button type="submit" variant="danger" loading={busy === "refuse"}>
              Refuse
            </Button>
          </div>
        </form>
      </Card>
    );
  }

  if (ncr.status === "APPROVED") {
    const start = async (event: FormEvent) => {
      event.preventDefault();
      const n = Number(days);
      if (!/^\d{2,3}$/.test(days.trim()) || n < 30 || n > 365) {
        setDaysError("Enter 30 to 365 days.");
        return;
      }
      setDaysError(undefined);
      await run("start", () => startCorrection(ncr.id, ncr.version, n, key.current));
    };
    return (
      <Card title="Correction">
        <form className={styles.form} onSubmit={start} noValidate aria-label="Start the 8D correction">
          <p className={`${styles.note} ${styles.wide}`}>
            The 8D opens at D1. Containment (D3) is due two business days after the start and the corrective action (D5) 30 days after it.
          </p>
          <FormField id="ncr-effectiveness-days" label="Effectiveness check after (days)" hint="effectiveness_check_days, default 90." error={daysError}>
            {(control) => (
              <Input
                {...control}
                inputMode="numeric"
                value={days}
                invalid={Boolean(daysError)}
                onChange={(event) => {
                  setDays(event.target.value);
                  key.current = newKey();
                }}
              />
            )}
          </FormField>
          <Failure failure={failure} title="Could not start the correction" />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "start"}>
              Start 8D
            </Button>
          </div>
        </form>
      </Card>
    );
  }
  return null;
}

/** The 8D steps with their due dates, the current step's form and the effectiveness check (FR-33.8, BR-33.9). */
export function CorrectionPanel({ ncr, correction, onChanged }: ChangeProps & { correction: Correction }) {
  const { failure, busy, run } = useChange(onChanged);
  const [notes, setNotes] = useState("");
  const [notesError, setNotesError] = useState<string>();
  const [result, setResult] = useState<EffectivenessResult>("EFFECTIVE");
  const [note, setNote] = useState("");
  const [noteError, setNoteError] = useState<string>();
  const overdue = new Set(correction.overdueSteps);
  const status = CORRECTION_STATUS_LABELS[correction.status] ?? { label: correction.status, tone: "neutral" as const };
  const current = correction.steps.find((s) => s.step === correction.currentStep);

  async function complete(event: FormEvent) {
    event.preventDefault();
    const n = notes.trim();
    if (n === "" || n.length > 2000) {
      setNotesError("Record what was done in this step, up to 2000 characters.");
      return;
    }
    setNotesError(undefined);
    if (await run("step", () => completeCorrectionStep(ncr.id, ncr.version, correction.currentStep, n))) setNotes("");
  }

  async function check(event: FormEvent) {
    event.preventDefault();
    const n = note.trim();
    if (n === "" || n.length > 2000) {
      setNoteError("Record the evidence for the result, up to 2000 characters.");
      return;
    }
    setNoteError(undefined);
    if (await run("check", () => checkEffectiveness(ncr.id, ncr.version, result, n))) setNote("");
  }

  return (
    <Card title="8D correction" actions={<Badge tone={status.tone}>{status.label}</Badge>}>
      <div className={styles.stack}>
        <p className={styles.note}>
          {`Started by ${correction.startedBy || "—"} on ${formatDateTime(correction.startedAt)}. Effectiveness check ${correction.effectivenessDays} days after D7.`}
          {correction.reopened > 0 ? ` Reopened at D4 ${correction.reopened} ${correction.reopened === 1 ? "time" : "times"} after a not-effective check.` : ""}
        </p>
        {correction.overdueSteps.length > 0 ? (
          <Alert tone="warning" title="Overdue">
            {`${correction.overdueSteps.map((s) => `D${s}`).join(" and ")} ${correction.overdueSteps.length === 1 ? "is" : "are"} past due.`}
          </Alert>
        ) : null}
        <ol className={styles.steps} aria-label="8D steps">
          {correction.steps.map((s) => {
            const isCurrent = correction.status === "OPEN" && s.step === correction.currentStep;
            return (
              <li key={s.step} className={isCurrent ? `${styles.step} ${styles.stepCurrent}` : styles.step} aria-current={isCurrent ? "step" : undefined}>
                <span className={styles.stepHead}>
                  {`D${s.step} ${s.name}`}
                  {s.completedAt ? <Badge tone="success">Done</Badge> : isCurrent ? <Badge tone="info">Current</Badge> : null}
                  {overdue.has(s.step) ? <Badge tone="danger">Overdue</Badge> : null}
                </span>
                {s.dueAt ? <span className={styles.term}>{`Due ${formatDate(s.dueAt)}`}</span> : null}
                {s.completedAt ? <span className={styles.term}>{`${s.completedBy || "—"}, ${formatDateTime(s.completedAt)}`}</span> : null}
                {s.notes ? <span className={styles.value}>{s.notes}</span> : null}
              </li>
            );
          })}
        </ol>
        {correction.status === "OPEN" && current ? (
          <form className={styles.form} onSubmit={complete} noValidate aria-label={`Complete D${current.step}`}>
            <div className={styles.wide}>
              <FormField id="capa-notes" label={`D${current.step} ${current.name}`} error={notesError}>
                {(control) => <Textarea {...control} rows={3} maxLength={2000} value={notes} invalid={Boolean(notesError)} onChange={(event) => setNotes(event.target.value)} />}
              </FormField>
            </div>
            <Failure failure={failure} title="Could not complete the step" />
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "step"}>
                {`Complete D${current.step}`}
              </Button>
            </div>
          </form>
        ) : null}
        {correction.status === "EFFECTIVENESS_PENDING" ? (
          correction.effectivenessCheckDue ? (
            <form className={styles.form} onSubmit={check} noValidate aria-label="Effectiveness check">
              <p className={`${styles.note} ${styles.wide}`}>The check must be made by someone other than the person who completed D7. Not effective sends the 8D back to D4.</p>
              <FormField id="capa-result" label="Result">
                {(control) => (
                  <Select {...control} value={result} onChange={(event) => setResult(event.target.value as EffectivenessResult)}>
                    <option value="EFFECTIVE">Effective</option>
                    <option value="NOT_EFFECTIVE">Not effective</option>
                  </Select>
                )}
              </FormField>
              <div className={styles.wide}>
                <FormField id="capa-note" label="Evidence" error={noteError}>
                  {(control) => <Textarea {...control} rows={3} maxLength={2000} value={note} invalid={Boolean(noteError)} onChange={(event) => setNote(event.target.value)} />}
                </FormField>
              </div>
              <Failure failure={failure} title="Could not record the effectiveness check" />
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy === "check"}>
                  Record effectiveness check
                </Button>
              </div>
            </form>
          ) : (
            <Alert tone="info" title="Effectiveness check scheduled">
              {`The check falls due on ${formatDate(correction.effectivenessDueAt)}.`}
            </Alert>
          )
        ) : null}
        {correction.status === "CLOSED" ? (
          <Alert tone="success" title="Correction closed">
            {`Effective, checked by ${correction.effectivenessBy || "—"} on ${formatDateTime(correction.effectivenessAt)}: ${correction.effectivenessNote ?? ""}`}
          </Alert>
        ) : null}
      </div>
    </Card>
  );
}
