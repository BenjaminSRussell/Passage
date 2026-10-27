import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listOpenItems, type OpenItem } from "./api/client";
import { money, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const PAGE = 500;

const columns: DataTableColumn<OpenItem>[] = [
  { key: "documentNumber", header: "Document", mono: true },
  { key: "sourceType", header: "Type" },
  { key: "customerId", header: "Customer", mono: true, render: (row) => <span title={row.customerId}>{shortId(row.customerId)}</span> },
  { key: "dueDate", header: "Due", mono: true },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "openAmount", header: "Open", mono: true, align: "end", render: (row) => money(row.openAmount, row.currency) },
  {
    key: "cashDiscountAmount",
    header: "Discount",
    mono: true,
    align: "end",
    render: (row) => (row.cashDiscountDate ? `${money(row.cashDiscountAmount, row.currency)} by ${row.cashDiscountDate}` : "None"),
  },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
  { key: "dunningLevel", header: "Dunning level", mono: true, align: "end" },
  {
    key: "collections",
    header: "Collections",
    render: (row) =>
      row.status === "OPEN" || row.status === "PARTIALLY_SETTLED" ? (
        <span className={styles.actions}>
          <Link className={buttonClassName("ghost", "sm")} to={`/receivables/disputes/new?${collectionParams(row)}`} aria-label={`Dispute ${row.documentNumber}`}>
            Dispute
          </Link>
          <Link className={buttonClassName("ghost", "sm")} to={`/receivables/promises/new?${collectionParams(row)}`} aria-label={`Promise for ${row.documentNumber}`}>
            Promise
          </Link>
          <Link className={buttonClassName("ghost", "sm")} to={`/receivables/deductions/new?${collectionParams(row)}`} aria-label={`Deduction on ${row.documentNumber}`}>
            Deduction
          </Link>
        </span>
      ) : null,
  },
];

function collectionParams(row: OpenItem): string {
  return new URLSearchParams({ legalEntity: row.legalEntityId, openItem: row.id, amount: row.openAmount }).toString();
}

/** Open items (FR-30.1, FBL5N): what each customer owes, ordered by due date. */
export function OpenItemsPage() {
  const [customer, setCustomer] = useState("");
  const [status, setStatus] = useState<"open" | "all">("open");
  const customerValid = customer.trim() === "" || UUID.test(customer.trim());
  const filterKey = `${customerValid ? customer.trim() : ""}|${status}`;
  const state = useLoad((signal) => listOpenItems({ customer: customerValid ? customer.trim() : "", status, limit: PAGE }, signal), filterKey);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Open items"
        description="Invoices and other receivables with the amount still open. Settle them from a payment."
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/payments">
            Payments
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="oi-customer" label="Customer id" optional error={customerValid ? undefined : "Enter a customer id (a UUID)."}>
          {(c) => <Input {...c} className={styles.mono} value={customer} onChange={(e) => setCustomer(e.target.value)} invalid={!customerValid} />}
        </FormField>
        <FormField id="oi-status" label="Show">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as "open" | "all")}>
              <option value="open">Open and partly settled</option>
              <option value="all">All, including settled</option>
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the open items" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Open items"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No open items", description: "Items appear here once invoices post for the customer." }}
        />
      )}
      {state.data && state.data.length >= PAGE ? <p className={styles.note}>Showing the first {PAGE} items. Filter by customer to see more.</p> : null}
    </div>
  );
}
