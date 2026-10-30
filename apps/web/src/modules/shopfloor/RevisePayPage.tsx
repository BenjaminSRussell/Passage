import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, MAX_PAY_RANGE_DAYS, newKey, revisePay, type PayRevision, type Problem, type RevisedDay } from "./api/client";
import { addDays, browserZone, daysBetween, describeLines, formatPayTotal, today, UUID } from "./format";
import styles from "./pages.module.css";

export interface Values {
  from: string;
  to: string;
  reason: string;
}

export type Errors = Partial<Record<keyof Values, string>>;

/** Checks that mirror the service, and the request the form sends. */
export function validateRevision(values: Values): Errors {
  const errors: Errors = {};
  const span = daysBetween(values.from, values.to);
  if (span === null) errors.from = "Enter both dates.";
  else if (span < 1) errors.to = "To must not be before From.";
  else if (span > MAX_PAY_RANGE_DAYS) errors.to = `Revise at most ${MAX_PAY_RANGE_DAYS} days at a time.`;
  const reason = values.reason.trim();
  if (reason.length < 1 || reason.length > 500) errors.reason = "Say why the pay is revised, up to 500 characters.";
  return errors;
}

const columns: DataTableColumn<RevisedDay>[] = [
  { key: "workDate", header: "Date", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  {
    key: "changed",
    header: "Result",
    render: (row) => (row.changed ? <Badge tone="info">{row.revision > 0 ? `Revision ${row.revision}` : "Recalculated"}</Badge> : "Unchanged"),
  },
  { key: "reversed", header: "Reversed", render: (row) => (row.reversedLines.length ? `${describeLines(row.reversedLines)} (${formatPayTotal(row.reversedLines)})` : "None") },
  { key: "added", header: "Added", render: (row) => (row.addedLines.length ? `${describeLines(row.addedLines)} (${formatPayTotal(row.addedLines)})` : "None") },
];

/**
 * Recalculate a worker's range after corrections. An approved day keeps its lines: lines that
 * changed are cancelled by reversal lines and new lines are added (BR-25.8, AC-25.8).
 */
export function RevisePayPage() {
  const { id = "" } = useParams();
  const [values, setValues] = useState<Values>({ from: addDays(today(), -6), to: today(), reason: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<PayRevision | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const zone = browserZone();

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setResult(null);
    const found = validateRevision(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    if (!UUID.test(id)) {
      setFailure({ code: "SF-0101", message: "This address does not name a worker.", fields: {} });
      return;
    }
    setBusy(true);
    try {
      setResult(await revisePay({ workerId: id, from: values.from, to: values.to, timeZone: zone, reason: values.reason.trim() }, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["from", "to", "reason"] as const) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else if (problem.code === "SF-0129") setFailure({ ...problem, message: "The recalculated pay matches the stored pay, so there is nothing to revise." });
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Revise pay"
        description={`Recalculates the range in ${zone} from the stored registrations. Approved lines are never edited: changes are paid through reversal lines and new lines in the next payroll export.`}
        actions={
          <Link className={buttonClassName("secondary")} to={`/shopfloor/workers/${id}`}>
            Back to worker
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="revise-from" label="From" error={errors.from}>
            {(control) => <Input {...control} type="date" value={values.from} invalid={Boolean(errors.from)} onChange={(event) => update({ from: event.target.value })} />}
          </FormField>
          <FormField id="revise-to" label="To" error={errors.to}>
            {(control) => <Input {...control} type="date" value={values.to} invalid={Boolean(errors.to)} onChange={(event) => update({ to: event.target.value })} />}
          </FormField>
          <FormField id="revise-reason" label="Reason" hint="Kept with the revision, e.g. clock-out corrected after the shift lead confirmed." error={errors.reason}>
            {(control) => (
              <Textarea {...control} rows={3} maxLength={500} value={values.reason} invalid={Boolean(errors.reason)} onChange={(event) => update({ reason: event.target.value })} />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not revise the pay" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Revise pay
            </Button>
          </div>
        </form>
      </Card>
      {result ? (
        <Card title="Revision">
          <DataTable
            label="Revised days"
            columns={columns}
            rows={result.days}
            getRowKey={(row) => row.workDate}
            empty={{ title: "No days in this range", description: "Nothing was recalculated." }}
          />
        </Card>
      ) : null}
    </div>
  );
}
