import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getWave, type WaveShipment } from "./api/client";
import { ShipmentStatusBadge } from "./badges";
import { dash, formatDate, formatDateTime, GROUP_LABELS, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const shipmentColumns: DataTableColumn<WaveShipment>[] = [
  { key: "shipmentNumber", header: "Shipment", mono: true },
  { key: "status", header: "Status", render: (row) => <ShipmentStatusBadge status={row.status} /> },
  { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
  { key: "customerId", header: "Customer", mono: true, render: (row) => shortId(row.customerId) },
  { key: "carrierCode", header: "Carrier", render: (row) => row.carrierCode ?? dash },
  { key: "plannedShipDate", header: "Planned ship", render: (row) => formatDate(row.plannedShipDate) },
  { key: "lineCount", header: "Lines", align: "end" },
];

/** One pick wave: its group and its shipments with their current status. */
export function WaveDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getWave(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Pick wave" />
        <Alert tone="danger" title="Could not load the wave" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Pick wave" />
        <span className="visually-hidden">Loading the wave…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const wave = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Wave ${wave.waveNumber}`}
        description={`${wave.lineCount} ${wave.lineCount === 1 ? "line" : "lines"} of ${wave.orderCount} ${wave.orderCount === 1 ? "order" : "orders"}, grouped by ${(GROUP_LABELS[wave.groupBy] ?? wave.groupBy).toLowerCase()}.`}
        actions={
          <Link className={buttonClassName("secondary")} to="/fulfillment/waves">
            All waves
          </Link>
        }
      />
      <Card>
        <dl className={styles.details}>
          {[
            ["Group", wave.groupKey === "UNASSIGNED" ? "Unassigned" : wave.groupKey],
            ["Warehouse", wave.warehouseId],
            ["Legal entity", wave.legalEntityId],
            ["Created", formatDateTime(wave.createdAt)],
          ].map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Shipments">
        <DataTable
          label="Wave shipments"
          columns={shipmentColumns}
          rows={wave.shipments}
          getRowKey={(row) => row.id}
          onRowOpen={(row) => navigate(`/fulfillment/shipments/${row.id}`)}
          empty={{ title: "No shipments", description: "This wave holds no shipment you can see." }}
        />
      </Card>
    </div>
  );
}
