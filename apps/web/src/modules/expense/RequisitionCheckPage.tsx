import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { checkRequisition, describeProblem, type Problem, type RequisitionCheckInput, type RequisitionCheckResult } from "./api/client";
import { DAY, splitProblem, splitList, today } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

export interface RequisitionValues {
  from: string;
  to: string;
  travelDates: string;
  lastTravelDate: string;
  asOf: string;
  reportPosted: boolean;
}

const MAX_DATES = 366;

export function emptyRequisition(asOf = today()): RequisitionValues {
  return { from: "", to: "", travelDates: "", lastTravelDate: "", asOf, reportPosted: false };
}

export function validateRequisition(v: RequisitionValues): Errors {
  const errors: Errors = {};
  if (!DAY.test(v.from)) errors["trip.from"] = "Enter the first approved travel day.";
  if (!DAY.test(v.to)) errors["trip.to"] = "Enter the last approved travel day.";
  else if (DAY.test(v.from) && v.to < v.from) errors["trip.to"] = "The approved end must be on or after the start.";
  const dates = splitList(v.travelDates);
  if (dates.length > MAX_DATES) errors.travelDates = `Enter at most ${MAX_DATES} dates.`;
  else if (dates.some((d) => !DAY.test(d))) errors.travelDates = "Enter dates as YYYY-MM-DD, separated by commas or new lines.";
  if (v.lastTravelDate !== "" && !DAY.test(v.lastTravelDate)) errors.lastTravelDate = "Enter a date, or leave it empty to use the approved end.";
  if (!DAY.test(v.asOf)) errors.asOf = "Enter the date to check on.";
  return errors;
}

export function toRequisitionInput(v: RequisitionValues): RequisitionCheckInput {
  return {
    trip: { from: v.from, to: v.to },
    travelDates: splitList(v.travelDates),
    ...(v.lastTravelDate !== "" ? { lastTravelDate: v.lastTravelDate } : {}),
    asOf: v.asOf,
    reportPosted: v.reportPosted,
  };
}

const SHOWN = ["trip.from", "trip.to", "travelDates", "lastTravelDate", "asOf"];
const dateColumns: DataTableColumn<{ date: string }>[] = [
  { key: "date", header: "Date", mono: true },
  { key: "outcome", header: "Outcome", render: () => <Badge tone="warning">Warning</Badge> },
];

/** BR-43.13 travel requisition window and closure: report dates outside the approved trip, and when the requisition closes. Stores nothing. */
export function RequisitionCheckPage() {
  const [values, setValues] = useState<RequisitionValues>(() => emptyRequisition());
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Pick<Problem, "code" | "message"> | null>(null);
  const [result, setResult] = useState<RequisitionCheckResult | null>(null);
  const [checkedPosted, setCheckedPosted] = useState(false);
  const [busy, setBusy] = useState(false);

  function update(patch: Partial<RequisitionValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRequisition(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await checkRequisition(toRequisitionInput(values)));
      setCheckedPosted(values.reportPosted);
    } catch (error) {
      const split = splitProblem(describeProblem(error), SHOWN);
      setResult(null);
      setErrors(split.fields);
      setFailure(split.failure);
    } finally {
      setBusy(false);
    }
  }

  function date(name: "from" | "to" | "lastTravelDate" | "asOf", errorKey: string, label: string, opts: { hint?: string; optional?: boolean } = {}) {
    return (
      <FormField id={`req-${name}`} label={label} hint={opts.hint} optional={opts.optional} error={errors[errorKey]}>
        {(c) => <Input {...c} type="date" value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[errorKey])} />}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Travel requisition check"
        description="Compare the dates on an expense report with the approved trip. Dates outside it are flagged as warnings. An approved requisition closes when its report posts, or 60 days after the last travel date. Nothing is saved."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {date("from", "trip.from", "Approved from")}
          {date("to", "trip.to", "Approved to")}
          <div className={styles.wide}>
            <FormField id="req-dates" label="Dates on the report" hint="YYYY-MM-DD, separated by commas or new lines." error={errors.travelDates}>
              {(c) => <Textarea {...c} mono value={values.travelDates} onChange={(e) => update({ travelDates: e.target.value })} invalid={Boolean(errors.travelDates)} />}
            </FormField>
          </div>
          {date("lastTravelDate", "lastTravelDate", "Last travel date", { hint: "Leave empty to use the approved end.", optional: true })}
          {date("asOf", "asOf", "Check on")}
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="req-posted" type="checkbox" checked={values.reportPosted} onChange={(e) => update({ reportPosted: e.target.checked })} />
              <label htmlFor="req-posted">Linked report is posted</label>
            </span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not check the requisition" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Check requisition
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Result">
        {result ? (
          <div className={styles.stack}>
            {result.closeDue ? (
              <Alert tone="info" title="The requisition is due to close">
                {checkedPosted ? "Its linked report is posted." : `It reached its close date, ${result.closeDate}.`}
              </Alert>
            ) : (
              <Alert tone="success" title="The requisition stays open">
                {`It closes on ${result.closeDate} unless its linked report posts first.`}
              </Alert>
            )}
            <DataTable
              label="Dates outside the approved trip"
              columns={dateColumns}
              rows={result.outOfRangeDates.map((d) => ({ date: d }))}
              getRowKey={(r, i) => `${r.date}-${i}`}
              empty={{ title: "Every date is inside the approved trip" }}
            />
          </div>
        ) : busy ? (
          <DataTable label="Dates outside the approved trip" columns={dateColumns} rows={[]} getRowKey={(r) => r.date} loading />
        ) : (
          <EmptyState icon="calendarCheck" title="No requisition checked yet" description="Enter the approved trip and the report dates, then check." compact />
        )}
      </Card>
    </div>
  );
}
