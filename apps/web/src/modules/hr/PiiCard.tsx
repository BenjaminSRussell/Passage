import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input } from "../../shell/ui";
import { describeProblem, REVEAL_REASON_MIN, revealPII, type PIIView, type Problem } from "./api/client";
import { dash } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

/**
 * Masked national ID and bank account with an audited reveal (FR-42.1,
 * PRD 8.42.6): the reveal needs hr.pii.read and a reason of at least ten
 * characters, which the service writes to the audit log.
 */
export function PiiCard({ workerId, masked }: { workerId: string; masked: PIIView }) {
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | undefined>();
  const [revealed, setRevealed] = useState<PIIView | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const shown = revealed ?? masked;

  async function reveal(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (reason.trim().length < REVEAL_REASON_MIN) {
      setReasonError(`Give a reason of at least ${REVEAL_REASON_MIN} characters. It is kept in the audit log.`);
      return;
    }
    setReasonError(undefined);
    setBusy(true);
    try {
      setRevealed(await revealPII(workerId, reason));
      setReason("");
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "HCM-PII-001") setReasonError(problem.message);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card
      title="Personal data"
      actions={
        revealed ? (
          <Button size="sm" variant="ghost" onClick={() => setRevealed(null)}>
            Mask again
          </Button>
        ) : null
      }
    >
      <div className={styles.stack}>
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>National ID</dt>
            <dd className={`${styles.value} ${styles.mono}`}>{shown.nationalId}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Bank account</dt>
            <dd className={`${styles.value} ${styles.mono}`}>{shown.bankAccount ?? dash}</dd>
          </div>
        </dl>
        {revealed ? (
          <Alert tone="warning" title="Unmasked values shown">
            This read and your reason are recorded in the audit log.
          </Alert>
        ) : (
          <form className={styles.toolbar} onSubmit={reveal} noValidate>
            <FormField id={`reveal-${workerId}`} label="Reason for reveal" hint="Required, at least 10 characters. Needs the PII read duty." error={reasonError}>
              {(control) => <Input {...control} value={reason} maxLength={500} invalid={Boolean(reasonError)} onChange={(event) => setReason(event.target.value)} />}
            </FormField>
            <Button type="submit" loading={busy} icon="lock">
              Reveal
            </Button>
          </form>
        )}
        {failure ? (
          <Alert tone="danger" title="Could not reveal personal data" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
      </div>
    </Card>
  );
}
