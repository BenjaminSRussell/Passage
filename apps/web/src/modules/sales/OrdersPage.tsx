import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Badge, buttonClassName, entityText, ListPage, standardLabel, StatusPill, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { describeProblem, HOLD_TYPES, listOrders, ORDER_STATUSES, type HoldType, type OrderStatus, type OrderSummary, type Problem } from "./api/client";
import { money, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
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

const columns: ListColumn<OrderSummary>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  {
    key: "customerId",
    header: "Customer",
    sortable: false,
    render: (row) => <EntityName entity="customer" id={row.customerId} />,
    csv: (row) => entityText("customer", row.customerId),
  },
  { key: "orderType", header: "Type", render: (row) => sentence(row.orderType), sortValue: (row) => sentence(row.orderType), csv: (row) => sentence(row.orderType) },
  {
    key: "status",
    header: "Status",
    render: (row) => <StatusPill status={row.status} />,
    sortValue: (row) => standardLabel(row.status),
    csv: (row) => standardLabel(row.status),
  },
  {
    key: "activeHolds",
    header: "Holds",
    render: (row) => <HoldBadges holds={row.activeHolds} />,
    sortValue: (row) => row.activeHolds.map(sentence).join(", "),
    csv: (row) => row.activeHolds.map(sentence).join(", "),
  },
  { key: "requestedShipDate", header: "Requested ship", mono: true },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "netTotal", header: "Net", align: "end", render: (row) => money(row.netTotal, row.currency), csv: (row) => row.netTotal, csvWith: "currency" },
  { key: "currency", header: "Currency", defaultHidden: true },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "Any status", options: ORDER_STATUSES.map((s) => ({ value: s, label: sentence(s) })) },
  { kind: "select", key: "hold", label: "Active hold", anyLabel: "Any or none", options: HOLD_TYPES.map((h) => ({ value: h, label: sentence(h) })) },
  { kind: "entity", key: "customerId", label: "Customer", entity: "customer" },
];

/** Orders newest first over the service cursor. A page that arrives after the filters changed is dropped. */
function useOrderRows(query: ListQuery): ListData<OrderSummary> {
  const status = (query.filters.status ?? "") as OrderStatus | "";
  const hold = (query.filters.hold ?? "") as HoldType | "";
  const customerId = query.filters.customerId ?? "";
  const state = useLoad((signal) => listOrders({ status, hold, customerId: customerId || undefined }, signal), `${status}|${hold}|${customerId}`);
  const [more, setMore] = useState<OrderSummary[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const generation = useRef(0);

  useEffect(() => {
    generation.current += 1;
    setMore([]);
    setMoreError(null);
    setLoadingMore(false);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  async function loadMore() {
    if (!cursor || loadingMore) return;
    const at = generation.current;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listOrders({ status, hold, customerId: customerId || undefined, cursor });
      if (at !== generation.current) return;
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      if (at === generation.current) setMoreError(describeProblem(error));
    } finally {
      if (at === generation.current) setLoadingMore(false);
    }
  }

  const error = state.error ?? moreError;
  return {
    rows: state.error ? [] : [...(state.data?.items ?? []), ...more],
    loading: state.loading,
    loadingMore,
    error: error ?? undefined,
    forbidden: state.error?.status === 403,
    hasMore: Boolean(cursor) && !state.error && !state.loading && !moreError,
    loadMore: () => void loadMore(),
    retry: state.error ? state.reload : () => void loadMore(),
  };
}

/** Sales orders list (PRD §5.27.14 "Sales orders list"). */
export function OrdersPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="sales-orders"
      title="Sales orders"
      description="Every order in your legal entities, newest first. Open an order to confirm it or release its holds."
      noun="sales orders"
      singular="sales order"
      actions={
        <Link className={buttonClassName("primary")} to="/sales/orders/new">
          New order
        </Link>
      }
      columns={columns}
      filters={filters}
      useRows={useOrderRows}
      search={(row) => ({ number: row.orderNumber, name: row.externalRef })}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/sales/orders/${row.id}`)}
      empty={{
        title: "No sales orders yet",
        description: "Orders you enter or receive through a channel appear here.",
        action: (
          <Link className={buttonClassName("secondary")} to="/sales/orders/new">
            Enter the first order
          </Link>
        ),
      }}
    />
  );
}
