import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Button, Card, FormField, Input } from "../../shell/ui";
import { describeProblem, type ChargePreviewRequest, type JournalPreviewRequest, type Problem } from "./api/client";
import { newKey, postCharge, postJournal, type ChargeRecord, type EntityTarget, type PostedJournal } from "./api/postings";
import { isDate } from "./format";
import styles from "./pages.module.css";

export interface TargetValues {
  legalEntityId: string;
  ledgerId: string;
}

type Targets = Record<string, TargetValues>;

/** Where each entity code posts: its legal entity and the ledger of that legal entity. */
function PostingTargets({ entities, targets, errors, onChange }: { entities: string[]; targets: Targets; errors: Record<string, string>; onChange(t: Targets): void }) {
  const set = (entity: string, patch: Partial<TargetValues>) => onChange({ ...targets, [entity]: { ...(targets[entity] ?? { legalEntityId: "", ledgerId: "" }), ...patch } });
  return (
    <>
      {entities.map((entity, i) => {
        const t = targets[entity] ?? { legalEntityId: "", ledgerId: "" };
        return (
          <fieldset key={entity} className={styles.lineCard}>
            <legend className={styles.lineLegend}>Entity {entity} posts to</legend>
            <FormField id={`post-le-${i}`} label="Legal entity" error={errors[`entities[${i}].legalEntityId`]}>
              {(c) => <EntityLookup entity="legalEntity" {...c} prefill={false} value={t.legalEntityId} onChange={(e) => set(entity, { legalEntityId: e.target.value })} invalid={Boolean(errors[`entities[${i}].legalEntityId`])} />}
            </FormField>
            <FormField id={`post-ledger-${i}`} label="Ledger code" error={errors[`entities[${i}].ledgerId`]}>
              {(c) => <Input {...c} className={styles.mono} value={t.ledgerId} onChange={(e) => set(entity, { ledgerId: e.target.value })} invalid={Boolean(errors[`entities[${i}].ledgerId`])} />}
            </FormField>
          </fieldset>
        );
      })}
    </>
  );
}

/** Checks the posting date and every entity's target; returns the targets the service takes. */
export function checkTargets(entities: string[], targets: Targets, postingDate: string, errors: Record<string, string>): EntityTarget[] {
  if (!isDate(postingDate)) errors.postingDate = "Choose the posting date.";
  return entities.map((entity, i) => {
    const t = targets[entity] ?? { legalEntityId: "", ledgerId: "" };
    if (t.legalEntityId === "") errors[`entities[${i}].legalEntityId`] = "Choose the legal entity.";
    if (t.ledgerId.trim() === "") errors[`entities[${i}].ledgerId`] = "Enter the ledger code.";
    return { entity, legalEntityId: t.legalEntityId, ledgerId: t.ledgerId.trim() };
  });
}

/** Post a previewed IC journal to both entities' ledgers (FR-40.5). */
export function PostJournalPanel({ request }: { request: JournalPreviewRequest }) {
  const entities = [request.entity, request.offsetEntity];
  const [date, setDate] = useState("");
  const [targets, setTargets] = useState<Targets>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [posted, setPosted] = useState<PostedJournal | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    const list = checkTargets(entities, targets, date, found);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setPosted(await postJournal(request, date, list, key.current));
      key.current = newKey();
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Post to the ledgers" headingLevel={3}>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
        <p className={styles.note}>Each entity's legs become one entry in its own ledger. If a later entity's ledger refuses its side, the sides already posted are reversed. After a connection problem, post again to continue where it stopped.</p>
        <FormField id="post-date" label="Posting date" error={errors.postingDate}>
          {(c) => <Input {...c} type="date" value={date} onChange={(e) => setDate(e.target.value)} invalid={Boolean(errors.postingDate)} />}
        </FormField>
        <PostingTargets entities={entities} targets={targets} errors={errors} onChange={setTargets} />
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The journal was not posted" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        {posted ? (
          <div className={styles.formActions}>
            <Alert tone="success" title="Journal posted" live="status">
              Both ledgers took their side. <Link to={`/intercompany/journals/${posted.id}`}>Open the posted journal</Link>.
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Post journal
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** Post a priced IC charge to both entities (FR-40.13). Without a transfer price it stays on suspense. */
export function PostChargePanel({ request }: { request: ChargePreviewRequest }) {
  const entities = [request.borrowingEntity, request.lendingEntity];
  const [sourceId, setSourceId] = useState("");
  const [version, setVersion] = useState("1");
  const [date, setDate] = useState("");
  const [targets, setTargets] = useState<Targets>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<ChargeRecord | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    const list = checkTargets(entities, targets, date, found);
    if (sourceId.trim() === "") found.sourceId = "Enter the expense, timesheet or invoice line the charge comes from.";
    if (!/^[1-9]\d{0,5}$/.test(version.trim())) found.version = "Enter a whole number from 1.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await postCharge(request, { sourceId: sourceId.trim(), version: Number(version), postingDate: date, entities: list }));
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Post the charge" headingLevel={3}>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
        <p className={styles.note}>A charge is stored per source and version, so submitting the same source again does not post twice. A charge without an applicable transfer price stays on suspense; add the price and submit the next version to post it.</p>
        <FormField id="post-source" label="Source" hint="For example the timesheet or expense line." error={errors.sourceId}>
          {(c) => <Input {...c} className={styles.mono} value={sourceId} onChange={(e) => setSourceId(e.target.value)} invalid={Boolean(errors.sourceId)} />}
        </FormField>
        <FormField id="post-version" label="Version" error={errors.version}>
          {(c) => <Input {...c} inputMode="numeric" value={version} onChange={(e) => setVersion(e.target.value)} invalid={Boolean(errors.version)} />}
        </FormField>
        <FormField id="post-date" label="Posting date" error={errors.postingDate}>
          {(c) => <Input {...c} type="date" value={date} onChange={(e) => setDate(e.target.value)} invalid={Boolean(errors.postingDate)} />}
        </FormField>
        <PostingTargets entities={entities} targets={targets} errors={errors} onChange={setTargets} />
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The charge was not posted" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        {result ? (
          <div className={styles.formActions}>
            {result.status === "suspense" ? (
              <Alert tone="warning" title="Charge on suspense" live="status">
                No transfer price applies to this charge, so nothing was posted. Add the price and submit the next version. <Link to="/intercompany/charge-log">Open the charge log</Link>.
              </Alert>
            ) : (
              <Alert tone="success" title="Charge posted" live="status">
                Both entities booked it. <Link to="/intercompany/charge-log">Open the charge log</Link>.
              </Alert>
            )}
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Post charge
          </Button>
        </div>
      </form>
    </Card>
  );
}
