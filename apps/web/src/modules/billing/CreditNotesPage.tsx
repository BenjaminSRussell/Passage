import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, entityText, ListPage, StatusPill, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { BillingApiError, listInvoicePage, type InvoiceStatus, type InvoiceSummary } from "./api/client";
import { invoiceName, STATUS_LABELS } from "./format";
import { usePaged } from "./usePaged";
import { EntityName } from "../../shell/lookup/EntityName";

const LIMIT = 100;
const STATUSES: InvoiceStatus[] = ["PROPOSAL", "POSTED"];

const statusText = (row: InvoiceSummary) => STATUS_LABELS[row.status] ?? row.status;

const columns: ListColumn<InvoiceSummary>[] = [
  { key: "number", header: "Credit note", mono: true, render: (row) => invoiceName(row), sortValue: (row) => invoiceName(row), csv: (row) => invoiceName(row) },
  { key: "originalNumber", header: "Original invoice", mono: true, render: (row) => row.originalNumber || "—", csv: (row) => row.originalNumber ?? "" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={statusText(row)} />, sortValue: statusText, csv: statusText },
  { key: "invoiceDate", header: "Credit date", mono: true },
  { key: "customerId", header: "Customer", sortable: false, render: (row) => <EntityName entity="customer" id={row.customerId} />, csv: (row) => entityText("customer", row.customerId) },
  { key: "gross", header: "Gross", mono: true, align: "end", render: (row) => `${row.totals.gross} ${row.currency}`, sortValue: (row) => row.totals.gross, csv: (row) => row.totals.gross, csvWith: "currency" },
  { key: "currency", header: "Currency", defaultHidden: true },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: STATUSES.map((s) => ({ value: s, label: STATUS_LABELS[s] })) },
];

/** Credit notes newest first over the service cursor; the status filters on the service. */
function useCreditNoteRows(query: ListQuery): ListData<InvoiceSummary> {
  const status = (query.filters.status ?? "") as InvoiceStatus | "";
  const [forbidden, setForbidden] = useState(false);
  const state = usePaged(async (cursor, signal) => {
    try {
      const page = await listInvoicePage({ type: "CREDIT_NOTE", status, limit: LIMIT, cursor }, signal);
      if (cursor === undefined && !signal?.aborted) setForbidden(false);
      return page;
    } catch (error) {
      if (cursor === undefined && !signal?.aborted) setForbidden(error instanceof BillingApiError && error.status === 403);
      throw error;
    }
  }, status);
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

/** Credit notes inquiry (FR-29.5): proposals and posted credit notes, newest first. */
export function CreditNotesPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="billing-credit-notes"
      title="Credit notes"
      description="Credits against posted invoices. To raise one, open the posted invoice and choose Credit invoice."
      noun="credit notes"
      singular="credit note"
      actions={
        <Link className={buttonClassName("secondary")} to="/billing">
          All invoices
        </Link>
      }
      columns={columns}
      filters={filters}
      useRows={useCreditNoteRows}
      search={(row) => ({ number: invoiceName(row) })}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/billing/invoices/${row.id}`)}
      empty={{ title: "No credit notes yet", description: "A credit note reverses all or part of a posted invoice, its tax included." }}
    />
  );
}
