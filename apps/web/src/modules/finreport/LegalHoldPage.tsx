import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { describeProblem, getLegalHold, releaseLegalHold, type LegalHold, type Problem } from "./api/client";
import { MAX_HOLD_REASON } from "./LegalHoldsPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** One legal hold (FR-15.8): who placed it and why, and the release that ends it. */
export function LegalHoldPage() {
  const { id = "" } = useParams();
  const canHold = holdsDuty(usePrincipalDuties(), "reporting.library.hold");
  const state = useLoad((signal) => getLegalHold(id, signal), id);
  const [released, setReleased] = useState<LegalHold | null>(null);
  const [note, setNote] = useState("");
  const [noteError, setNoteError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);
  const title = "Legal hold";

  if (state.error) {
    const missing = state.error.status === 404;
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <Alert
          tone="danger"
          title={missing ? "This legal hold does not exist" : "This legal hold could not be loaded"}
          live="alert"
          action={missing ? undefined : <Button size="sm" onClick={state.reload}>Try again</Button>}
        >
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/finreport/legal-holds">Back to the legal holds</Link>
      </div>
    );
  }
  const hold = released ?? state.data;
  if (!hold) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={title} />
        <p className={styles.note}>Loading legal hold…</p>
        <Skeleton width="full" height="tile" />
      </div>
    );
  }

  async function release(event: FormEvent) {
    event.preventDefault();
    if (!hold || submitting.current) return;
    if (note.trim().length > MAX_HOLD_REASON) {
      setNoteError(`Keep the note within ${MAX_HOLD_REASON} characters.`);
      return;
    }
    setNoteError(undefined);
    setFailure(null);
    submitting.current = true;
    setBusy(true);
    try {
      setReleased(await releaseLegalHold(hold.id, hold.revision, note.trim()));
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  const rows: [string, string][] = [
    ["Legal entity", hold.legalEntityId],
    ["Reason", hold.reason],
    ["Placed", `${hold.placedAt} by ${hold.placedBy}`],
  ];
  if (!hold.active) {
    rows.push(["Released", `${hold.releasedAt ?? ""} by ${hold.releasedBy ?? ""}`]);
    if (hold.releaseNote) rows.push(["Release note", hold.releaseNote]);
  }
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Legal hold"
        description={hold.active ? "The report library of this legal entity is kept in full." : "This hold was released; the retention sweep deletes past-retention files again."}
        actions={<Badge tone={hold.active ? "warning" : "neutral"}>{hold.active ? "In force" : "Released"}</Badge>}
      />
      <Card title="Details">
        <dl className={styles.details}>
          {rows.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      {hold.active && canHold ? (
        <Card title="Release the hold">
          <form className={styles.form} onSubmit={release} noValidate aria-label="Release the hold">
            <FormField id="fr-release-note" label="Release note" optional error={noteError} hint="Why the hold ends. Kept in the audit trail.">
              {(c) => <Input {...c} value={note} invalid={Boolean(noteError)} onChange={(e) => setNote(e.target.value)} />}
            </FormField>
            {failure ? (
              <Alert tone="danger" title="The hold could not be released" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="danger" disabled={busy}>
                {busy ? "Releasing…" : "Release hold"}
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <Link to="/finreport/legal-holds">Back to the legal holds</Link>
    </div>
  );
}
