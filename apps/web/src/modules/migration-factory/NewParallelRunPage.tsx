import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeProblem, newKey, startParallelRun, type ParallelRunRequest, type Problem } from "./api/client";
import { parseTuples, PERIOD, SOURCE_LABELS, SOURCE_SYSTEMS, type SourceSystem } from "./domain";
import styles from "./pages.module.css";

export interface ParallelRunValues {
  period: string;
  sourceSystem: SourceSystem;
  documentsReplayed: string;
  revenue: string;
  explainedMaxPct: string;
  legacy: string;
  passage: string;
}

type Field = keyof ParallelRunValues;
type Errors = Partial<Record<Field, string>>;

const WHOLE = /^\d{1,9}$/;
const AMOUNT = /^\d{1,22}(\.\d{1,6})?$/;
const PCT = /^(0(\.\d{1,6})?|1(\.0{1,6})?)$/;

/** The checks the service makes before it diffs, and the request the form sends. */
export function validateParallelRun(values: ParallelRunValues): { errors: Errors; input?: ParallelRunRequest } {
  const errors: Errors = {};
  const period = values.period.trim();
  if (!PERIOD.test(period)) errors.period = "Enter the period as YYYY-MM.";
  const docs = values.documentsReplayed.trim();
  if (!WHOLE.test(docs)) errors.documentsReplayed = "Enter the number of documents replayed.";
  const revenue = values.revenue.trim();
  if (!AMOUNT.test(revenue)) errors.revenue = "Enter the period revenue, such as 1250000.00.";
  const pct = values.explainedMaxPct.trim();
  if (pct !== "" && !PCT.test(pct)) errors.explainedMaxPct = "Enter a percentage from 0 to 1.";
  const legacy = parseTuples(values.legacy);
  const passage = parseTuples(values.passage);
  if (legacy.errors.length > 0) errors.legacy = legacy.errors.slice(0, 3).join(" ");
  if (passage.errors.length > 0) errors.passage = passage.errors.slice(0, 3).join(" ");
  if (!errors.legacy && !errors.passage && legacy.tuples.length + passage.tuples.length === 0) errors.legacy = "Paste the legacy and the Passage tuples.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      period,
      sourceSystem: values.sourceSystem,
      documentsReplayed: Number(docs),
      revenue,
      ...(pct !== "" ? { explainedMaxPct: pct } : {}),
      legacy: legacy.tuples,
      passage: passage.tuples,
    },
  };
}

/** Diff a replayed legacy period against Passage (FR-49.5). */
export function NewParallelRunPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ParallelRunValues>({
    period: "",
    sourceSystem: "AX2012",
    documentsReplayed: "",
    revenue: "",
    explainedMaxPct: "",
    legacy: "",
    passage: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ParallelRunValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateParallelRun(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const run = await startParallelRun(input, key.current);
      navigate(`/migration-factory/parallel-runs/${run.parallelRunId}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(
        problem.code === "MIG-0009" ? { ...problem, message: `${problem.message}. A period is diffed once per source system; open the existing run from the list.` } : problem,
      );
      if (problem.code === "MIG-0007") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Field, label: string, hint?: string, extra?: Record<string, unknown>, optional?: boolean) => (
    <FormField id={`parallel-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<ParallelRunValues>)} />
      )}
    </FormField>
  );

  const tuples = (id: "legacy" | "passage", label: string) => (
    <div className={styles.wide}>
      <FormField id={`parallel-${id}`} label={label} hint="One per line: area (TB, INV, AP, AR or FA), key, amount." error={errors[id]}>
        {(control) => <Textarea {...control} mono rows={8} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value })} />}
      </FormField>
    </div>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Start parallel run"
        description="Paste the legacy period's canonical tuples and the tuples the replay produced in Passage. Differences of at most 0.01 are expected by rule RND-001; the rest need a reviewer's reason."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("period", "Period", "YYYY-MM", { placeholder: "2026-11" })}
          <FormField id="parallel-sourceSystem" label="Source system">
            {(control) => (
              <Select {...control} value={values.sourceSystem} onChange={(event) => update({ sourceSystem: event.target.value as SourceSystem })}>
                {SOURCE_SYSTEMS.map((s) => (
                  <option key={s} value={s}>
                    {SOURCE_LABELS[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("documentsReplayed", "Documents replayed", undefined, { inputMode: "numeric" })}
          {text("revenue", "Period revenue", "The base of the explained-amount limit.", { inputMode: "decimal" })}
          {text("explainedMaxPct", "Explained limit (% of revenue)", "Default 0.1.", { inputMode: "decimal" }, true)}
          {tuples("legacy", "Legacy tuples")}
          {tuples("passage", "Passage tuples")}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not start the parallel run" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Diff period
            </Button>
            <Link className={buttonClassName("ghost")} to="/migration-factory/parallel-runs">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
