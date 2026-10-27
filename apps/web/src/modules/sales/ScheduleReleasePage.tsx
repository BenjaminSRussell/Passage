import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { checkScheduleRelease, describeProblem, type Problem, type ReleaseBucket, type ReleaseCheckInput, type ReleaseCheckResult } from "./api/client";
import { DECIMAL } from "./format";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface ReleaseValues {
  cumReceived: string;
  cumShipped: string;
  tolerance: string;
  buckets: ReleaseBucket[];
}

/** Checks that mirror the service (api/release.go, domain/schedule.go). Keys are the service's field paths. */
export function validateRelease(values: ReleaseValues): { errors: Errors; input?: ReleaseCheckInput } {
  const errors: Errors = {};
  if (!DECIMAL.test(values.cumReceived)) errors.cumReceived = "Enter the customer's cumulative received quantity (0 or more).";
  if (!DECIMAL.test(values.cumShipped)) errors.cumShipped = "Enter the cumulative shipped quantity (0 or more).";
  if (values.tolerance !== "" && !DECIMAL.test(values.tolerance)) errors.tolerance = "Enter 0 or more, or leave it empty for 0.";
  if (values.buckets.length === 0) errors.buckets = "Add at least one requirement date.";
  values.buckets.forEach((b, i) => {
    const at = (name: string) => `buckets[${i}].${name}`;
    const prev = values.buckets[i - 1];
    if (!DATE.test(b.date)) errors[at("date")] = "Choose the requirement date.";
    else if (prev && DATE.test(prev.date) && b.date < prev.date) errors[at("date")] = "Use a date on or after the previous one.";
    if (!DECIMAL.test(b.cumRequired)) errors[at("cumRequired")] = "Enter 0 or more.";
    else if (prev && DECIMAL.test(prev.cumRequired) && Number(b.cumRequired) < Number(prev.cumRequired))
      errors[at("cumRequired")] = "Cumulative required may not go down.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  const input: ReleaseCheckInput = { cumReceived: values.cumReceived, cumShipped: values.cumShipped, buckets: values.buckets };
  if (values.tolerance !== "") input.tolerance = values.tolerance;
  return { errors, input };
}

const lineColumns: DataTableColumn<{ date: string; quantity: string }>[] = [
  { key: "date", header: "Due date", mono: true },
  { key: "quantity", header: "Open quantity", align: "end" },
];

/**
 * Scheduling-release check (FR-27.20, BR-27.12): reconcile a forecast or JIT
 * release's cumulative received quantity against what was shipped and see the
 * open schedule lines it would create. Nothing is stored; stored releases on a
 * sales agreement are a later slice.
 */
export function ScheduleReleasePage() {
  const [values, setValues] = useState<ReleaseValues>({ cumReceived: "", cumShipped: "", tolerance: "", buckets: [{ date: "", cumRequired: "" }] });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<ReleaseCheckResult | null>(null);
  const [busy, setBusy] = useState(false);

  function updateBucket(i: number, patch: Partial<ReleaseBucket>) {
    setValues((v) => ({ ...v, buckets: v.buckets.map((b, idx) => (idx === i ? { ...b, ...patch } : b)) }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRelease(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult(await checkScheduleRelease(input));
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Scheduling release check"
        description="Reconcile a customer's delivery schedule release against shipments before accepting it. A cumulative mismatch beyond the tolerance holds the release."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Scheduling release check">
          <FormField id="rel-received" label="Cumulative received (customer)" error={errors.cumReceived}>
            {(control) => <Input {...control} inputMode="decimal" value={values.cumReceived} onChange={(e) => setValues((v) => ({ ...v, cumReceived: e.target.value }))} invalid={Boolean(errors.cumReceived)} />}
          </FormField>
          <FormField id="rel-shipped" label="Cumulative shipped" error={errors.cumShipped}>
            {(control) => <Input {...control} inputMode="decimal" value={values.cumShipped} onChange={(e) => setValues((v) => ({ ...v, cumShipped: e.target.value }))} invalid={Boolean(errors.cumShipped)} />}
          </FormField>
          <FormField id="rel-tolerance" label="Tolerance" optional hint="Default 0." error={errors.tolerance}>
            {(control) => <Input {...control} inputMode="decimal" value={values.tolerance} onChange={(e) => setValues((v) => ({ ...v, tolerance: e.target.value }))} invalid={Boolean(errors.tolerance)} />}
          </FormField>
          <div className={`${styles.formActions} ${styles.lines}`}>
            {values.buckets.map((b, i) => (
              <fieldset key={i} className={styles.lineCard}>
                <legend className={styles.lineLegend}>Requirement {i + 1}</legend>
                <FormField id={`rel-${i}-date`} label="Date" error={errors[`buckets[${i}].date`]}>
                  {(control) => <Input {...control} type="date" value={b.date} onChange={(e) => updateBucket(i, { date: e.target.value })} invalid={Boolean(errors[`buckets[${i}].date`])} />}
                </FormField>
                <FormField id={`rel-${i}-cum`} label="Cumulative required" error={errors[`buckets[${i}].cumRequired`]}>
                  {(control) => <Input {...control} inputMode="decimal" value={b.cumRequired} onChange={(e) => updateBucket(i, { cumRequired: e.target.value })} invalid={Boolean(errors[`buckets[${i}].cumRequired`])} />}
                </FormField>
                {values.buckets.length > 1 ? (
                  <div className={styles.formActions}>
                    <Button size="sm" variant="ghost" onClick={() => setValues((v) => ({ ...v, buckets: v.buckets.filter((_, idx) => idx !== i) }))}>
                      Remove requirement {i + 1}
                    </Button>
                  </div>
                ) : null}
              </fieldset>
            ))}
            {errors.buckets ? (
              <p role="alert" className={styles.fieldError}>
                {errors.buckets}
              </p>
            ) : null}
            <div>
              <Button onClick={() => setValues((v) => ({ ...v, buckets: [...v.buckets, { date: "", cumRequired: "" }] }))}>Add requirement</Button>
            </div>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not check the release" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Check release
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone={result?.held ? "warning" : "success"} live="status" title={result ? (result.held ? "Release held: cumulative quantity mismatch" : "Release reconciles") : undefined}>
        {result
          ? result.held
            ? `The customer's cumulative received differs from shipped by ${result.difference}. Review the release (${result.code}, ${result.serviceCode}); no schedule line changes.`
            : `${result.openQuantity} open over ${result.lines.length} schedule line${result.lines.length === 1 ? "" : "s"}.`
          : null}
      </Alert>
      {result && !result.held ? (
        <DataTable
          label="Open schedule lines"
          columns={lineColumns}
          rows={result.lines}
          getRowKey={(l) => l.date}
          empty={{ title: "Nothing open", description: "Everything required up to the last date has already shipped." }}
        />
      ) : null}
    </div>
  );
}
