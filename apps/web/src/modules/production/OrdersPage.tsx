import { useEffect, useRef, useState } from "react";
import { Alert, Badge, Button, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  STATUSES,
  describeProblem,
  listOrders,
  type OrderSummary,
  type Problem,
  type ProductionStatus,
} from "./api/client";
import { amount, dash, statusLabel, statusTone } from "./labels";
import styles from "./pages.module.css";

export interface ProductionOrdersPageProps {
  /** Opens an order's detail page. */
  onOpen?(order: OrderSummary): void;
  /** Opens the create form; the button is hidden without it. */
  onCreate?(): void;
}

interface ListState {
  rows: OrderSummary[];
  nextCursor?: string;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
}

const columns: DataTableColumn<OrderSummary>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "productId", header: "Product", mono: true },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "remaining", header: "Remaining", align: "end" },
  { key: "status", header: "Status", render: (row) => <Badge tone={statusTone[row.status]}>{statusLabel[row.status]}</Badge> },
  { key: "deliveryDate", header: "Delivery date", render: (row) => row.deliveryDate ?? dash },
  { key: "plannedCost", header: "Planned cost", align: "end", render: (row) => `${amount(row.plannedCost)} ${row.currency}` },
];

/** Production orders list (PRD 4.21.6): newest first, filtered by status, paged by cursor. */
export function ProductionOrdersPage({ onOpen, onCreate }: ProductionOrdersPageProps) {
  const [status, setStatus] = useState<ProductionStatus | "">("");
  const [state, setState] = useState<ListState>({ rows: [], loading: true, loadingMore: false, error: null });
  const latest = useRef(0);

  useEffect(() => {
    const token = ++latest.current;
    const controller = new AbortController();
    setState({ rows: [], loading: true, loadingMore: false, error: null });
    listOrders({ status, signal: controller.signal }).then(
      (page) => {
        if (token === latest.current) {
          setState({ rows: page.items ?? [], nextCursor: page.nextCursor, loading: false, loadingMore: false, error: null });
        }
      },
      (failure) => {
        if (token === latest.current) {
          setState({ rows: [], loading: false, loadingMore: false, error: describeProblem(failure) });
        }
      },
    );
    return () => controller.abort();
  }, [status]);

  const loadMore = async () => {
    const token = latest.current;
    setState((s) => ({ ...s, loadingMore: true }));
    try {
      const page = await listOrders({ status, cursor: state.nextCursor });
      if (token === latest.current) {
        setState((s) => ({ ...s, rows: [...s.rows, ...(page.items ?? [])], nextCursor: page.nextCursor, loadingMore: false }));
      }
    } catch (failure) {
      if (token === latest.current) {
        setState((s) => ({ ...s, loadingMore: false, error: describeProblem(failure) }));
      }
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Production orders"
        description="Create, estimate, release, start, report and end production orders."
        actions={
          onCreate ? (
            <Button variant="primary" icon="box" onClick={onCreate}>
              New production order
            </Button>
          ) : undefined
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="production-status-filter" label="Status">
            {(control) => (
              <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as ProductionStatus | "")}>
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load production orders" live="alert">
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      <DataTable
        label="Production orders"
        columns={columns}
        rows={state.rows}
        getRowKey={(row) => row.id}
        loading={state.loading}
        onRowOpen={onOpen}
        empty={{
          title: status ? `No ${statusLabel[status].toLowerCase()} orders` : "No production orders yet",
          description: status ? "Choose another status to see more orders." : "Orders you create appear here, newest first.",
        }}
      />
      {state.nextCursor && !state.loading ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={state.loadingMore}>
            Load more orders
          </Button>
        </div>
      ) : null}
    </div>
  );
}
