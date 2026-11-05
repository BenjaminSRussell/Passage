import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { describeProblem, intakeSubjectRequest, newKey, PRIVACY_TYPES, REGULATIONS, type IntakeInput, type PrivacyRequestType, type Problem, type Regulation } from "./api/client";
import { sentence } from "./format";
import styles from "./pages.module.css";

export interface IntakeValues {
  type: PrivacyRequestType;
  regulation: Regulation;
  subjectRef: string;
  receivedOn: string;
  deadlineDays: string;
}

type Errors = Partial<Record<"subjectRef" | "receivedOn" | "deadlineDays", string>>;

const maxDays: Record<Regulation, number> = { GDPR: 30, CCPA: 45 };

/** Checks the form and builds the request; mirrors the service (api.intakeRequest). */
export function toIntakeInput(v: IntakeValues, now: Date = new Date()): { input?: IntakeInput; errors: Errors } {
  const errors: Errors = {};
  const subject = v.subjectRef.trim();
  if (!subject) errors.subjectRef = "Enter the party, worker, user or contact, for example party:42.";
  else if (subject.length > 200) errors.subjectRef = "Keep the reference under 200 characters.";
  let receivedAt: string | undefined;
  if (v.receivedOn) {
    const day = new Date(`${v.receivedOn}T00:00:00Z`);
    if (Number.isNaN(day.getTime())) errors.receivedOn = "Enter the date the request arrived.";
    else if (day.getTime() > now.getTime()) errors.receivedOn = "The request cannot arrive in the future.";
    else receivedAt = day.toISOString();
  }
  let deadlineDays: number | undefined;
  if (v.deadlineDays.trim()) {
    deadlineDays = Number(v.deadlineDays.trim());
    if (!Number.isInteger(deadlineDays) || deadlineDays < 1 || deadlineDays > maxDays[v.regulation]) {
      errors.deadlineDays = `Enter whole days from 1 to ${maxDays[v.regulation]}; ${v.regulation} allows no longer.`;
    }
  }
  if (Object.keys(errors).length > 0) return { errors };
  const input: IntakeInput = { type: v.type, regulation: v.regulation, subjectRef: subject };
  if (receivedAt) input.receivedAt = receivedAt;
  if (deadlineDays !== undefined) input.deadlineDays = deadlineDays;
  return { input, errors };
}

/**
 * Record a subject request (PRD §9.48.5 intake, duty compliance.privacy-request.create). The
 * deadline is set from the regulation; the request then waits for a privacy
 * officer to verify the requester's identity.
 */
export function PrivacyRequestNewPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<IntakeValues>({ type: "ACCESS", regulation: "GDPR", subjectRef: "", receivedOn: "", deadlineDays: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function change(next: IntakeValues) {
    setValues(next);
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { input, errors: found } = toIntakeInput(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      const created = await intakeSubjectRequest(input, key.current);
      navigate(`/compliance/privacy/requests/${created.value.requestId}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Record a privacy request"
        description="Enter a GDPR or CCPA request as it arrived. The deadline runs from the day it was received: 30 days under GDPR, 45 under CCPA."
        actions={<Link to="/compliance/privacy/requests">Back to privacy requests</Link>}
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Record a privacy request">
          <FormField id="intake-type" label="Request type">
            {(control) => (
              <Select {...control} value={values.type} onChange={(e) => change({ ...values, type: e.target.value as PrivacyRequestType })}>
                {PRIVACY_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="intake-regulation" label="Regulation">
            {(control) => (
              <Select {...control} value={values.regulation} onChange={(e) => change({ ...values, regulation: e.target.value as Regulation })}>
                {REGULATIONS.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="intake-subject" label="Subject" hint="The party, worker, user or contact the request is about." error={errors.subjectRef}>
            {(control) => <Input {...control} className={styles.mono} placeholder="party:42" value={values.subjectRef} onChange={(e) => change({ ...values, subjectRef: e.target.value })} invalid={Boolean(errors.subjectRef)} />}
          </FormField>
          <FormField id="intake-received" label="Received on" hint="Today when left empty." error={errors.receivedOn} optional>
            {(control) => <Input {...control} type="date" value={values.receivedOn} onChange={(e) => change({ ...values, receivedOn: e.target.value })} invalid={Boolean(errors.receivedOn)} />}
          </FormField>
          <FormField id="intake-days" label="Respond within (days)" hint="Only to shorten the regulation's window." error={errors.deadlineDays} optional>
            {(control) => <Input {...control} inputMode="numeric" value={values.deadlineDays} onChange={(e) => change({ ...values, deadlineDays: e.target.value })} invalid={Boolean(errors.deadlineDays)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record request
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "The request was not recorded" : undefined} live="alert">
        {failure ? `${failure.message} (${failure.code})` : null}
      </Alert>
    </div>
  );
}
