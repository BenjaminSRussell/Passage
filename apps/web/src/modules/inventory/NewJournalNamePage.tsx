import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createJournalName, describeProblem, KEYED_JOURNAL_TYPES, type KeyedJournalType, type Problem } from "./api/client";
import { typeLabels, UUID } from "./format";
import { fieldErrorsOf } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface JournalNameValues {
  legalEntityId: string;
  code: string;
  description: string;
  journalType: KeyedJournalType;
  requiresApproval: "yes" | "no";
  offsetAccountId: string;
}

const fields = ["legalEntityId", "code", "description", "journalType", "requiresApproval", "offsetAccountId"] as const;
type Errors = Partial<Record<(typeof fields)[number], string>>;

/** Client checks that mirror the service. */
export function validateJournalName(values: JournalNameValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!/^[A-Z0-9][A-Z0-9_-]{0,19}$/.test(values.code.trim().toUpperCase())) errors.code = "Use 1 to 20 letters, digits, _ or -.";
  if (values.description.trim().length > 200) errors.description = "Keep the description to 200 characters.";
  // The offset field is shown for movement names only; a value left from another type is ignored.
  const offset = values.journalType === "MOVEMENT" ? values.offsetAccountId.trim() : "";
  if (offset !== "" && !/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(offset)) errors.offsetAccountId = "Enter a ledger account id.";
  return errors;
}

/** New journal name (PRD 3.16.13). */
export function NewJournalNamePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<JournalNameValues>({
    legalEntityId: "",
    code: "",
    description: "",
    journalType: "ADJUSTMENT",
    requiresApproval: "no",
    offsetAccountId: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(crypto.randomUUID());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateJournalName(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await createJournalName(
        {
          legalEntityId: values.legalEntityId.trim(),
          code: values.code.trim().toUpperCase(),
          description: values.description.trim(),
          journalType: values.journalType,
          requiresApproval: values.requiresApproval === "yes",
          offsetAccountId: values.journalType === "MOVEMENT" ? values.offsetAccountId.trim() : undefined,
        },
        key.current,
      );
      navigate("/inventory/journal-names");
    } catch (error) {
      const problem = describeProblem(error);
      const mapped = fieldErrorsOf(problem, fields);
      if (Object.keys(mapped).length > 0) setErrors(mapped);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = crypto.randomUUID();
    } finally {
      setBusy(false);
    }
  }

  const set = (name: keyof JournalNameValues) => (event: { target: { value: string } }) => {
    setValues({ ...values, [name]: event.target.value });
    key.current = crypto.randomUUID();
  };

  return (
    <div className={styles.stack}>
      <PageHeader title="New journal name" description="The code, legal entity and journal type cannot change once the name exists." />
      <Card>
        <form onSubmit={submit} noValidate className={styles.form}>
          <FormField id="jnn-entity" label="Legal entity" error={errors.legalEntityId}>
            {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="jnn-code" label="Code" hint="Saved in capitals, unique per legal entity." error={errors.code}>
            {(control) => <Input {...control} value={values.code} maxLength={20} onChange={set("code")} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="jnn-type" label="Journal type" error={errors.journalType}>
            {(control) => (
              <Select {...control} value={values.journalType} onChange={set("journalType")}>
                {KEYED_JOURNAL_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {typeLabels[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="jnn-approval" label="Approval before posting" hint="When required, a journal posts only after it is submitted and approved.">
            {(control) => (
              <Select {...control} value={values.requiresApproval} onChange={set("requiresApproval")}>
                <option value="no">Not required</option>
                <option value="yes">Required</option>
              </Select>
            )}
          </FormField>
          {values.journalType === "MOVEMENT" ? (
            <FormField id="jnn-offset" label="Default offset account" optional error={errors.offsetAccountId}>
              {(control) => <Input {...control} value={values.offsetAccountId} onChange={set("offsetAccountId")} invalid={Boolean(errors.offsetAccountId)} />}
            </FormField>
          ) : null}
          <FormField id="jnn-description" label="Description" optional error={errors.description}>
            {(control) => <Input {...control} value={values.description} maxLength={200} onChange={set("description")} invalid={Boolean(errors.description)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the journal name" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create journal name
            </Button>
            <Link className={buttonClassName("ghost")} to="/inventory/journal-names">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
