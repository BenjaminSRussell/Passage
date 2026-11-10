import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { INVOICE_STATUSES, listCarrierInvoices, type CarrierInvoice, type InvoiceStatus } from "./api/client";
import { dash, formatDateTime, matchTone, money, shortId, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<CarrierInvoice>[] = [
  { key: "invoiceNo", header: "Invoice", mono: true },
  { key: "carrierId", header: "Carrier", mono: true, render: (row) => <span title={row.carrierId}>{shortId(row.carrierId)}</span> },
  { key: "invoiceDate", header: "Invoice date", mono: true },
  { key: "source", header: "Source", render: (row) => <Badge tone="neutral">{row.source}</Badge> },
  { key: "total", header: "Total", align: "end", render: (row) => money(row.total, row.currency) },
  { key: "varianceTotal", header: "Variance", align: "end", render: (row) => money(row.varianceTotal, row.currency) },
  { key: "matchStatus", header: "Match", render: (row) => <Badge tone={matchTone(row.matchStatus)}>{statusLabel(row.matchStatus)}</Badge> },
  {
    key: "freightBillId",
    header: "Freight bill",
    mono: true,
    render: (row) => (row.freightBillId ? <Link to={`/transport/freight-bills/${row.freightBillId}`}>{shortId(row.freightBillId)}</Link> : dash),
  },
  { key: "createdAt", header: "Registered", render: (row) => formatDateTime(row.createdAt) },
];

/** Carrier invoices as received; UNEXPECTED_INVOICE is the exception queue (BR-34.4, TMS-0109). */
export function CarrierInvoicesPage() {
  const [status, setStatus] = useState<InvoiceStatus | "">("");
  const state = useLoad((signal) => listCarrierInvoices({ matchStatus: status }, signal), `carrier-invoices:${status}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Carrier invoices"
        description="Invoices are matched to their freight bill line by line when registered. An invoice without a freight bill is an exception and is never approved automatically."
        actions={
          <Link className={buttonClassName("primary")} to="/transport/carrier-invoices/new">
            Register invoice
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="invoice-status" label="Match status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as InvoiceStatus | "")}>
              <option value="">All</option>
              {INVOICE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load carrier invoices" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Carrier invoices"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status === "" ? "No carrier invoices yet" : `No ${statusLabel(status).toLowerCase()} invoices`,
            description: "Invoices arrive by EDI 210 or the carrier API, or you register them here.",
          }}
        />
      )}
    </div>
  );
}
