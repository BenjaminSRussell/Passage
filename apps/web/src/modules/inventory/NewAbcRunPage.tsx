import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { describeProblem, runAbc, type Problem } from "./api/client";
import { DECIMAL, UUID } from "./format";
import { fieldErrorsOf } from "./parts";
import styles from "./pages.module.css";

export interface AbcValues {
  legalEntityId: string;
  a: string;
  b: string;
  c: string;
  zeroClass: "C" | "N";
}

const fields = ["legalEntityId", "a", "b", "c", "thresholds"] as const;
type Errors = Partial<Record<(typeof fields)[number], string>>;

/** Client checks that mirror the service (INV-ABC-001: thresholds sum to 100). */
export function validateAbc(values: AbcValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  let sum = 0;
  for (const name of ["a", "b", "c"] as const) {
    const v = values[name].trim();
    if (!DECIMAL.test(v) || Number(v) < 0 || Number(v) > 100) errors[name] = "Enter a percentage from 0 to 100.";
    else sum += Number(v);
  }
  if (!errors.a && !errors.b && !errors.c && Math.abs(sum - 100) > 1e-9) errors.thresholds = `The three percentages must add up to 100 (they add up to ${sum}).`;
  return errors;
}

/** Runs an ABC classification by on-hand value for one legal entity. */
export function NewAbcRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<AbcValues>({ legalEntityId: "", a: "80", b: "15", c: "5", zeroClass: "C" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(crypto.randomUUID());

  const change = (next: AbcValues) => {
    setValues(next);
    key.current = crypto.randomUUID();
  };
  const set = (name: keyof AbcValues) => (event: { target: { value: string } }) => change({ ...values, [name]: event.target.value });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAbc(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const run = await runAbc(
        { legalEntityId: values.legalEntityId.trim(), zeroClass: values.zeroClass, thresholds: { a: values.a.trim(), b: values.b.trim(), c: values.c.trim() } },
        key.current,
      );
      navigate(`/inventory/abc-runs/${run.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const mapped = fieldErrorsOf(problem, fields);
      if (problem.code === "INV-ABC-001") setErrors({ thresholds: problem.message });
      else if (Object.keys(mapped).length > 0) setErrors(mapped);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = crypto.randomUUID();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Run ABC classification" description="Ranks every product and site of the legal entity by on-hand value. The run is kept as a report." />
      <Card>
        <form onSubmit={submit} noValidate className={styles.form}>
          <FormField id="abc-legal-entity" label="Legal entity" error={errors.legalEntityId}>
            {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="abc-a" label="Class A share %" hint="Cumulative share of value held by A items." error={errors.a}>
            {(control) => <Input {...control} inputMode="decimal" value={values.a} onChange={set("a")} invalid={Boolean(errors.a)} />}
          </FormField>
          <FormField id="abc-b" label="Class B share %" error={errors.b}>
            {(control) => <Input {...control} inputMode="decimal" value={values.b} onChange={set("b")} invalid={Boolean(errors.b)} />}
          </FormField>
          <FormField id="abc-c" label="Class C share %" error={errors.c}>
            {(control) => <Input {...control} inputMode="decimal" value={values.c} onChange={set("c")} invalid={Boolean(errors.c)} />}
          </FormField>
          <FormField id="abc-zero" label="Products with no value" hint="Class C, or N to leave them unclassified.">
            {(control) => (
              <Select {...control} value={values.zeroClass} onChange={set("zeroClass")}>
                <option value="C">Class C</option>
                <option value="N">Not classified (N)</option>
              </Select>
            )}
          </FormField>
          {errors.thresholds ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Check the thresholds" live="alert">
                {errors.thresholds}
              </Alert>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not run the classification" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Run classification
            </Button>
            <Link className={buttonClassName("ghost")} to="/inventory/abc-runs">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
