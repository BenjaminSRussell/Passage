import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { describeProblem, HOLD_TYPES, listOrders, ORDER_STATUSES, type HoldType, type OrderStatus, type OrderSummary, type Problem } from "./api/client";
import { money, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

/** Active holds as badges: CREDIT and FRAUD block confirmation, so they show as danger. */
export function HoldBadges({ holds }: { holds: HoldType[] }) {
  if (holds.length === 0) return <span className={styles.muted}>None</span>;
  return (
    <span className={styles.holds}>
      {holds.map((h, i) => (
        <Badge key={`${h}-${i}`} tone={h === "CREDIT" || h === "FRAUD" ? "danger" : "warning"}>
          {sentence(h)}
        </Badge>
      ))}
    </span>
  );
}

const columns: DataTableColumn<OrderSummary>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "customerId", header: "Customer", render: (row) => <EntityName entity="customer" id={row.customerId} /> },
  { key: "orderType", header: "Type", render: (row) => sentence(row.orderType) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "activeHolds", header: "Holds", render: (row) => <HoldBadges holds={row.activeHolds} /> },
  { key: "requestedShipDate", header: "Requested ship", mono: true },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "netTotal", header: "Net", align: "end", render: (row) => money(row.netTotal, row.currency) },
];

interface OrderListProps {
  title: string;
  description: string;
  label: string;
  empty: { title: string; description: string };
}

/** One list of orders, newest first, with "Load more" over the service cursor. */
function OrderList({ title, description, label, empty }: OrderListProps) {
  const navigate = useNavigate();
  const [status, setStatus] = useState<OrderStatus | "">("");
  const [holdFilter, setHoldFilter] = useState<HoldType | "">("");
  const filterKey = `${status}|${holdFilter}`;
  const state = useLoad((signal) => listOrders({ status, hold: holdFilter }, signal), filterKey);
  const [more, setMore] = useState<OrderSummary[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listOrders({ status, hold: holdFilter, cursor });
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  const rows = [...(state.data?.items ?? []), ...more];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={title}
        description={description}
        actions={
          <Link className={buttonClassName("primary")} to="/sales/orders/new">
            New order
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id={`${label}-status`} label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as OrderStatus | "")}>
              <option value="">Any status</option>
              {ORDER_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id={`${label}-hold`} label="Active hold">
          {(control) => (
            <Select {...control} value={holdFilter} onChange={(e) => setHoldFilter(e.target.value as HoldType | "")}>
              <option value="">Any or none</option>
              {HOLD_TYPES.map((h) => (
                <option key={h} value={h}>
                  {sentence(h)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load orders" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label={label}
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={empty}
          onRowOpen={(row) => navigate(`/sales/orders/${row.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more orders" live="alert">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}
      {cursor && !state.error ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** Sales orders list (PRD §5.27.14 "Sales orders list"). */
export function OrdersPage() {
  return (
    <OrderList
      title="Sales orders"
      description="Every order in your legal entities, newest first. Open an order to confirm it or release its holds."
      label="Sales orders"
      empty={{ title: "No sales orders yet", description: "Orders you enter or receive through a channel appear here." }}
    />
  );
}
