import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, newKey, startRun, type NewRun, type Problem } from "./api/client";
import { fromUtcInput, UUID } from "./format";
import styles from "./pages.module.css";

interface Values {
  scope: "site" | "selection";
  mode: "jobs" | "operations";
  finite: boolean;
  operationIds: string;
  start: string;
  horizonDays: string;
}

type Errors = Partial<Record<keyof Values, string>>;

/** Parses one operation id per line (commas and spaces also separate). */
export function parseIds(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim().toLowerCase())
    .filter(Boolean);
}

/** Client-side checks that mirror the service. */
export function validateRun(values: Values): Errors {
  const errors: Errors = {};
  if (values.scope === "selection") {
    const ids = parseIds(values.operationIds);
    if (ids.length === 0) errors.operationIds = "List at least one operation id.";
    else if (ids.some((id) => !UUID.test(id))) errors.operationIds = "Every entry must be an operation id (a UUID).";
    else if (ids.length > 5000) errors.operationIds = "A selection holds at most 5,000 operations.";
  }
  if (values.start !== "" && fromUtcInput(values.start) === null) errors.start = "Enter a date and time.";
  if (values.horizonDays !== "") {
    const n = Number(values.horizonDays);
    if (!Number.isInteger(n) || n < 30 || n > 730) errors.horizonDays = "Enter a whole number of days from 30 to 730.";
  }
  return errors;
}

const fieldNames: Record<string, keyof Values> = { scope: "scope", mode: "mode", operationIds: "operationIds", start: "start", horizonDays: "horizonDays" };

/** Start a heuristic run (FR-23.5, FR-23.11). */
export function NewRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ scope: "site", mode: "jobs", finite: true, operationIds: "", start: "", horizonDays: "" });
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
    const found = validateRun(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const input: NewRun = { scope: values.scope, mode: values.mode, finite: values.finite };
    if (values.scope === "selection") input.operationIds = parseIds(values.operationIds);
    const start = fromUtcInput(values.start);
    if (start) input.start = start;
    if (values.horizonDays !== "") input.horizonDays = Number(values.horizonDays);
    setBusy(true);
    try {
      const run = await startRun(input, key.current);
      navigate(`/scheduling/runs/${run.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        const field = fieldNames[name];
        if (field) fieldErrors[field] = message;
      }
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Start a schedule run"
        description="The heuristic places operations forward from the start time on the candidate that finishes first. Firmed jobs stay where they are."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="run-scope" label="Scope">
            {(control) => (
              <Select {...control} value={values.scope} onChange={(event) => update({ scope: event.target.value as Values["scope"] })}>
                <option value="site">Whole site</option>
                <option value="selection">Selected operations</option>
              </Select>
            )}
          </FormField>
          <FormField id="run-mode" label="Mode" hint="Both modes place work to the minute in this release.">
            {(control) => (
              <Select {...control} value={values.mode} onChange={(event) => update({ mode: event.target.value as Values["mode"] })}>
                <option value="jobs">Jobs</option>
                <option value="operations">Operations</option>
              </Select>
            )}
          </FormField>
          <FormField id="run-start" label="Start (UTC)" optional hint="Defaults to now. Must not be before today." error={errors.start}>
            {(control) => <Input {...control} type="datetime-local" value={values.start} onChange={(event) => update({ start: event.target.value })} invalid={Boolean(errors.start)} />}
          </FormField>
          <FormField id="run-horizon" label="Horizon days" optional hint="30 to 730; defaults to the site setting." error={errors.horizonDays}>
            {(control) => (
              <Input {...control} inputMode="numeric" value={values.horizonDays} onChange={(event) => update({ horizonDays: event.target.value })} invalid={Boolean(errors.horizonDays)} />
            )}
          </FormField>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="run-finite" type="checkbox" checked={values.finite} onChange={(event) => update({ finite: event.target.checked })} />
              <label htmlFor="run-finite">Finite capacity</label>
            </span>
            <span className={styles.note}>Clear it for an infinite-capacity run: jobs may overlap and show overload.</span>
          </div>
          {values.scope === "selection" ? (
            <div className={styles.formActions}>
              <FormField id="run-operations" label="Operation ids" hint="One per line." error={errors.operationIds}>
                {(control) => (
                  <Textarea {...control} rows={5} value={values.operationIds} onChange={(event) => update({ operationIds: event.target.value })} invalid={Boolean(errors.operationIds)} />
                )}
              </FormField>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The run did not start" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Start run
            </Button>
            <Link className={buttonClassName("ghost")} to="/scheduling/runs">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
