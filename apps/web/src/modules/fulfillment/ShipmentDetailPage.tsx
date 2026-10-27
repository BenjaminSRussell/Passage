import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getShipment, listPackingSlips, type AutoPackResult, type Container, type PackingSlipSummary, type Shipment, type ShipmentLine } from "./api/client";
import { ShipmentStatusBadge, SlipStatusBadge } from "./badges";
import { dash, formatDate, formatDateTime, formatMoney, shortId } from "./format";
import styles from "./pages.module.css";
import { AutoPackForm, PackForm, PickForm, PostForm } from "./ShipmentActions";
import { useLoad } from "./useLoad";

interface Detail {
  shipment: Shipment;
  slips: PackingSlipSummary[];
}

const lineColumns: DataTableColumn<ShipmentLine>[] = [
  { key: "orderLineId", header: "Order line", mono: true, render: (row) => shortId(row.orderLineId) },
  { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
  { key: "unit", header: "Unit" },
  { key: "qtyPlanned", header: "Planned", align: "end" },
  { key: "qtyPicked", header: "Picked", align: "end", render: (row) => (row.pickConfirmed ? row.qtyPicked : "Not confirmed") },
  { key: "qtyPacked", header: "Packed", align: "end" },
  { key: "qtyShipped", header: "Shipped", align: "end" },
];

function containerColumns(lines: ShipmentLine[]): DataTableColumn<Container>[] {
  const orderLine = (id: string) => shortId(lines.find((l) => l.id === id)?.orderLineId ?? id);
  return [
    { key: "sscc", header: "SSCC", mono: true },
    { key: "containerType", header: "Type" },
    { key: "status", header: "Status" },
    { key: "grossKg", header: "Gross kg", align: "end" },
    { key: "returnable", header: "Returnable", render: (row) => (row.returnable ? "Yes" : "No") },
    { key: "packagingMaterial", header: "Material", render: (row) => row.packagingMaterial ?? dash },
    { key: "lines", header: "Contents", render: (row) => row.lines.map((l) => `${orderLine(l.shipmentLineId)} × ${l.qty}`).join(", ") },
  ];
}

const slipColumns: DataTableColumn<PackingSlipSummary>[] = [
  { key: "packingSlipNumber", header: "Packing slip", mono: true },
  { key: "status", header: "Status", render: (row) => <SlipStatusBadge status={row.status} reversal={row.reversalOfId !== null} /> },
  { key: "postingDate", header: "Posting date", render: (row) => formatDate(row.postingDate) },
  { key: "totalCost", header: "Cost", align: "end", render: (row) => formatMoney(row.totalCost, row.currency) },
];

/** Shipment detail: lines, containers and packing slips, with the next command for its state (BR-28.6). */
export function ShipmentDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [autoPacked, setAutoPacked] = useState<AutoPackResult | null>(null);
  const state = useLoad<Detail>(async (signal) => {
    const [shipment, slips] = await Promise.all([getShipment(id, signal), listPackingSlips({ shipmentId: id }, signal)]);
    return { shipment, slips };
  }, id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Shipment" />
        <Alert tone="danger" title="Could not load the shipment" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Shipment" />
        <span className="visually-hidden">Loading the shipment…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { shipment, slips } = state.data;
  const canPick = shipment.status === "RELEASED" || shipment.status === "PICKING";
  const canPack = shipment.status === "PICKED";
  const canPost = shipment.status === "PACKED";

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Shipment ${shipment.shipmentNumber}`}
        description={`Planned to ship ${formatDate(shipment.plannedShipDate)}`}
        meta={<ShipmentStatusBadge status={shipment.status} />}
        actions={
          <Link className={buttonClassName("secondary")} to="/fulfillment/shipments">
            All shipments
          </Link>
        }
      />
      <Card>
        <dl className={styles.details}>
          {[
            ["Order", shipment.orderId],
            ["Customer", shipment.customerId],
            ["Warehouse", shipment.warehouseId],
            ["Legal entity", shipment.legalEntityId],
            ["Shipped", formatDateTime(shipment.actualShipDate)],
            ["Version", String(shipment.version)],
          ].map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <DataTable label="Shipment lines" columns={lineColumns} rows={shipment.lines} getRowKey={(row) => row.id} />
      {canPick ? <PickForm key={`pick-${shipment.version}`} shipment={shipment} onDone={state.reload} /> : null}
      {autoPacked ? (
        <Alert tone={autoPacked.warnings.length > 0 ? "warning" : "success"} title={`Auto-pack closed ${autoPacked.containers.length} container${autoPacked.containers.length === 1 ? "" : "s"}`} live="status">
          {autoPacked.warnings.length > 0 ? (
            <ul className={styles.plainList}>
              {autoPacked.warnings.map((w) => (
                <li key={w.shipmentLineId}>
                  {w.message} ({w.code}). Pack it below or add a packing rule.
                </li>
              ))}
            </ul>
          ) : (
            "Every picked line is packed."
          )}
        </Alert>
      ) : null}
      {canPack ? (
        <AutoPackForm
          key={`autopack-${shipment.version}`}
          shipment={shipment}
          onDone={(result) => {
            setAutoPacked(result);
            state.reload();
          }}
        />
      ) : null}
      {canPack ? <PackForm key={`pack-${shipment.version}`} shipment={shipment} onDone={state.reload} /> : null}
      {canPost ? <PostForm key={`post-${shipment.version}`} shipment={shipment} onPosted={(slip) => navigate(`/fulfillment/packing-slips/${slip.id}`)} /> : null}
      <Card title="Containers">
        <DataTable
          label="Containers"
          columns={containerColumns(shipment.lines)}
          rows={shipment.containers}
          getRowKey={(row) => row.id}
          empty={{ title: "No containers yet", description: "Pack picked lines into a container to issue its SSCC label." }}
        />
      </Card>
      <Card title="Packing slips">
        <DataTable
          label="Packing slips"
          columns={slipColumns}
          rows={slips}
          getRowKey={(row) => row.id}
          onRowOpen={(row) => navigate(`/fulfillment/packing-slips/${row.id}`)}
          empty={{ title: "No packing slips yet", description: "Post the packing slip once every picked unit is packed." }}
        />
      </Card>
    </div>
  );
}
