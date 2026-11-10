import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listCorrections, type Correction } from "./api/client";
import { addDays, browserZone, dash, daysBetween, formatDateTime, formatHours, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const MAX_DAYS = 31;

const columns: DataTableColumn<Correction>[] = [
  { key: "correctedAt", header: "Corrected", mono: true, render: (row) => formatDateTime(row.correctedAt) },
  { key: "workerId", header: "Worker", render: (row) => <Link to={`/shopfloor/workers/${row.workerId}`}>Open worker</Link> },
  { key: "old", header: "Before", render: (row) => `${formatDateTime(row.oldStartedAt)} to ${formatDateTime(row.oldStoppedAt)}` },
  { key: "new", header: "After", render: (row) => `${formatDateTime(row.newStartedAt)} to ${formatDateTime(row.newStoppedAt)}` },
  { key: "hours", header: "Hours", align: "end", render: (row) => (row.newHours ? formatHours(row.newHours) : dash) },
  { key: "reason", header: "Reason" },
  { key: "correctedBy", header: "By" },
];

/** Supervisor corrections of registrations, newest first (FR-25.8 audit trail). */
export function CorrectionsPage() {
  const [from, setFrom] = useState(addDays(today(), -6));
  const [to, setTo] = useState(today());
  const span = daysBetween(from, to);
  const rangeError =
    span === null ? "Enter both dates." : span < 1 ? "To must not be before From." : span > MAX_DAYS ? `Show at most ${MAX_DAYS} days.` : null;
  const state = useLoad((signal) => (rangeError ? Promise.resolve([] as Correction[]) : listCorrections({ from, to, timeZone: browserZone() }, signal)), `${from}|${to}|${rangeError}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Time corrections"
        description="Every supervisor change to a registration, with the times before and after and the reason. Open a worker to correct a registration."
      />
      <div className={styles.toolbar}>
        <FormField id="corrections-from" label="From">
          {(control) => <Input {...control} type="date" value={from} onChange={(event) => setFrom(event.target.value)} />}
        </FormField>
        <FormField id="corrections-to" label="To" error={rangeError ?? undefined}>
          {(control) => <Input {...control} type="date" value={to} onChange={(event) => setTo(event.target.value)} invalid={Boolean(rangeError)} />}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the corrections" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <Card title="Corrections">
          <DataTable
            label="Corrections"
            columns={columns}
            rows={state.data ?? []}
            getRowKey={(row) => row.id}
            loading={state.loading}
            empty={{ title: "No corrections in this range", description: "Corrected registrations appear here with their reason." }}
          />
        </Card>
      )}
    </div>
  );
}
