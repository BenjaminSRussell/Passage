import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createDowntimeReason, describeProblem, newKey, type NewDowntimeReason, type Problem } from "./api/client";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

interface Values {
  code: string;
  name: string;
  category: string;
}

type Field = keyof Values;
type Errors = Partial<Record<Field, string>>;

/** Checks that mirror the service, and the request the form sends. */
export function validateDowntimeReason(values: Values): { errors: Errors; input?: NewDowntimeReason } {
  const errors: Errors = {};
  const code = values.code.trim();
  const name = values.name.trim();
  if (code.length < 1 || code.length > 32) errors.code = "Enter a code of up to 32 characters.";
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (values.category !== "planned" && values.category !== "unplanned") errors.category = "Choose planned or unplanned.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code, name, category: values.category as "planned" | "unplanned" } };
}

/** Create a downtime reason code (FR-25.11). */
export function NewDowntimeReasonPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ code: "", name: "", category: "unplanned" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateDowntimeReason(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createDowntimeReason(input, key.current);
      navigate("/shopfloor/downtime-reasons");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["code", "name", "category"] as const) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "SF-0142") fieldErrors.code = "This code is already used.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New downtime reason" description="A planned stop, such as a changeover, does not lower availability; an unplanned stop does." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="reason-code" label="Code" hint="Unique, for example JAM." error={errors.code}>
            {(control) => <Input {...control} maxLength={32} value={values.code} onChange={(event) => update({ code: event.target.value })} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="reason-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} maxLength={120} value={values.name} onChange={(event) => update({ name: event.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="reason-category" label="Category" error={errors.category}>
            {(control) => (
              <Select {...control} value={values.category} onChange={(event) => update({ category: event.target.value })} invalid={Boolean(errors.category)}>
                <option value="unplanned">Unplanned</option>
                <option value="planned">Planned</option>
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the downtime reason" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create downtime reason
            </Button>
            <Link className={buttonClassName("ghost")} to="/shopfloor/downtime-reasons">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
