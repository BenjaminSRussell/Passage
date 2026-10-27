import { Alert, Button, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listPrinterStatuses, type PrinterStatus } from "./api/print";
import { dash, formatDateTime, PrinterStateBadge } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<PrinterStatus>[] = [
  { key: "code", header: "Printer", mono: true },
  { key: "queueCode", header: "Queue", mono: true },
  { key: "state", header: "State", render: (row) => <PrinterStateBadge state={row.state} /> },
  { key: "queueDepth", header: "Queue depth", align: "end", render: (row) => String(row.queueDepth) },
  { key: "lastError", header: "Last error", mono: true, render: (row) => row.lastError || dash },
  { key: "lastJobAt", header: "Last job", render: (row) => formatDateTime(row.lastJobAt) },
];

/** Printer status report (§6.32.14): online state, queue depth and last error per printer. */
export function PrinterStatusPage() {
  const state = useLoad((signal) => listPrinterStatuses(signal), "printer-status");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Printer status"
        description="The state comes from the last job each printer handled: Ready after a print, Error after MOB-0111, Busy while a job is still queued, Idle before its first job. Queue depth counts jobs still waiting on the printer's queue."
        actions={<Button onClick={state.reload}>Refresh</Button>}
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load printer status" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Printer status"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.printerId}
          loading={state.loading}
          empty={{ title: "No printers yet", description: "Register a printer to see its status here." }}
        />
      )}
    </div>
  );
}
