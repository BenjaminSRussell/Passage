import { useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, exportPay, listPayExports, MAX_EXPORT_RANGE_DAYS, newKey, type PayExport, type Problem } from "./api/client";
import { addDays, daysBetween, formatDateTime, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<PayExport>[] = [
  { key: "batchId", header: "Batch", mono: true, render: (row) => row.batchId.slice(0, 8) },
  { key: "range", header: "Work dates", render: (row) => `${row.fromDate} to ${row.toDate}` },
  { key: "lineCount", header: "Pay lines", align: "end" },
  { key: "createdBy", header: "Exported by" },
  { key: "createdAt", header: "Exported", render: (row) => formatDateTime(row.createdAt) },
];

/** Payroll export batches of approved pay lines (FR-25.8; consumed by payroll, wave 43). */
export function PayExportsPage() {
  const navigate = useNavigate();
  const [from, setFrom] = useState(addDays(today(), -6));
  const [to, setTo] = useState(today());
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const key = useRef(newKey());
  const state = useLoad((signal) => listPayExports(signal), "pay-exports");

  const span = daysBetween(from, to);
  const rangeError =
    span === null ? "Enter both dates." : span < 1 ? "To must not be before From." : span > MAX_EXPORT_RANGE_DAYS ? `Use at most ${MAX_EXPORT_RANGE_DAYS} days.` : null;

  function change(set: (v: string) => void, value: string) {
    set(value);
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (rangeError) return;
    setFailure(null);
    setBusy(true);
    try {
      const batch = await exportPay({ from, to }, key.current);
      key.current = newKey();
      navigate(`/shopfloor/pay-exports/${batch.batchId}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      // Keep the key so a retry after a lost response returns the same batch.
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Payroll export"
        description="Each export takes every approved pay line in the date range that is not in an earlier batch. A pay line is exported once."
      />
      <Card title="New export">
        <form className={styles.toolbar} onSubmit={submit} noValidate>
          <FormField id="export-from" label="From">
            {(control) => <Input {...control} type="date" value={from} onChange={(event) => change(setFrom, event.target.value)} />}
          </FormField>
          <FormField id="export-to" label="To" error={rangeError ?? undefined}>
            {(control) => <Input {...control} type="date" value={to} invalid={Boolean(rangeError)} onChange={(event) => change(setTo, event.target.value)} />}
          </FormField>
          <Button type="submit" variant="primary" loading={busy} disabled={Boolean(rangeError)}>
            Export approved pay
          </Button>
        </form>
      </Card>
      {failure ? (
        <Alert tone={failure.code === "SF-0111" ? "info" : "danger"} title={failure.code === "SF-0111" ? "Nothing to export" : "Could not export"} live="alert">
          {failure.code === "SF-0111"
            ? `Every approved pay line from ${from} to ${to} is already in a batch, or no day in the range is approved yet.`
            : `${failure.message} (${failure.code})`}
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load export batches" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Export batches"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.batchId}
          loading={state.loading}
          empty={{ title: "No exports yet", description: "Approve worker-days on the time approval page, then export them here." }}
          onRowOpen={(row) => navigate(`/shopfloor/pay-exports/${row.batchId}`)}
        />
      )}
    </div>
  );
}
