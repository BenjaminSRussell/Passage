import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listInvoices, type InvoiceStatus, type InvoiceSummary } from "./api/client";
import { invoiceName, STATUS_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const LIMIT = 100;
const STATUSES: InvoiceStatus[] = ["PROPOSAL", "POSTED"];

const columns: DataTableColumn<InvoiceSummary>[] = [
  { key: "number", header: "Credit note", mono: true, render: (row) => invoiceName(row) },
  { key: "originalNumber", header: "Original invoice", mono: true, render: (row) => row.originalNumber || "—" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={STATUS_LABELS[row.status] ?? row.status} /> },
  { key: "invoiceDate", header: "Credit date", mono: true },
  { key: "customerId", header: "Customer", mono: true },
  { key: "gross", header: "Gross", mono: true, align: "end", render: (row) => `${row.totals.gross} ${row.currency}` },
];

/** Credit notes inquiry (FR-29.5): proposals and posted credit notes, newest first. */
export function CreditNotesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<InvoiceStatus | "">("");
  const state = useLoad((signal) => listInvoices({ type: "CREDIT_NOTE", status, limit: LIMIT }, signal), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Credit notes"
        description="Credits against posted invoices. To raise one, open the posted invoice and choose Credit invoice."
        actions={
          <Link className={buttonClassName("secondary")} to="/billing">
            All invoices
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="cn-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as InvoiceStatus | "")}>
              <option value="">Any status</option>
              {STATUSES.map((s) => (
                <option key={s} value={s}>
                  {STATUS_LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the credit notes" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Credit notes"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status ? `No ${STATUS_LABELS[status].toLowerCase()} credit notes` : "No credit notes yet",
            description: "A credit note reverses all or part of a posted invoice, its tax included.",
          }}
          onRowOpen={(row) => navigate(`/billing/invoices/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= LIMIT ? <p className={styles.note}>{`Showing the newest ${LIMIT} credit notes.`}</p> : null}
    </div>
  );
}
