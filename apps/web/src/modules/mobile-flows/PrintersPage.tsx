import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, ConfirmDialog, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listPrinters, mediaLabel, printsOn, retirePrinter, type Printer, type Problem } from "./api/print";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const DEVICE_LABELS: Record<string, string> = { ZPL_II: "ZPL II", PDF_IPP: "PDF over IPP", PCL_6: "PCL 6" };

function columnsFor(onRetire: (printer: Printer) => void): DataTableColumn<Printer>[] {
  return [
  { key: "code", header: "Printer", mono: true },
  { key: "name", header: "Name" },
  { key: "deviceType", header: "Device type", render: (row) => DEVICE_LABELS[row.deviceType] ?? row.deviceType },
  { key: "dpi", header: "Resolution", align: "end", render: (row) => `${row.dpi} dpi` },
  { key: "mediaSize", header: "Media", render: (row) => mediaLabel(row.mediaSize) },
  { key: "prints", header: "Prints", render: (row) => printsOn(row.deviceType, row.mediaSize) },
  { key: "host", header: "Address", mono: true, render: (row) => `${row.host}:${row.port}` },
  { key: "queueCode", header: "Queue", mono: true },
  { key: "failoverRank", header: "Failover rank", align: "end", render: (row) => String(row.failoverRank) },
  { key: "createdAt", header: "Registered", render: (row) => formatDateTime(row.createdAt) },
  {
    key: "actions",
    header: "Actions",
    render: (row) => (
      <span className={styles.rowActions}>
        <Link className={buttonClassName("ghost", "sm")} to={`/mobile-flows/printers/${row.id}/edit`} aria-label={`Edit ${row.code}`}>
          Edit
        </Link>
        <Button size="sm" variant="ghost" onClick={() => onRetire(row)} aria-label={`Retire ${row.code}`}>
          Retire
        </Button>
      </span>
    ),
  },
  ];
}

/** Output devices by queue (FR-32.13). A job tries the queue's devices in failover-rank order. */
export function PrintersPage() {
  const state = useLoad((signal) => listPrinters(signal), "printers");
  const [retiring, setRetiring] = useState<Printer | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function confirmRetire() {
    const printer = retiring;
    setRetiring(null);
    if (!printer) return;
    try {
      await retirePrinter(printer.id);
      setFailure(null);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    }
  }
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Printers"
        description="Label and document printers on the site network, grouped into queues. A job tries each device of its queue in failover-rank order, twice by default (the site’s PRINT_RETRY setting), before moving to the next."
        actions={
          <Link className={buttonClassName("primary")} to="/mobile-flows/printers/new">
            Register printer
          </Link>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not retire the printer" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {retiring ? (
        <ConfirmDialog
          title={`Retire ${retiring.code}?`}
          description={`${retiring.code} leaves queue ${retiring.queueCode} and no job is sent to it again. Its past jobs stay in the job log.`}
          confirmLabel="Retire printer"
          cancelLabel="Keep printer"
          onConfirm={() => void confirmRetire()}
          onCancel={() => setRetiring(null)}
        />
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load printers" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Printers"
          columns={columnsFor(setRetiring)}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No printers yet", description: "Register a printer and give it a queue before routing labels to it." }}
        />
      )}
    </div>
  );
}
