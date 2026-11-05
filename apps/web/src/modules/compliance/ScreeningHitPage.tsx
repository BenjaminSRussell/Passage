import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, FormField, PageHeader, Select, Skeleton, Textarea } from "../../shell/ui";
import { decideScreeningHit, describeProblem, getScreeningHit, newKey, type HitDecision, type Problem, type ScreeningHit, type Versioned } from "./api/client";
import { dash, formatDateTime, hitStateTone, scoreText, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const decisionLabel: Record<HitDecision, string> = {
  CLEAR_FALSE_POSITIVE: "Clear as false positive",
  CONFIRM: "Confirm the match",
};

/** Checks that mirror the service (api.decideHit): a decision and a reason. */
export function validateDecision(decision: HitDecision | "", reason: string): { decision?: string; reason?: string } {
  const errors: { decision?: string; reason?: string } = {};
  if (!decision) errors.decision = "Choose clear or confirm.";
  if (!reason.trim()) errors.reason = "Record why: the decision is kept for five years.";
  else if (reason.length > 2000) errors.reason = "Keep the reason under 2,000 characters.";
  return errors;
}

/**
 * One screening hit (PRD §9.48.6 Screening workbench): the subject next to the
 * list entry it matched, and the clear or confirm decision for a
 * compliance.trade-screening.decide. A false positive is not raised again for this list
 * entry until the subject's name, address or country or the entry changes.
 */
export function ScreeningHitPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getScreeningHit(id, signal), id);
  const [latest, setLatest] = useState<Versioned<ScreeningHit> | null>(null);
  const [decision, setDecision] = useState<HitDecision | "">("");
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<{ decision?: string; reason?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  const current = latest && state.data && latest.value.hitId === state.data.value.hitId && latest.value.version >= state.data.value.version ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Screening hit" />
        <Alert tone="danger" title="Could not load the hit" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/compliance/screening/hits">Back to screening hits</Link>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Screening hit" />
        <span className="visually-hidden">Loading the hit…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const hit = current.value;
  const etag = current.etag;

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateDecision(decision, reason);
    setErrors(found);
    if (found.decision || found.reason || !decision) return;
    setFailure(null);
    setDone(null);
    setBusy(true);
    try {
      setLatest(await decideScreeningHit(hit.hitId, etag, decision, reason.trim(), key.current));
      setDone(decision === "CONFIRM" ? "Match confirmed. The subject stays blocked." : "Cleared as a false positive. The subject is released for this list entry.");
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      if (problem.code === "SOD-1110") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const compare: [string, ReactNode, ReactNode][] = [
    ["Name", hit.subjectName, hit.listEntryName || dash],
    ["Identifier", <span className={styles.mono}>{hit.subjectId}</span>, <span className={styles.mono}>{hit.listEntryId}</span>],
    ["Kind", sentence(hit.subjectType), <span className={styles.mono}>{`${hit.listCode} v${hit.listEntryVersion}`}</span>],
  ];
  const facts: [string, ReactNode][] = [
    ["Score", <span className={styles.mono}>{scoreText(hit.score)}</span>],
    ["Matched on", hit.matchedFields.join(", ") || dash],
    ["State", <Badge tone={hitStateTone[hit.state]}>{sentence(hit.state)}</Badge>],
    ["Detected", formatDateTime(hit.detectedAt)],
    ["Decided by", hit.decidedBy ? <span className={styles.mono}>{hit.decidedBy}</span> : dash],
    ["Decided", formatDateTime(hit.decidedAt)],
    ["Reason", hit.reason ?? dash],
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Hit on ${hit.subjectName}`}
        description={`${hit.listCode} entry ${hit.listEntryId} at score ${scoreText(hit.score)}.`}
        meta={<Badge tone={hitStateTone[hit.state]}>{sentence(hit.state)}</Badge>}
        actions={<Link to="/compliance/screening/hits">Back to screening hits</Link>}
      />
      <Card title="Compare">
        <dl className={styles.details}>
          {compare.map(([term, subject, entry]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>
                <span className="visually-hidden">Subject: </span>
                {subject}
              </dd>
              <dd className={`${styles.value} ${styles.muted}`}>
                <span className="visually-hidden">List entry: </span>
                {entry}
              </dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Hit">
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Alert tone="danger" title={failure ? "Could not record the decision" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {hit.state === "OPEN" ? (
        <Card title="Decide">
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Decide screening hit">
            <p className={`${styles.note} ${styles.wide}`}>
              You decide as yourself and need the trade compliance officer duty. Until the hit is decided the subject cannot be released, shipped, sent or paid.
            </p>
            <FormField id="hit-decision" label="Decision" error={errors.decision}>
              {(control) => (
                <Select {...control} value={decision} onChange={(e) => {
                    setDecision(e.target.value as HitDecision | "");
                    key.current = newKey();
                  }}
                  invalid={Boolean(errors.decision)}>
                  <option value="">Choose…</option>
                  {(Object.keys(decisionLabel) as HitDecision[]).map((d) => (
                    <option key={d} value={d}>
                      {decisionLabel[d]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <div className={styles.wide}>
              <FormField id="hit-reason" label="Reason" error={errors.reason}>
                {(control) => (
                  <Textarea
                    {...control}
                    rows={3}
                    value={reason}
                    onChange={(e) => {
                      setReason(e.target.value);
                      key.current = newKey();
                    }}
                    invalid={Boolean(errors.reason)}
                  />
                )}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Record decision
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}
