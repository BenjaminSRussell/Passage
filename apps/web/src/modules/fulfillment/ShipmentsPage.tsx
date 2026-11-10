import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listShipments, SHIPMENT_STATUSES, type ShipmentStatus, type ShipmentSummary } from "./api/client";
import { ShipmentStatusBadge } from "./badges";
import { formatDate, formatDateTime, SHIPMENT_STATUS_LABELS, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<ShipmentSummary>[] = [
  { key: "shipmentNumber", header: "Shipment", mono: true },
  { key: "status", header: "Status", render: (row) => <ShipmentStatusBadge status={row.status} /> },
  { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
  { key: "customerId", header: "Customer", mono: true, render: (row) => shortId(row.customerId) },
  { key: "warehouseId", header: "Warehouse", mono: true, render: (row) => shortId(row.warehouseId) },
  { key: "plannedShipDate", header: "Planned ship", render: (row) => formatDate(row.plannedShipDate) },
  { key: "actualShipDate", header: "Shipped", render: (row) => formatDateTime(row.actualShipDate) },
  { key: "lineCount", header: "Lines", align: "end" },
];

/** Shipments list / delivery monitor (section 5.28.14), filtered by status. */
export function ShipmentsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ShipmentStatus | "">("");
  const state = useLoad((signal) => listShipments({ status }, signal), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Shipments"
        description="Every shipment released to the warehouse, newest first. Open one to pick, pack and post its packing slip."
        actions={
          <Link className={buttonClassName("primary")} to="/fulfillment/release">
            Release lines
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="shipment-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as ShipmentStatus | "")}>
              <option value="">All statuses</option>
              {SHIPMENT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {SHIPMENT_STATUS_LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load shipments" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Shipments"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          onRowOpen={(row) => navigate(`/fulfillment/shipments/${row.id}`)}
          empty={
            status
              ? { title: "No shipments in this status", description: "Choose another status, or show all statuses." }
              : { title: "No shipments yet", description: "Release confirmed order lines to the warehouse to create shipments." }
          }
        />
      )}
    </div>
  );
}
