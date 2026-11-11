import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Badge, buttonClassName, entityText, ListPage, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { listPayments, PAYMENT_STATUSES, ReceivablesApiError, type Payment, type PaymentStatus } from "./api/client";
import { MODE_LABELS, money, STATUS_LABELS, STATUS_TONES } from "./format";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";

/** The service's largest page; the list shows the newest payments up to it. */
const PAGE = 500;

const modeText = (row: Payment) => MODE_LABELS[row.paymentMode] ?? row.paymentMode;
const statusText = (row: Payment) => STATUS_LABELS[row.status] ?? row.status;

const columns: ListColumn<Payment>[] = [
  { key: "receivedDate", header: "Received", mono: true },
  { key: "paymentMode", header: "Mode", render: modeText, sortValue: modeText, csv: modeText },
  { key: "sourceRef", header: "Reference", mono: true, render: (row) => row.sourceRef ?? "None", csv: (row) => row.sourceRef ?? "" },
  {
    key: "customerId",
    header: "Customer",
    sortable: false,
    render: (row) => <EntityName entity="customer" id={row.customerId ?? ""} fallback="Unassigned" />,
    csv: (row) => (row.customerId ? entityText("customer", row.customerId) : "Unassigned"),
  },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency), csv: (row) => row.amount, csvWith: "currency" },
  {
    key: "unappliedAmount",
    header: "Unapplied",
    mono: true,
    align: "end",
    render: (row) => money(row.unappliedAmount, row.currency),
    csv: (row) => row.unappliedAmount,
    csvWith: "currency",
  },
  { key: "currency", header: "Currency", defaultHidden: true },
  {
    key: "status",
    header: "Status",
    render: (row) => <Badge tone={STATUS_TONES[row.status] ?? "neutral"}>{statusText(row)}</Badge>,
    sortValue: statusText,
    csv: statusText,
  },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: PAYMENT_STATUSES.map((s) => ({ value: s, label: STATUS_LABELS[s] })) },
  { kind: "entity", key: "customer", label: "Customer", entity: "customer" },
];

/** The newest payments of one status and customer; the service answers a single page. */
function usePaymentRows(query: ListQuery): ListData<Payment> {
  const status = (query.filters.status ?? "") as PaymentStatus | "";
  const customer = query.filters.customer ?? "";
  const [forbidden, setForbidden] = useState(false);
  const state = useLoad(async (signal) => {
    try {
      const rows = await listPayments({ status, customer: customer || undefined, limit: PAGE }, signal);
      if (!signal.aborted) setForbidden(false);
      return rows;
    } catch (error) {
      if (!signal.aborted) setForbidden(error instanceof ReceivablesApiError && error.status === 403);
      throw error;
    }
  }, `${status}|${customer}`);
  return {
    rows: state.error ? [] : (state.data ?? []),
    loading: state.loading,
    loadingMore: false,
    error: state.error ?? undefined,
    forbidden,
    hasMore: false,
    loadMore: () => undefined,
    retry: state.reload,
  };
}

/** Customer payments (FR-30.2): received, applied and on account. Opening one leads to cash application. */
export function PaymentsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="receivables-payments"
      title="Payments"
      description={`Customer payments, newest first, up to the newest ${PAGE}. Open a payment to match it to open items and apply it.`}
      noun="payments"
      singular="payment"
      actions={
        <Link className={buttonClassName("primary")} to="/receivables/payments/new">
          Record payment
        </Link>
      }
      columns={columns}
      filters={filters}
      useRows={usePaymentRows}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/receivables/payments/${row.id}`)}
      empty={{
        title: "No payments",
        description: "Record a check, ACH, wire, card or cash payment to start cash application.",
        action: (
          <Link className={buttonClassName("secondary")} to="/receivables/payments/new">
            Record the first payment
          </Link>
        ),
      }}
    />
  );
}
