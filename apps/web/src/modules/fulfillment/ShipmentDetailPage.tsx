import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getShipment, listPackingSlips, type AutoPackResult, type Container, type PackingSlipSummary, type Problem, type Shipment, type ShipmentLine } from "./api/client";
import { ShipmentStatusBadge, SlipStatusBadge } from "./badges";
import { compareDecimal, dash, formatDate, formatDateTime, formatMoney, shortId, SHORT_PICK_REASONS, subtractDecimal } from "./format";
import styles from "./pages.module.css";
import { AutoPackForm, PackForm, PickForm, PostForm } from "./ShipmentActions";
import { canUnnest, NestForm, UnnestButton } from "./PalletForm";
import { BackorderRemainderPanel } from "./BackorderPanel";
import type { PostedRemainder } from "./PackingSlipDetailPage";
import { canCancelShipment, canCloseRemainder, CancelShipmentPanel, CloseRemainderPanel, canUnpack, LabelButton, LabelPanel, TrackingCard, UnpackButton } from "./ShipmentTracking";
import { TrackingForm, trackableLines, trackingCover } from "./TrackingForm";
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
  { key: "qtyPicked", header: "Picked", align: "end", render: (row) => (row.pickConfirmed ? (compareDecimal(row.qtyPicked, row.qtyPlanned) > 0 ? `${row.qtyPicked} (+${subtractDecimal(row.qtyPicked, row.qtyPlanned)} over plan)` : row.qtyPicked) : "Not confirmed") },
  { key: "qtyPacked", header: "Packed", align: "end" },
  { key: "qtyShipped", header: "Shipped", align: "end" },
  { key: "remaining", header: "To ship", align: "end", render: (row) => (compareDecimal(row.qtyShipped, row.qtyPlanned) >= 0 ? "0" : subtractDecimal(row.qtyPlanned, row.qtyShipped)) },
  { key: "qtyClosed", header: "Closed", align: "end", render: (row) => row.qtyClosed ?? "0" },
  { key: "zone", header: "Zone", render: (row) => row.zone ?? dash },
  { key: "partial", header: "Release", render: (row) => (row.partial ? "Partial (ATP)" : "Full") },
  {
    key: "tracking",
    header: "Batch / serial",
    render: (row) => {
      const entries = row.tracking ?? [];
      if (entries.length === 0) return row.trackingRequired ? "Required" : dash;
      const text = entries.map((e) => [e.serialNo ?? e.batchNo, e.locationCode ? `@${e.locationCode}` : "", e.serialNo ? "" : ` × ${e.qty}`].join("")).join(", ");
      return row.trackingRequired ? `${text} (${trackingCover(row)})` : text;
    },
  },
  { key: "shortPickReason", header: "Short reason", render: (row) => (row.shortPickReason ? (SHORT_PICK_REASONS[row.shortPickReason] ?? row.shortPickReason) : dash) },
];

/** The pallet a carton sits on, or the number of cartons a pallet holds. */
function palletCell(row: Container, containers: Container[]): ReactNode {
  if (row.parentContainerId) return containers.find((c) => c.id === row.parentContainerId)?.sscc ?? dash;
  const n = (row.childContainerIds ?? []).length;
  if (n === 0) return dash;
  return n === 1 ? "1 carton" : `${n} cartons`;
}

