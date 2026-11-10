import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listPayments, PAYMENT_STATUSES, type Payment, type PaymentStatus } from "./api/client";
import { MODE_LABELS, money, STATUS_LABELS, STATUS_TONES } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const PAGE = 500;

const columns: DataTableColumn<Payment>[] = [
  { key: "receivedDate", header: "Received", mono: true },
  { key: "paymentMode", header: "Mode", render: (row) => MODE_LABELS[row.paymentMode] ?? row.paymentMode },
  { key: "sourceRef", header: "Reference", mono: true, render: (row) => row.sourceRef ?? "None" },
  { key: "customerId", header: "Customer", render: (row) => <EntityName entity="customer" id={row.customerId ?? ""} fallback="Unassigned" /> },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "unappliedAmount", header: "Unapplied", mono: true, align: "end", render: (row) => money(row.unappliedAmount, row.currency) },
  { key: "status", header: "Status", render: (row) => <Badge tone={STATUS_TONES[row.status] ?? "neutral"}>{STATUS_LABELS[row.status] ?? row.status}</Badge> },
];

/** Customer payments (FR-30.2): received, applied and on account. Opening one leads to cash application. */
export function PaymentsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<PaymentStatus | "">("");
  const state = useLoad((signal) => listPayments({ status, limit: PAGE }, signal), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Payments"
        description="Customer payments, newest first. Open a payment to match it to open items and apply it."
        actions={
          <Link className={buttonClassName("primary")} to="/receivables/payments/new">
            Record payment
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="pay-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as PaymentStatus | "")}>
              <option value="">All statuses</option>
              {PAYMENT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {STATUS_LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the payments" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Payments"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No payments", description: "Record a check, ACH, wire, card or cash payment to start cash application." }}
          onRowOpen={(row) => navigate(`/receivables/payments/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= PAGE ? <p className={styles.note}>Showing the newest {PAGE} payments.</p> : null}
    </div>
  );
}
