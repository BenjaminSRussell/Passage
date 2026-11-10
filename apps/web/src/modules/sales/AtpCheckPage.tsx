import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { atpCheck, describeProblem, type AtpCheckInput, type AtpCheckResult, type AtpPeriod, type AtpScheduleLine, type Problem } from "./api/client";
import { DECIMAL, today } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface AtpValues {
  requested: string;
  quantity: string;
  completeOnly: boolean;
  periods: AtpPeriod[];
}

/** Checks that mirror the service (api/atp.go). Keys are the service's field paths. */
export function validateAtp(values: AtpValues): { errors: Errors; input?: AtpCheckInput } {
  const errors: Errors = {};
  if (!DATE.test(values.requested)) errors.requested = "Choose the requested date.";
  if (!DECIMAL.test(values.quantity) || Number(values.quantity) <= 0) errors.quantity = "Enter a quantity above 0 with up to 6 decimals.";
  if (values.periods.length === 0) errors.periods = "Add at least one bucket.";
  values.periods.forEach((p, i) => {
    const at = (name: string) => `periods[${i}].${name}`;
    if (!DATE.test(p.date)) errors[at("date")] = "Choose the bucket date.";
    else if (i > 0 && DATE.test(values.periods[i - 1].date) && p.date <= values.periods[i - 1].date)
      errors[at("date")] = "Use a date later than the previous bucket.";
    if (!DECIMAL.test(p.supply)) errors[at("supply")] = "Enter 0 or more.";
    if (!DECIMAL.test(p.demand)) errors[at("demand")] = "Enter 0 or more.";
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { requested: values.requested, quantity: values.quantity, completeOnly: values.completeOnly, periods: values.periods } };
}

interface BucketRow extends AtpPeriod {
  index: number;
  cumulative: string;
}

const bucketColumns: DataTableColumn<BucketRow>[] = [
  { key: "index", header: "Bucket", align: "end" },
  { key: "date", header: "Date", mono: true },
  { key: "supply", header: "Supply", align: "end" },
  { key: "demand", header: "Demand", align: "end" },
  { key: "cumulative", header: "Cumulative ATP", align: "end" },
];

const scheduleColumns: DataTableColumn<AtpScheduleLine>[] = [
  { key: "date", header: "Ship date", mono: true },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "period", header: "Bucket", align: "end" },
];

/** The promise in one sentence: a date, a split, or a backorder. */
export function promiseSummary(result: AtpCheckResult, quantity: string): string {
  const { promise } = result;
  if (promise.complete && promise.schedule.length === 1) return `${quantity} can ship on ${promise.promisedDate}.`;
  if (promise.complete) return `${quantity} can ship in ${promise.schedule.length} deliveries, the last on ${promise.promisedDate}.`;
  if (promise.schedule.length === 0) return `None of ${quantity} can be promised; ${promise.backorder} goes on backorder.`;
  return `${quantity} cannot ship in full; ${promise.backorder} goes on backorder after the last delivery on ${promise.promisedDate}.`;
}

/**
 * ATP check (BR-27.1): enter supply and demand buckets and see the cumulative
 * ATP and when a quantity can be promised. The stored projection that fills
 * these buckets from inventory and planning is a later slice.
 */