function containerColumns(lines: ShipmentLine[], containers: Container[], label: (row: Container) => ReactNode): DataTableColumn<Container>[] {
  const orderLine = (id: string) => shortId(lines.find((l) => l.id === id)?.orderLineId ?? id);
  return [
    { key: "sscc", header: "SSCC", mono: true },
    { key: "containerType", header: "Type" },
    { key: "status", header: "Status" },
    { key: "pallet", header: "Pallet", render: (row) => palletCell(row, containers) },
    { key: "grossKg", header: "Gross kg", align: "end" },
    { key: "returnable", header: "Returnable", render: (row) => (row.returnable ? "Yes" : "No") },
    { key: "packagingMaterial", header: "Material", render: (row) => row.packagingMaterial ?? dash },
    { key: "lines", header: "Contents", render: (row) => row.lines.map((l) => `${orderLine(l.shipmentLineId)} × ${l.qty}`).join(", ") },
    { key: "label", header: "Label", render: label },
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
  const [label, setLabel] = useState<{ sscc: string; label: string | Problem } | null>(null);
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
  // A partially shipped shipment re-picks its remainder (FR-28.8).
  const canPick = shipment.status === "RELEASED" || shipment.status === "PICKING" || shipment.status === "PARTIALLY_SHIPPED";
  const canPack = shipment.status === "PICKED";
  const canPost = shipment.status === "PACKED";

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Shipment ${shipment.shipmentNumber}`}
        description={`Planned to ship ${formatDate(shipment.plannedShipDate)}${shipment.priority ? `, priority ${shipment.priority}` : ""}`}
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
            ["Carrier", shipment.carrierCode ?? dash],
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
      {canCancelShipment(shipment) ? <CancelShipmentPanel key={`cancel-${shipment.version}`} shipment={shipment} onDone={state.reload} /> : null}
      {canCloseRemainder(shipment) ? <CloseRemainderPanel key={`close-${shipment.version}`} shipment={shipment} onDone={state.reload} /> : null}
      {canCloseRemainder(shipment) ? <BackorderRemainderPanel key={`backorder-${shipment.id}`} shipment={shipment} onDone={state.reload} /> : null}
      {shipment.backorderOfId || shipment.backorderId ? (
        <Alert tone="info" title="Backorder shipment">
          {shipment.backorderOfId ? (
            <>
              This shipment holds the remainder of <Link to={`/fulfillment/shipments/${shipment.backorderOfId}`}>an earlier shipment</Link> of the same order.{" "}
            </>
          ) : null}
          {shipment.backorderId ? (
            <>
              The remainder of this shipment moved to <Link to={`/fulfillment/shipments/${shipment.backorderId}`}>its backorder shipment</Link>.
            </>
          ) : null}
        </Alert>
      ) : null}
      {shipment.status === "PARTIALLY_SHIPPED" ? (
        <Alert tone="info" title="Partially shipped">
          A packing slip has shipped part of this shipment. Pick the remainder below; the shipment is Shipped once every line has shipped its planned quantity. If the remainder will not ship, close it instead.
        </Alert>
      ) : null}
      <DataTable label="Shipment lines" columns={lineColumns} rows={shipment.lines} getRowKey={(row) => row.id} />
      {trackableLines(shipment).length > 0 ? <TrackingForm key={`tracking-${shipment.version}`} shipment={shipment} onDone={state.reload} /> : null}
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
      {canPost ? <PostForm key={`post-${shipment.version}`} shipment={shipment} onPosted={(slip) => navigate(`/fulfillment/packing-slips/${slip.id}`, { state: { remainderAction: slip.remainderAction, backorderShipmentId: slip.backorderShipmentId } satisfies PostedRemainder })} /> : null}
      <NestForm key={`nest-${shipment.version}`} shipment={shipment} onDone={state.reload} />
      <Card title="Containers">
        <DataTable
          label="Containers"
          columns={containerColumns(shipment.lines, shipment.containers, (row) => (
            <span className={styles.actions}>
              <LabelButton shipmentId={shipment.id} container={row} onLoaded={(sscc, loaded) => setLabel({ sscc, label: loaded })} />
              {canUnnest(shipment, row) ? <UnnestButton key={`unnest-${shipment.version}-${row.id}`} shipment={shipment} container={row} onDone={state.reload} /> : null}
              {canUnpack(shipment, row) ? <UnpackButton key={`unpack-${shipment.version}-${row.id}`} shipment={shipment} container={row} onDone={state.reload} /> : null}
            </span>
          ))}
          rows={shipment.containers}
          getRowKey={(row) => row.id}
          empty={{ title: "No containers yet", description: "Pack picked lines into a container to issue its SSCC label." }}
        />
      </Card>
      {label ? <LabelPanel sscc={label.sscc} label={label.label} onClose={() => setLabel(null)} /> : null}
      <TrackingCard shipment={shipment} onRecorded={state.reload} />
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
