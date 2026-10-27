import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listPrinters, type Printer } from "./api/print";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const DEVICE_LABELS: Record<string, string> = { ZPL_II: "ZPL II", PDF_IPP: "PDF over IPP", PCL_6: "PCL 6" };

const columns: DataTableColumn<Printer>[] = [
  { key: "code", header: "Printer", mono: true },
  { key: "name", header: "Name" },
  { key: "deviceType", header: "Device type", render: (row) => DEVICE_LABELS[row.deviceType] ?? row.deviceType },
  { key: "dpi", header: "Resolution", align: "end", render: (row) => `${row.dpi} dpi` },
  { key: "host", header: "Address", mono: true, render: (row) => `${row.host}:${row.port}` },
  { key: "queueCode", header: "Queue", mono: true },
  { key: "failoverRank", header: "Failover rank", align: "end", render: (row) => String(row.failoverRank) },
  { key: "createdAt", header: "Registered", render: (row) => formatDateTime(row.createdAt) },
];

/** Output devices by queue (FR-32.13). A job tries the queue's devices in failover-rank order. */
export function PrintersPage() {
  const state = useLoad((signal) => listPrinters(signal), "printers");
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
      {state.error ? (
        <Alert tone="danger" title="Could not load printers" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Printers"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No printers yet", description: "Register a printer and give it a queue before routing labels to it." }}
        />
      )}
    </div>
  );
}