export function AtpCheckPage() {
  const [values, setValues] = useState<AtpValues>({
    requested: today(),
    quantity: "",
    completeOnly: true,
    periods: [{ date: today(), supply: "0", demand: "0" }],
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<{ value: AtpCheckResult; quantity: string } | null>(null);
  const [busy, setBusy] = useState(false);

  function updateBucket(i: number, patch: Partial<AtpPeriod>) {
    setValues((v) => ({ ...v, periods: v.periods.map((p, idx) => (idx === i ? { ...p, ...patch } : p)) }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAtp(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult({ value: await atpCheck(input), quantity: input.quantity });
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const buckets: BucketRow[] =
    result?.value.periods.map((p, i) => ({ ...p, index: i, cumulative: result.value.cumulative[i] ?? "" })) ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="ATP check"
        description="Enter the supply and demand per bucket, earliest first, to see cumulative available-to-promise and the date a quantity can ship."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="ATP check">
          <FormField id="atp-requested" label="Requested date" error={errors.requested}>
            {(control) => (
              <Input {...control} type="date" value={values.requested} onChange={(e) => setValues((v) => ({ ...v, requested: e.target.value }))} invalid={Boolean(errors.requested)} />
            )}
          </FormField>
          <FormField id="atp-quantity" label="Quantity" error={errors.quantity}>
            {(control) => (
              <Input {...control} inputMode="decimal" value={values.quantity} onChange={(e) => setValues((v) => ({ ...v, quantity: e.target.value }))} invalid={Boolean(errors.quantity)} />
            )}
          </FormField>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="atp-complete" type="checkbox" checked={values.completeOnly} onChange={(e) => setValues((v) => ({ ...v, completeOnly: e.target.checked }))} />
              <label htmlFor="atp-complete">Complete delivery only</label>
            </span>
            <span className={styles.note}>Clear it to allow partial deliveries split over buckets.</span>
          </div>
          <div className={`${styles.formActions} ${styles.lines}`}>
            {values.periods.map((p, i) => (
              <fieldset key={i} className={styles.lineCard}>
                <legend className={styles.lineLegend}>Bucket {i + 1}</legend>
                <FormField id={`bucket-${i}-date`} label="Date" error={errors[`periods[${i}].date`]}>
                  {(control) => <Input {...control} type="date" value={p.date} onChange={(e) => updateBucket(i, { date: e.target.value })} invalid={Boolean(errors[`periods[${i}].date`])} />}
                </FormField>
                <FormField id={`bucket-${i}-supply`} label="Supply" error={errors[`periods[${i}].supply`]}>
                  {(control) => <Input {...control} inputMode="decimal" value={p.supply} onChange={(e) => updateBucket(i, { supply: e.target.value })} invalid={Boolean(errors[`periods[${i}].supply`])} />}
                </FormField>
                <FormField id={`bucket-${i}-demand`} label="Demand" error={errors[`periods[${i}].demand`]}>
                  {(control) => <Input {...control} inputMode="decimal" value={p.demand} onChange={(e) => updateBucket(i, { demand: e.target.value })} invalid={Boolean(errors[`periods[${i}].demand`])} />}
                </FormField>
                {values.periods.length > 1 ? (
                  <div className={styles.formActions}>
                    <Button size="sm" variant="ghost" onClick={() => setValues((v) => ({ ...v, periods: v.periods.filter((_, idx) => idx !== i) }))}>
                      Remove bucket {i + 1}
                    </Button>
                  </div>
                ) : null}
              </fieldset>
            ))}
            {errors.periods ? (
              <p role="alert" className={styles.fieldError}>
                {errors.periods}
              </p>
            ) : null}
            <div>
              <Button onClick={() => setValues((v) => ({ ...v, periods: [...v.periods, { date: "", supply: "0", demand: "0" }] }))}>Add bucket</Button>
            </div>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not check ATP" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Check ATP
            </Button>
          </div>
        </form>
      </Card>
      <p role="status" className={styles.muted}>
        {result ? promiseSummary(result.value, result.quantity) : ""}
      </p>
      {result ? (
        <>
          <DataTable label="Promise schedule" columns={scheduleColumns} rows={result.value.promise.schedule} getRowKey={(s, i) => `${s.date}-${i}`} empty={{ title: "Nothing can be promised", description: "Add supply, or clear Complete delivery only to allow partial deliveries." }} />
          <DataTable label="Cumulative ATP" columns={bucketColumns} rows={buckets} getRowKey={(b) => String(b.index)} />
        </>
      ) : null}
    </div>
  );
}
