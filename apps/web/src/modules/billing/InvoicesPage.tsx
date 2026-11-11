import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, entityText, ListPage, StatusPill, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { BillingApiError, INVOICE_STATUSES, listInvoicePage, type InvoiceStatus, type InvoiceSummary, type InvoiceType } from "./api/client";
import { invoiceName, STATUS_LABELS, TYPE_LABELS } from "./format";
import { usePaged } from "./usePaged";
import { EntityName } from "../../shell/lookup/EntityName";

const LIMIT = 100;
/** The invoice types the list filters by; credit notes have their own list. */
const LIST_TYPES: InvoiceType[] = ["FREE_TEXT", "SHIPMENT", "CONSOLIDATED"];

const typeText = (row: InvoiceSummary) => TYPE_LABELS[row.type] ?? row.type;
const statusText = (row: InvoiceSummary) => STATUS_LABELS[row.status] ?? row.status;

const columns: ListColumn<InvoiceSummary>[] = [
  { key: "number", header: "Invoice", mono: true, render: (row) => invoiceName(row), sortValue: (row) => invoiceName(row), csv: (row) => invoiceName(row) },
  { key: "type", header: "Type", render: typeText, sortValue: typeText, csv: typeText },
  { key: "status", header: "Status", render: (row) => <StatusPill status={statusText(row)} />, sortValue: statusText, csv: statusText },
  { key: "invoiceDate", header: "Invoice date", mono: true },
  { key: "dueDate", header: "Due", mono: true },
  {
    key: "customerId",
    header: "Customer",
    sortable: false,
    render: (row) => <EntityName entity="customer" id={row.customerId} />,
    csv: (row) => entityText("customer", row.customerId),
  },
  {
    key: "gross",
    header: "Gross",
    mono: true,
    align: "end",
    render: (row) => `${row.totals.gross} ${row.currency}`,
    sortValue: (row) => row.totals.gross,
    csv: (row) => row.totals.gross,
    csvWith: "currency",
  },
  { key: "currency", header: "Currency", defaultHidden: true },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: INVOICE_STATUSES.map((s) => ({ value: s, label: STATUS_LABELS[s] })) },
  { kind: "select", key: "type", label: "Type", anyLabel: "Any type", options: LIST_TYPES.map((t) => ({ value: t, label: TYPE_LABELS[t] })) },
  { kind: "entity", key: "customerId", label: "Customer", entity: "customer" },
];

/** Invoices newest first over the service cursor; status, type and customer filter on the service. */
function useInvoiceRows(query: ListQuery): ListData<InvoiceSummary> {
  const status = (query.filters.status ?? "") as InvoiceStatus | "";
  const type = (query.filters.type ?? "") as InvoiceType | "";
  const customerId = query.filters.customerId ?? "";
  const [forbidden, setForbidden] = useState(false);
  const state = usePaged(async (cursor, signal) => {
    try {
      const page = await listInvoicePage({ status, type, customerId: customerId || undefined, limit: LIMIT, cursor }, signal);
      if (cursor === undefined && !signal?.aborted) setForbidden(false);
      return page;
    } catch (error) {
      if (cursor === undefined && !signal?.aborted) setForbidden(error instanceof BillingApiError && error.status === 403);
      throw error;
    }
  }, `${status}|${type}|${customerId}`);
  return {
    rows: state.error ? [] : state.rows,
    loading: state.loading,
    loadingMore: state.loadingMore,
    error: state.error ?? state.moreError ?? undefined,
    forbidden,
    hasMore: state.hasMore && !state.moreError,
    loadMore: state.loadMore,
    retry: state.error ? state.reload : state.loadMore,
  };
}

/** Invoices list inquiry (§5.29.14): proposals and posted invoices, newest first, paged by the service cursor. */
export function InvoicesPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="billing-invoices"
      title="Invoices"
      description="Invoice proposals and posted invoices, from shipments and free text. Open a proposal to review its tax and post it."
      noun="invoices"
      singular="invoice"
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
      columns={columns}
      filters={filters}
      useRows={useInvoiceRows}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/billing/invoices/${row.id}`)}
      empty={{
        title: "No invoices yet",
        description: "Invoice the shipments of a customer's posted packing slips, or create a free-text invoice for services or other charges.",
        action: (
          <Link className={buttonClassName("secondary")} to="/billing/proposals/new">
            Invoice posted packing slips
          </Link>
        ),
      }}
    />
  );
}
