import { useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, issueCertification, listCertifications, newKey, SKILL_CODE, type Certification, type NewCertification, type Problem } from "./api/client";
import { dash, DATE, todayIso } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export type ExpiryMode = "months" | "date" | "never";

export interface CertificationValues {
  code: string;
  name: string;
  issuer: string;
  issuedOn: string;
  expiry: ExpiryMode;
  validityMonths: string;
  expiresOn: string;
}
type Field = Exclude<keyof CertificationValues, "expiry">;
type Errors = Partial<Record<Field, string>>;
const FIELDS: Field[] = ["code", "name", "issuer", "issuedOn", "validityMonths", "expiresOn"];

/** The certificate rules (BR-42.8) that need no database, and the request the form sends. */
export function validateCertification(values: CertificationValues, today: string = todayIso()): { errors: Errors; input?: NewCertification } {
  const errors: Errors = {};
  const code = values.code.trim().toUpperCase();
  if (!SKILL_CODE.test(code)) errors.code = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const name = values.name.trim();
  if (name === "" || name.length > 120) errors.name = "Enter the certificate's title, up to 120 characters.";
  const issuer = values.issuer.trim();
  if (issuer.length > 120) errors.issuer = "Use at most 120 characters.";
  if (!DATE.test(values.issuedOn)) errors.issuedOn = "Enter the issue date.";
  else if (values.issuedOn > today) errors.issuedOn = "The issue date can't be in the future.";
  const input: NewCertification = { code, name, issuedOn: values.issuedOn };
  if (issuer) input.issuer = issuer;
  if (values.expiry === "months") {
    const months = values.validityMonths.trim();
    if (!/^\d{1,3}$/.test(months) || Number(months) < 1 || Number(months) > 240) errors.validityMonths = "Enter 1 to 240 months.";
    else input.validityMonths = Number(months);
  } else if (values.expiry === "date") {
    if (!DATE.test(values.expiresOn)) errors.expiresOn = "Enter the expiry date.";
    else if (DATE.test(values.issuedOn) && values.expiresOn <= values.issuedOn) errors.expiresOn = "The expiry must be after the issue date.";
    else input.expiresOn = values.expiresOn;
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input };
}

const columns: DataTableColumn<Certification>[] = [
  { key: "code", header: "Certificate", mono: true },
  { key: "name", header: "Title" },
  { key: "issuer", header: "Issuer", render: (row) => row.issuer ?? dash },
  { key: "issuedOn", header: "Issued", mono: true },
  { key: "expiresOn", header: "Expires", mono: true, render: (row) => row.expiresOn ?? "Never" },
  { key: "valid", header: "Status", render: (row) => (row.valid ? <Badge tone="success">Valid</Badge> : <Badge tone="neutral">Not valid</Badge>) },
];

function IssueForm({ workerId, onDone, onCancel }: { workerId: string; onDone(c: Certification): void; onCancel(): void }) {
  const [values, setValues] = useState<CertificationValues>({
    code: "",
    name: "",
    issuer: "",
    issuedOn: todayIso(),
    expiry: "months",
    validityMonths: "24",
    expiresOn: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CertificationValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateCertification(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      onDone(await issueCertification(workerId, input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-CERT-DUPLICATE") fieldErrors.issuedOn = problem.message;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Field, label: string, extra: Record<string, unknown> = {}, hint?: string, optional?: boolean) => (
    <FormField id={`cert-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<CertificationValues>)} />
      )}
    </FormField>
  );

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Record a certificate">
      {text("code", "Code", { maxLength: 20 }, "The certificate type, for example FORKLIFT. Skill search matches on it.")}
      {text("name", "Title", { maxLength: 120 })}
      {text("issuer", "Issuer", { maxLength: 120 }, undefined, true)}
      {text("issuedOn", "Issued on", { type: "date" })}
      <FormField id="cert-expiry" label="Expiry">
        {(control) => (
          <Select {...control} value={values.expiry} onChange={(event) => update({ expiry: event.target.value as ExpiryMode })}>
            <option value="months">Valid for a number of months</option>
            <option value="date">Expires on a date</option>
            <option value="never">Never expires</option>
          </Select>
        )}
      </FormField>
      {values.expiry === "months" ? text("validityMonths", "Valid for (months)", { inputMode: "numeric" }, "Expires on the same day that many months later.") : null}
      {values.expiry === "date" ? text("expiresOn", "Expires on", { type: "date" }, "No longer valid from this day.") : null}
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="Could not record the certificate" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Record certificate
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

/** The worker's certificates with their validity today, and a form to record one (BR-42.8). */
export function CertificationsCard({ workerId }: { workerId: string }) {
  const state = useLoad((signal) => listCertifications(workerId, undefined, signal), `certs|${workerId}`);
  const [adding, setAdding] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  return (
    <Card
      title="Certifications"
      actions={
        adding ? null : (
          <Button size="sm" onClick={() => setAdding(true)}>
            Record certificate
          </Button>
        )
      }
    >
      <div className={styles.stack}>
        <Alert tone="success" live="status">
          {notice}
        </Alert>
        {adding ? (
          <IssueForm
            workerId={workerId}
            onCancel={() => setAdding(false)}
            onDone={(c) => {
              setAdding(false);
              setNotice(`${c.code} recorded${c.expiresOn ? `, valid until the day before ${c.expiresOn}` : ""}.`);
              state.reload();
            }}
          />
        ) : null}
        {state.error ? (
          <Alert tone="danger" title="Could not load certifications" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : state.loading && !state.data ? (
          <Skeleton width="3/4" />
        ) : (
          <DataTable
            label="Certifications"
            columns={columns}
            rows={state.data?.items ?? []}
            getRowKey={(row) => row.id}
            empty={{ title: "No certificates", description: "Record a certificate the worker holds, such as a forklift licence." }}
          />
        )}
      </div>
    </Card>
  );
}
