import { Link } from "react-router-dom";
import { Alert, Button, Card, CardGrid, PageHeader, Skeleton, StatGroup, StatTile } from "../../shell/ui";
import { listShipments, type ShipmentSummary } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** How many shipments the workspace reads to count its tiles. */
export const WORKSPACE_SAMPLE = 500;

function count(rows: ShipmentSummary[], ...statuses: ShipmentSummary["status"][]): number {
  return rows.filter((r) => statuses.includes(r.status)).length;
}

const links = [
  { to: "/fulfillment/release", title: "Release lines", text: "Release confirmed order lines to a warehouse by rule." },
  { to: "/fulfillment/workbench", title: "Picking workbench", text: "Group released shipments into pick waves, preview and create them." },
  { to: "/fulfillment/shipments", title: "Shipments", text: "Pick, pack into containers with SSCCs, post the packing slip and track the carrier." },
  { to: "/fulfillment/packing-slips", title: "Packing slips", text: "Posted delivery notes with their cost journal; cancel before invoicing." },
  { to: "/fulfillment/exceptions", title: "Warehouse exceptions", text: "Cases sales cancelling an order remainder left for the supervisor: goods already shipped or packed beyond what the order keeps." },
  { to: "/fulfillment/delivered-not-invoiced", title: "Delivered not invoiced", text: "Shipped cost not yet invoiced: the GL 1405 balance per legal entity and its slips." },
  { to: "/fulfillment/backorders", title: "Short picks and backorders", text: "Lines picked below plan and the remainder still owed until it is re-picked." },
  { to: "/fulfillment/returnable-aging", title: "Returnable packaging aging", text: "Pallets and crates customers still hold, how long, and which are due to be charged." },
  { to: "/fulfillment/sscc-register", title: "SSCC register", text: "Every SSCC issued, with its container, shipment and the date it could be reused." },
  { to: "/fulfillment/scan", title: "Scan a label", text: "Read a GS1-128 barcode into GTIN, batch, serial and quantity." },
];

/** Fulfillment workspace: shipment counts by stage and the entry points (section 5.28.6). */
export function WorkspacePage() {
  const state = useLoad((signal) => listShipments({ limit: WORKSPACE_SAMPLE }, signal), "workspace");
  const rows = state.data ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader title="Fulfillment" description="Release orders to the warehouse, pick, pack and ship them, and post packing slips." />
      {state.error ? (
        <Alert tone="danger" title="Could not load shipment counts" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : state.loading && !state.data ? (
        <div aria-busy="true">
          <span className="visually-hidden">Loading shipment counts…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <StatGroup label="Fulfillment tiles">
          <StatTile label="Released, not picked" value={count(rows, "RELEASED")} />
          <StatTile label="Picking" value={count(rows, "PICKING", "PICKED")} />
          <StatTile label="Packed, not shipped" value={count(rows, "PACKED")} tone="warning" />
          <StatTile label="Partially shipped" value={count(rows, "PARTIALLY_SHIPPED")} />
          <StatTile label="Shipped" value={count(rows, "SHIPPED", "DELIVERED")} />
        </StatGroup>
      )}
      {rows.length >= WORKSPACE_SAMPLE ? (
        <p className={styles.note}>Counts cover the {WORKSPACE_SAMPLE} most recent shipments.</p>
      ) : null}
      <CardGrid label="Fulfillment pages">
        {links.map((l) => (
          <Card key={l.to} title={l.title}>
            <p className={styles.note}>{l.text}</p>
            <Link to={l.to}>Open {l.title.toLowerCase()}</Link>
          </Card>
        ))}
      </CardGrid>
    </div>
  );
}
