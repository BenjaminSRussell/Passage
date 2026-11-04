import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, type DataTableColumn } from "../../shell/ui";
import { addWorkerCostRate, describeProblem, listWorkerCostRates, newKey, type CostRate, type Problem, type Worker } from "./api/client";
import { dash, fixed, formatDateTime, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const NOTE_MAX = 500;
const RATE_PATTERN = /^\d{1,14}(\.\d{1,6})?$/;

function explain(problem: Problem): string {
  switch (problem.code) {
    case "SF-0170":
      return "The worker already has a rate from this date. Rates are never changed; set the new rate from a later date.";
    case "SF-0106":
      return "You can't set your own cost rate. Ask another supervisor.";
    default:
      return problem.message;
  }
}

/**
 * The worker's dated cost rates (D2a'). A day is priced at the latest rate from a date on or before
 * it, and days before the first dated rate at the worker's own cost rate. Adding a rate rewrites no
 * pay line: a recalculation (or a revision of an approved day) prices each day at the rate of its
 * own date. The service enforces shopfloor.setup.maintain.
 */
export function WorkerCostRates({ worker }: { worker: Worker }) {
  const state = useLoad<CostRate[]>((signal) => listWorkerCostRates(worker.id, signal), `cost-rates|${worker.id}`);
  const [from, setFrom] = useState(today());
  const [rate, setRate] = useState("");
  const [note, setNote] = useState("");
  const [errors, setErrors] = useState<{ from?: string; rate?: string; note?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function edit(change: () => void) {
    change();
    setSaved(false);
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved(false);
    const found: typeof errors = {};
    if (!/^\d{4}-\d{2}-\d{2}$/.test(from)) found.from = "Enter the date the rate starts.";
    if (!RATE_PATTERN.test(rate.trim())) found.rate = "Enter a rate of zero or more, with at most 6 decimals.";
    if (note.trim().length > NOTE_MAX) found.note = `Keep the note to ${NOTE_MAX} characters.`;
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await addWorkerCostRate(worker.id, { effectiveFrom: from, costRate: rate.trim(), note: note.trim() || null }, key.current);
      setSaved(true);
      setRate("");
      setNote("");
      key.current = newKey();
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: typeof errors = {};
      if (problem.fields.effectiveFrom) fieldErrors.from = problem.fields.effectiveFrom;
      if (problem.fields.costRate) fieldErrors.rate = problem.fields.costRate;
      if (problem.fields.note) fieldErrors.note = problem.fields.note;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<CostRate>[] = [
    { key: "effectiveFrom", header: "From", mono: true },
    { key: "costRate", header: `Rate per hour (${worker.currency})`, align: "end", render: (row) => fixed(row.costRate, 2) },
    { key: "note", header: "Note", render: (row) => row.note ?? dash },
    { key: "createdBy", header: "Set by", render: (row) => `${row.createdBy}, ${formatDateTime(row.createdAt)}` },
  ];

  return (
    <Card title="Cost rate history">
      <div className={styles.form}>
        <p className={styles.note}>
          {`Days are priced at the latest rate from a date on or before the day. Days before the first dated rate use the worker's own rate, ${fixed(worker.costRate, 2)} ${worker.currency}. Adding a rate changes no pay line: calculated days it prices must be recalculated before approval, and approved days keep their pay unless revised.`}
        </p>
        {state.error ? (
          <Alert tone="danger" title="Could not load the cost rates" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Cost rates"
            columns={columns}
            rows={state.data ?? []}
            getRowKey={(row) => row.id}
            loading={state.loading}
            empty={{ title: "No dated cost rates", description: "Every day is priced at the worker's own cost rate." }}
          />
        )}
      </div>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="worker-rate-from" label="Rate starts on" error={errors.from}>
          {(control) => <Input {...control} type="date" value={from} onChange={(event) => edit(() => setFrom(event.target.value))} invalid={Boolean(errors.from)} />}
        </FormField>
        <FormField id="worker-rate-amount" label={`New rate per hour (${worker.currency})`} error={errors.rate}>
          {(control) => <Input {...control} inputMode="decimal" value={rate} onChange={(event) => edit(() => setRate(event.target.value))} invalid={Boolean(errors.rate)} />}
        </FormField>
        <FormField id="worker-rate-note" label="Note" optional hint="For example the annual raise." error={errors.note}>
          {(control) => <Input {...control} maxLength={NOTE_MAX} value={note} onChange={(event) => edit(() => setNote(event.target.value))} invalid={Boolean(errors.note)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not set the cost rate" live="alert">
              {explain(failure)} ({failure.code})
            </Alert>
          </div>
        ) : null}
        {saved ? (
          <div className={styles.formActions}>
            <Alert tone="success" title="Cost rate added" live="status">
              Recalculate the calculated days from its date before approving them; approved days keep their pay.
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Add cost rate
          </Button>
        </div>
      </form>
    </Card>
  );
}
