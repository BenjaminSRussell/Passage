import { Link } from "react-router-dom";
import { Alert, Badge, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listInTransit, type InTransitItem } from "./api/client";
import { describeDimensions } from "./format";
import { ALL_DIMENSIONS, ProblemAlert } from "./parts";
import { Named } from "../purchasing/Named";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<InTransitItem>[] = [
  {
    key: "number",
    header: "Transfer order",
    mono: true,
    render: (row) => <Link to={`/inventory/transfer-orders/${row.orderId}`}>{row.number}</Link>,
  },
  { key: "productId", header: "Product", render: (row) => <Named entity="product" id={row.productId} /> },
  { key: "dimensions", header: "Dimensions", render: (row) => describeDimensions(row.dimensions, ALL_DIMENSIONS) },
  { key: "route", header: "Route", mono: true, render: (row) => `${row.fromWarehouseId} → ${row.transitWarehouseId} → ${row.toWarehouseId}` },
  { key: "shipDate", header: "Shipped", mono: true },
  { key: "daysInTransit", header: "Days in transit", align: "end", mono: true, render: (row) => String(row.daysInTransit) },
  { key: "inTransitQty", header: "In transit", align: "end", mono: true, render: (row) => `${row.inTransitQty} ${row.unit}` },
  {
    key: "closedShort",
    header: "State",
    render: (row) => (row.closedShort ? <Badge tone="danger">Closed short, investigate</Badge> : <Badge tone="warning">Awaiting receipt</Badge>),
  },
];

/** Stock in transit (FR-16.16, SAP MB5T): shipped and not yet received, oldest shipment first. */
export function InTransitPage() {
  const load = useLoad((signal) => listInTransit(signal), "in-transit");
  const short = (load.data ?? []).filter((row) => row.closedShort).length;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Stock in transit"
        description="Quantities shipped to a transit warehouse and not yet received at their destination."
        actions={
          <Link className={buttonClassName("ghost")} to="/inventory/transfer-orders">
            Transfer orders
          </Link>
        }
      />
      {short > 0 ? (
        <Alert tone="warning" title="Shortfalls to investigate">
          {short === 1 ? "One line was" : `${short} lines were`} closed short: the missing quantity is still booked in transit.
        </Alert>
      ) : null}
      {load.error ? (
        <ProblemAlert title="Could not load stock in transit" problem={load.error} onRetry={load.reload} />
      ) : (
        <DataTable
          label="Stock in transit"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => row.lineId}
          loading={load.loading}
          empty={{ title: "Nothing in transit", description: "Every shipped transfer order has been received." }}
        />
      )}
    </div>
  );
}
