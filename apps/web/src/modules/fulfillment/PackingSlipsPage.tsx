import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, ListPage, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { FulfillmentApiError, listPackingSlips, type PackingSlipStatus, type PackingSlipSummary } from "./api/client";
import { SlipStatusBadge, slipStatusText } from "./badges";
import { formatDate, formatMoney, shortId } from "./format";
import { useLoad } from "./useLoad";
import { OrderNumber, orderNumberText } from "../sales/OrderNumber";

const isReversal = (row: PackingSlipSummary) => row.reversalOfId !== null;

const columns: ListColumn<PackingSlipSummary>[] = [
  { key: "packingSlipNumber", header: "Packing slip", mono: true },
  {
    key: "status",
    header: "Status",
    render: (row) => <SlipStatusBadge status={row.status} reversal={isReversal(row)} />,
    sortValue: (row) => slipStatusText(row.status, isReversal(row)),
    csv: (row) => slipStatusText(row.status, isReversal(row)),
  },
  { key: "postingDate", header: "Posting date", render: (row) => formatDate(row.postingDate), csv: (row) => formatDate(row.postingDate) },
  { key: "orderId", header: "Order", sortable: false, render: (row) => <OrderNumber id={row.orderId} />, csv: (row) => orderNumberText(row.orderId) },
  { key: "shipmentId", header: "Shipment", mono: true, defaultHidden: true, render: (row) => shortId(row.shipmentId) },
  { key: "totalCost", header: "Cost", align: "end", render: (row) => formatMoney(row.totalCost, row.currency), csv: (row) => row.totalCost, csvWith: "currency" },
  { key: "currency", header: "Currency", defaultHidden: true },
];

const filters: ListFilterDef[] = [
  {
    kind: "select",
    key: "status",
    label: "Status",
    anyLabel: "All statuses",
    options: (["POSTED", "CANCELLED", "INVOICED"] as const).map((s) => ({ value: s, label: slipStatusText(s) })),
  },
];

/** The slips of one status; the service answers one page, so there is nothing more to load. */
function usePackingSlipRows(query: ListQuery): ListData<PackingSlipSummary> {
  const status = (query.filters.status ?? "") as PackingSlipStatus | "";
  const [forbidden, setForbidden] = useState(false);
  const state = useLoad(async (signal) => {
    try {
      const rows = await listPackingSlips({ status }, signal);
      if (!signal.aborted) setForbidden(false);
      return rows;
    } catch (error) {
      if (!signal.aborted) setForbidden(error instanceof FulfillmentApiError && error.status === 403);
      throw error;
    }
  }, status);
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

/** Packing slip journal (section 5.28.14): posted delivery notes and their reversals. */
export function PackingSlipsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="fulfillment-packing-slips"
      title="Packing slips"
      description="Posted packing slips with the cost they moved from inventory. A cancelled slip has a reversal with the mirror entries."
      noun="packing slips"
      singular="packing slip"
      columns={columns}
      filters={filters}
      useRows={usePackingSlipRows}
      search={(row) => ({ number: row.packingSlipNumber })}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/fulfillment/packing-slips/${row.id}`)}
      empty={{
        title: "No packing slips yet",
        description: "Packing slips appear here when a packed shipment is posted.",
        action: (
          <Link className={buttonClassName("secondary")} to="/fulfillment/shipments">
            Go to shipments
          </Link>
        ),
      }}
    />
  );
}
