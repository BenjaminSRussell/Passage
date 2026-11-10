import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { INVOICE_STATUSES, listInvoicePage, type InvoiceStatus, type InvoiceSummary, type InvoiceType } from "./api/client";
import { invoiceName, STATUS_LABELS, TYPE_LABELS } from "./format";
import { MoreRows } from "./MoreRows";
import styles from "./pages.module.css";
import { usePaged } from "./usePaged";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const LIMIT = 100;
/** The invoice types the list filters by; credit notes have their own list. */
const LIST_TYPES: InvoiceType[] = ["FREE_TEXT", "SHIPMENT", "CONSOLIDATED"];

const columns: DataTableColumn<InvoiceSummary>[] = [
  { key: "number", header: "Invoice", mono: true, render: (row) => invoiceName(row) },
  { key: "type", header: "Type", render: (row) => TYPE_LABELS[row.type] ?? row.type },
  { key: "status", header: "Status", render: (row) => <StatusPill status={STATUS_LABELS[row.status] ?? row.status} /> },
  { key: "invoiceDate", header: "Invoice date", mono: true },
  { key: "dueDate", header: "Due", mono: true },
  { key: "customerId", header: "Customer", render: (row) => <EntityName entity="customer" id={row.customerId} /> },
  { key: "gross", header: "Gross", mono: true, align: "end", render: (row) => `${row.totals.gross} ${row.currency}` },
];

/** Invoices list inquiry (§5.29.14): proposals and posted invoices, newest first, paged by the service cursor. */
export function InvoicesPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<InvoiceStatus | "">("");
  const [type, setType] = useState<InvoiceType | "">("");
  const filterWords = [status && STATUS_LABELS[status].toLowerCase(), type && TYPE_LABELS[type].toLowerCase()].filter(Boolean).join(" ");
  const state = usePaged((cursor, signal) => listInvoicePage({ status, type, limit: LIMIT, cursor }, signal), `${status}|${type}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Invoices"
        description="Invoice proposals and posted invoices, from shipments and free text. Open a proposal to review its tax and post it."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/billing/revenue-allocation">
              Revenue allocation
            </Link>
            <Link className={buttonClassName("secondary")} to="/billing/free-text-invoices/new">
              New free-text invoice
            </Link>
            <Link className={buttonClassName("primary")} to="/billing/proposals/new">
              Invoice shipments
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="inv-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as InvoiceStatus | "")}>
              <option value="">Any status</option>
              {INVOICE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {STATUS_LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="inv-type" label="Type">
          {(c) => (
            <Select {...c} value={type} onChange={(e) => setType(e.target.value as InvoiceType | "")}>
              <option value="">Any type</option>
              {LIST_TYPES.map((t) => (
                <option key={t} value={t}>
                  {TYPE_LABELS[t]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the invoices" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Invoices"
          columns={columns}
          rows={state.rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: filterWords ? `No ${filterWords} invoices` : "No invoices yet",
            description: "Invoice the shipments of a customer's posted packing slips, or create a free-text invoice for services or other charges.",
          }}
          onRowOpen={(row) => navigate(`/billing/invoices/${row.id}`)}
        />
      )}
      <MoreRows state={state} noun="invoices" />
    </div>
  );
}
