import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, entityText, ListPage, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { FulfillmentApiError, listShipments, SHIPMENT_STATUSES, type ShipmentStatus, type ShipmentSummary } from "./api/client";
import { ShipmentStatusBadge } from "./badges";
import { formatDate, formatDateTime, SHIPMENT_STATUS_LABELS } from "./format";
import { useLoad } from "./useLoad";
import { EntityName } from "../../shell/lookup/EntityName";
import { OrderNumber, orderNumberText } from "../sales/OrderNumber";

const statusText = (row: ShipmentSummary) => SHIPMENT_STATUS_LABELS[row.status] ?? row.status;

const columns: ListColumn<ShipmentSummary>[] = [
  { key: "shipmentNumber", header: "Shipment", mono: true },
  { key: "status", header: "Status", render: (row) => <ShipmentStatusBadge status={row.status} />, sortValue: statusText, csv: statusText },
  { key: "orderId", header: "Order", sortable: false, render: (row) => <OrderNumber id={row.orderId} />, csv: (row) => orderNumberText(row.orderId) },
  { key: "customerId", header: "Customer", sortable: false, render: (row) => <EntityName entity="customer" id={row.customerId} />, csv: (row) => entityText("customer", row.customerId) },
  { key: "warehouseId", header: "Warehouse", sortable: false, render: (row) => <EntityName entity="warehouse" id={row.warehouseId} />, csv: (row) => entityText("warehouse", row.warehouseId) },
  { key: "plannedShipDate", header: "Planned ship", render: (row) => formatDate(row.plannedShipDate), csv: (row) => row.plannedShipDate ?? "" },
  { key: "actualShipDate", header: "Shipped", render: (row) => formatDateTime(row.actualShipDate), csv: (row) => row.actualShipDate ?? "" },
  { key: "lineCount", header: "Lines", align: "end" },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: SHIPMENT_STATUSES.map((s) => ({ value: s, label: SHIPMENT_STATUS_LABELS[s] })) },
];

/** The shipments of one status; the service answers one page, so there is nothing more to load. */
function useShipmentRows(query: ListQuery): ListData<ShipmentSummary> {
  const status = (query.filters.status ?? "") as ShipmentStatus | "";
  const [forbidden, setForbidden] = useState(false);
  const state = useLoad(async (signal) => {
    try {
      const rows = await listShipments({ status }, signal);
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

/** Shipments list / delivery monitor (section 5.28.14), filtered by status. */
export function ShipmentsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="fulfillment-shipments"
      title="Shipments"
      description="Every shipment released to the warehouse, newest first. Open one to pick, pack and post its packing slip."
      noun="shipments"
      singular="shipment"
      actions={
        <Link className={buttonClassName("primary")} to="/fulfillment/release">
          Release lines
        </Link>
      }
      columns={columns}
      filters={filters}
      useRows={useShipmentRows}
      search={(row) => ({ number: row.shipmentNumber })}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/fulfillment/shipments/${row.id}`)}
      empty={{
        title: "No shipments yet",
        description: "Release confirmed order lines to the warehouse to create shipments.",
        action: (
          <Link className={buttonClassName("secondary")} to="/fulfillment/release">
            Release lines
          </Link>
        ),
      }}
    />
  );
}
