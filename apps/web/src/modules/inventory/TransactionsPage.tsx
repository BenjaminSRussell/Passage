import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listTransactions,
  SOURCE_TYPES,
  TRANSACTION_STATUSES,
  type Page,
  type Problem,
  type SourceType,
  type Transaction,
  type TransactionQuery,
  type TransactionStatus,
} from "./api/client";
import { dash, describeDimensions, sentence, shortId, UUID } from "./format";
import { ALL_DIMENSIONS, money, ProblemAlert } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const sourceLabels: Record<SourceType, string> = {
  INVENT_JOURNAL_LINE: "Journal line",
  BLOCKING: "Blocking",
  TRANSFER_LINE: "Transfer order",
  CONSIGNMENT_LINE: "Consignment receipt",
  OWNERSHIP_CHANGE: "Ownership change",
  PURCH_LINE: "Purchase order line",
  RECEIPT_LINE: "Receipt line",
  SALES_LINE: "Sales order line",
  PROD_LINE: "Production order line",
};

function origin(row: Transaction) {
  if (row.journalId) {
    return <Link to={`/inventory/journals/${row.journalId}`}>Journal {shortId(row.journalId)}</Link>;
  }
  if (row.sourceType === "TRANSFER_LINE" && row.reference) {
    return <Link to={`/inventory/transfer-orders/${row.reference}`}>Transfer order {shortId(row.reference)}</Link>;
  }
  if (row.sourceType === "CONSIGNMENT_LINE" && row.reference) {
    return <Link to={`/inventory/consignment-orders/${row.reference}`}>Replenishment order {shortId(row.reference)}</Link>;
  }
  return `${sourceLabels[row.sourceType] ?? row.sourceType}${row.reference ? ` ${shortId(row.reference)}` : ""}`;
}

const columns: DataTableColumn<Transaction>[] = [
  {
    key: "id",
    header: "Transaction",
    mono: true,
    render: (row) => (
      <Link to={`/inventory/transactions/${row.id}`} aria-label={`Transaction ${shortId(row.id)}`}>
        {shortId(row.id)}
      </Link>
    ),
  },
  { key: "physicalDate", header: "Physical date", mono: true },
  { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "qty", header: "Quantity", align: "end", mono: true, render: (row) => `${row.qty} ${row.enteredUnit}` },
  {
    key: "status",
    header: "Status",
    render: (row) => {
      const status = row.issueStatus ?? row.receiptStatus;
      return status ? <Badge tone={row.issueStatus ? "warning" : "success"}>{`${row.issueStatus ? "Issue" : "Receipt"} · ${sentence(status)}`}</Badge> : dash;
    },
  },
  { key: "costAmountPosted", header: "Cost amount", align: "end", mono: true, render: (row) => money(row.costAmountPosted ?? row.costAmountPhysical, row.currency) },
  { key: "origin", header: "Origin", render: origin },
];

interface Filters {
  product: string;
  status: TransactionStatus | "";
  sourceType: SourceType | "";
  from: string;
  to: string;
}

const noFilters: Filters = { product: "", status: "", sourceType: "", from: "", to: "" };

/** Transaction inquiry (FR-16.13): every movement with its status, value and origin, newest first. */
export function TransactionsPage() {
  const [draft, setDraft] = useState<Filters>(noFilters);
  const [filters, setFilters] = useState<Filters>(noFilters);
  const [errors, setErrors] = useState<Partial<Record<keyof Filters, string>>>({});
  const key = JSON.stringify(filters);
  const state = useLoad((signal) => listTransactions(filters as TransactionQuery, signal), key);
  const [more, setMore] = useState<{ key: string; page: Page<Transaction> } | null>(null);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  const extra = more && more.key === key ? more.page : null;
  const rows = [...(state.data?.items ?? []), ...(extra?.items ?? [])];
  const nextCursor = extra ? extra.nextCursor : state.data?.nextCursor;

  function apply(event: FormEvent) {
    event.preventDefault();
    const found: Partial<Record<keyof Filters, string>> = {};
    if (draft.product.trim() !== "" && !UUID.test(draft.product.trim())) found.product = "Enter the product id (a UUID).";
    if (draft.from && draft.to && draft.to < draft.from) found.to = "The end date is before the start date.";
    setErrors(found);
    if (Object.keys(found).length === 0) setFilters({ ...draft, product: draft.product.trim() });
  }

  async function loadMore() {
    if (!nextCursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listTransactions({ ...filters, cursor: nextCursor } as TransactionQuery);
      setMore({ key, page: { items: [...(extra?.items ?? []), ...page.items], nextCursor: page.nextCursor } });
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  const set = (name: keyof Filters) => (event: { target: { value: string } }) => setDraft({ ...draft, [name]: event.target.value });
  const failure = state.error ?? moreError;
  return (
    <div className={styles.stack}>
      <PageHeader title="Inventory transactions" description="Every posted movement with its status, physical date, value and the document behind it." />
      <Card title="Filter">
        <form onSubmit={apply} noValidate className={styles.form}>
          <FormField id="txn-product" label="Product" optional error={errors.product}>
            {(control) => <Input {...control} value={draft.product} onChange={set("product")} placeholder="Product id" invalid={Boolean(errors.product)} />}
          </FormField>
          <FormField id="txn-status" label="Status" optional>
            {(control) => (
              <Select {...control} value={draft.status} onChange={set("status")}>
                <option value="">Any status</option>
                {TRANSACTION_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {sentence(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="txn-source" label="Origin" optional>
            {(control) => (
              <Select {...control} value={draft.sourceType} onChange={set("sourceType")}>
                <option value="">Any origin</option>
                {SOURCE_TYPES.map((s) => (
                  <option key={s} value={s}>
                    {sourceLabels[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="txn-from" label="From" optional>
            {(control) => <Input {...control} type="date" value={draft.from} onChange={set("from")} />}
          </FormField>
          <FormField id="txn-to" label="To" optional error={errors.to}>
            {(control) => <Input {...control} type="date" value={draft.to} onChange={set("to")} invalid={Boolean(errors.to)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filters
            </Button>
            <Button
              onClick={() => {
                setErrors({});
                setDraft(noFilters);
                setFilters(noFilters);
              }}
            >
              Clear filters
            </Button>
          </div>
        </form>
      </Card>
      {failure ? <ProblemAlert title="Could not load transactions" problem={failure} onRetry={state.reload} /> : null}
      {state.error ? null : (
        <DataTable
          label="Transactions"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No transactions match", description: "Post a journal or block stock, or widen the filters." }}
        />
      )}
      {nextCursor && !state.loading ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more transactions
          </Button>
        </div>
      ) : null}
    </div>
  );
}
