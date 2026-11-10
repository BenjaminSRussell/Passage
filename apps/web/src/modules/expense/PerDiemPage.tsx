import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { computePerDiem, describeProblem, type PerDiemInput, type PerDiemLine, type PerDiemResult, type Problem } from "./api/client";
import { CURRENCY, DAY, DECIMAL, MAX_TRIP_DAYS, daysBetween, splitProblem } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

/** Service field errors this form shows beside an input. */
const SHOWN = ["rate.currency", "rate.lodging", "rate.mealsIncidentals"] as const;

export interface DayValues {
  lodging: boolean;
  breakfast: boolean;
  lunch: boolean;
  dinner: boolean;
}

export interface PerDiemValues {
  currency: string;
  lodging: string;
  mealsIncidentals: string;
  from: string;
  to: string;
  /** Per-day choices keyed by date; a date without an entry uses the defaults. */
  days: Record<string, DayValues>;
}

export const emptyPerDiem: PerDiemValues = { currency: "USD", lodging: "", mealsIncidentals: "", from: "", to: "", days: {} };

/** A night is charged on every day but the last; no meals are provided. */
function defaultDay(date: string, last: string): DayValues {
  return { lodging: date !== last, breakfast: false, lunch: false, dinner: false };
}

export function validatePerDiem(v: PerDiemValues): Errors {
  const errors: Errors = {};
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter a three-letter upper-case currency code such as USD.";
  if (!DECIMAL.test(v.lodging.trim())) errors.lodging = "Enter the lodging rate per night, such as 150.00.";
  if (!DECIMAL.test(v.mealsIncidentals.trim())) errors.mealsIncidentals = "Enter the M&IE rate per day, such as 80.00.";
  if (!DAY.test(v.from)) errors.from = "Enter the first day of travel.";
  if (!DAY.test(v.to)) errors.to = "Enter the last day of travel.";
  else if (DAY.test(v.from) && daysBetween(v.from, v.to).length === 0) {
    errors.to = `The last day must be on or after the first day, and a trip is at most ${MAX_TRIP_DAYS} days.`;
  }
  return errors;
}

export function toPerDiemInput(v: PerDiemValues): PerDiemInput {
  return {
    rate: { currency: v.currency.trim(), lodging: v.lodging.trim(), mealsIncidentals: v.mealsIncidentals.trim() },
    days: daysBetween(v.from, v.to).map((date) => {
      const d = v.days[date] ?? defaultDay(date, v.to);
      return { date, lodging: d.lodging, breakfastProvided: d.breakfast, lunchProvided: d.lunch, dinnerProvided: d.dinner };
    }),
  };
}

const DAY_CHOICES: { key: keyof DayValues; label: string }[] = [
  { key: "lodging", label: "Night charged" },
  { key: "breakfast", label: "Breakfast provided" },
  { key: "lunch", label: "Lunch provided" },
  { key: "dinner", label: "Dinner provided" },
];

/** BR-43.6 per diem for one trip: lodging per night and M&IE per day, with partial days and provided meals. Stores nothing. */
export function PerDiemPage() {
  const [values, setValues] = useState<PerDiemValues>(emptyPerDiem);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Pick<Problem, "code" | "message"> | null>(null);
  const [result, setResult] = useState<PerDiemResult | null>(null);
  const [busy, setBusy] = useState(false);

  const dates = daysBetween(values.from, values.to);

  function update(patch: Partial<PerDiemValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  function setDay(date: string, patch: Partial<DayValues>) {
    setValues((prev) => ({ ...prev, days: { ...prev.days, [date]: { ...(prev.days[date] ?? defaultDay(date, prev.to)), ...patch } } }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validatePerDiem(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await computePerDiem(toPerDiemInput(values)));
    } catch (error) {
      const split = splitProblem(describeProblem(error), SHOWN);
      setResult(null);
      setErrors(split.fields);
      setFailure(split.failure);
    } finally {
      setBusy(false);
    }
  }

  const currency = result?.currency ?? "";
  const columns: DataTableColumn<PerDiemLine>[] = [
    { key: "date", header: "Date", mono: true },
    { key: "partial", header: "Day", render: (l) => (l.partial ? "First or last day (75%)" : "Full day") },
    { key: "lodging", header: "Lodging", mono: true, align: "end", render: (l) => `${l.lodging} ${currency}` },
    { key: "mie", header: "M&IE", mono: true, align: "end", render: (l) => `${l.mie} ${currency}` },
    { key: "total", header: "Total", mono: true, align: "end", render: (l) => `${l.total} ${currency}` },
  ];

  function field(name: "currency" | "lodging" | "mealsIncidentals", label: string, hint?: string) {
    return (
      <FormField id={`pd-${name}`} label={label} hint={hint} error={errors[`rate.${name}`] ?? errors[name]}>
        {(c) => (
          <Input
            {...c}
            className={styles.mono}
            inputMode={name === "currency" ? undefined : "decimal"}
            value={values[name]}
            onChange={(e) => update({ [name]: e.target.value })}
            invalid={Boolean(errors[`rate.${name}`] ?? errors[name])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Per diem"
        description="Work out the per diem for a trip from the rate for the location. The first and last day pay 75% of meals and incidentals, and provided meals are deducted at 20%, 30% and 50% of the full-day rate. Nothing is saved."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("currency", "Currency")}
          {field("lodging", "Lodging per night")}
          {field("mealsIncidentals", "M&IE per day", "Meals and incidental expenses for a full day.")}
          <FormField id="pd-from" label="First day of travel" error={errors.from}>
            {(c) => <Input {...c} type="date" value={values.from} onChange={(e) => update({ from: e.target.value })} invalid={Boolean(errors.from)} />}
          </FormField>
          <FormField id="pd-to" label="Last day of travel" error={errors.to}>
            {(c) => <Input {...c} type="date" value={values.to} onChange={(e) => update({ to: e.target.value })} invalid={Boolean(errors.to)} />}
          </FormField>
          {dates.map((date) => {
            const d = values.days[date] ?? defaultDay(date, values.to);
            return (
              <fieldset key={date} className={styles.lineSet}>
                <legend className={styles.legend}>{date}</legend>
                {DAY_CHOICES.map(({ key, label }) => {
                  const id = `pd-${date}-${key}`;
                  return (
                    <span key={key} className={styles.check}>
                      <Input id={id} type="checkbox" checked={d[key]} onChange={(e) => setDay(date, { [key]: e.target.checked })} />
                      <label htmlFor={id}>{label}</label>
                    </span>
                  );
                })}
              </fieldset>
            );
          })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not compute the per diem" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Compute per diem
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Result">
        {result ? (
          <div className={styles.stack}>
            <StatGroup label="Per diem totals">
              <StatTile label="Lodging" value={`${result.totals.lodging} ${currency}`} />
              <StatTile label="M&IE" value={`${result.totals.mie} ${currency}`} />
              <StatTile label="Total" value={`${result.totals.total} ${currency}`} />
            </StatGroup>
            <DataTable label="Per diem by day" columns={columns} rows={result.lines} getRowKey={(l) => l.date} />
          </div>
        ) : busy ? (
          <DataTable label="Per diem by day" columns={columns} rows={[]} getRowKey={(l) => l.date} loading />
        ) : (
          <EmptyState icon="receipt" title="No per diem worked out yet" description="Enter the rate and the travel days, then compute." compact />
        )}
      </Card>
    </div>
  );
}
