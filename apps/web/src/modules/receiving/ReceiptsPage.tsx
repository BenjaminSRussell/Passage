import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, entityText, ListPage, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { ApiError, fetchPurchaseOrderRef, fetchReceipts, RECEIPT_STATES, type Receipt, type ReceiptState } from "./api/client";
import { Named, PurchaseOrderRef, StateBadge, stateLabel } from "./parts";

const poNumbers = new Map<string, Promise<string>>();

/** The purchase order number for the CSV export; the id when the order cannot be read. */
function purchaseOrderText(id: string): Promise<string> {
  let pending = poNumbers.get(id);
  if (!pending) {
    pending = id === "" ? Promise.resolve("") : fetchPurchaseOrderRef(id).then(
      (po) => po.number,
      () => {
        poNumbers.delete(id);
        return id;
      },
    );
    poNumbers.set(id, pending);
  }
  return pending;
}

const columns: ListColumn<Receipt>[] = [
  { key: "receiptNumber", header: "Receipt", mono: true },
  { key: "packingSlip", header: "Packing slip" },
  {
    key: "vendorId",
    header: "Vendor",
    sortable: false,
    render: (r) => <Named entity="vendor" id={r.vendorId} />,
    csv: (r) => entityText("vendor", r.vendorId),
  },
  {
    key: "purchaseOrderId",
    header: "Purchase order",
    sortable: false,
    render: (r) => <PurchaseOrderRef id={r.purchaseOrderId} />,
    csv: (r) => purchaseOrderText(r.purchaseOrderId),
  },
  { key: "receiptDate", header: "Receipt date" },
  { key: "currency", header: "Currency" },
  {
    key: "state",
    header: "State",
    render: (r) => <StateBadge state={r.state} />,
    sortValue: (r) => stateLabel[r.state],
    csv: (r) => stateLabel[r.state],
  },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "state", label: "State", anyLabel: "All states", options: RECEIPT_STATES.map((s) => ({ value: s, label: stateLabel[s] })) },
  { kind: "entity", key: "vendorId", label: "Vendor", entity: "vendor" },
  { kind: "dateRange", key: "receiptDate", label: "Receipt date" },
];

/** Receipts newest first, paged by the service's cursor; state, vendor and receipt date filter on the service. */
function useReceiptRows(query: ListQuery): ListData<Receipt> {
  const { state = "", vendorId = "", receiptDateFrom: from = "", receiptDateTo: to = "" } = query.filters;
  const result = useInfiniteQuery({
    queryKey: ["receiving", "receipts", { state, vendorId, from, to }],
    queryFn: ({ pageParam }) =>
      fetchReceipts({ state: (state as ReceiptState) || undefined, vendorId: vendorId || undefined, from: from || undefined, to: to || undefined, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  return {
    rows: result.data?.pages.flatMap((p) => p.items) ?? [],
    loading: result.isPending,
    loadingMore: result.isFetchingNextPage,
    error: result.isError ? result.error : undefined,
    forbidden: result.error instanceof ApiError && result.error.status === 403,
    hasMore: Boolean(result.hasNextPage),
    loadMore: () => void result.fetchNextPage(),
    retry: () => void result.refetch(),
  };
}

export function ReceiptsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="receiving-receipts"
      title="Receipts"
      description="Deliveries from arrival at the dock to posting. Open a receipt to register what was received, post it to the ledger, correct it or cancel it."
      noun="receipts"
      singular="receipt"
      actions={
        <Link className={buttonClassName("primary")} to="/receiving/receipts/new">
          Record arrival
        </Link>
      }
      columns={columns}
      filters={filters}
      useRows={useReceiptRows}
      search={(r) => ({ number: r.receiptNumber })}
      getRowKey={(r) => r.id}
      onRowOpen={(r) => navigate(`/receiving/receipts/${encodeURIComponent(r.id)}`)}
      empty={{
        title: "No receipts yet",
        description: "Record an arrival when a delivery reaches the dock.",
        action: (
          <Link className={buttonClassName("secondary")} to="/receiving/receipts/new">
            Record the first arrival
          </Link>
        ),
      }}
    />
  );
}
