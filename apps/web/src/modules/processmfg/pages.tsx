import { Link } from "react-router-dom";
import { Card, CardGrid, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const AREAS = [
  { to: "/processmfg/formulas", title: "Formulas", text: "Recipes with scalable and percent-controlled lines, co- and by-products." },
  { to: "/processmfg/batch-orders", title: "Batch orders", text: "Orders scaled from a formula snapshot; rescale until the order starts." },
  { to: "/processmfg/batches", title: "Batches", text: "Quality record of each batch: recorded attributes, expiry, and disposition with its history." },
  { to: "/processmfg/attribute-definitions", title: "Attribute definitions", text: "The attributes an item's batches carry, with limits and options." },
  { to: "/processmfg/potency", title: "Potency consumption", text: "Active-ingredient quantity for a batch's potency and the filler that keeps the batch size." },
  { to: "/processmfg/allocation", title: "Co-product allocation", text: "How a batch's cost splits over main, co- and by-products." },
  { to: "/processmfg/derivation", title: "Batch derivation", text: "A product batch attribute from the ingredient batches it consumed." },
  { to: "/processmfg/merge", title: "Batch merge", text: "The quantity, cost and attributes of the batch that merging sources produces." },
  { to: "/processmfg/batch-requirements", title: "Customer batch requirements", text: "Which batches meet a customer's shelf-life, attribute and same-batch rules." },
  { to: "/processmfg/consolidation", title: "Bulk consolidation", text: "Bulk batch orders sized for packed orders and pegged by due date." },
  { to: "/processmfg/batch-record-check", title: "Batch record approval check", text: "Whether every review exception is dispositioned and signed." },
  { to: "/processmfg/batch-attributes", title: "Batch attributes", text: "Recorded values checked against the item's attribute definitions; values outside limits are flagged." },
  { to: "/processmfg/picking", title: "Batch picking", text: "Which batches a line consumes: disposition, expiry with the override, potency band, first expiry first." },
  { to: "/processmfg/picking-stored", title: "Pick from stored batches", text: "The same picking rules over the batches of an item, with disposition, expiry and potency read from the batch register." },
  { to: "/processmfg/rework", title: "Rework batch order", text: "The plan for reworking a batch: output batch, disposition, quality order and cost." },
];

/** Process manufacturing workspace: where each task lives. */
export function ProcessHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Process manufacturing"
        description="Formula-based batch orders: formulas, scaling to batch size, potency consumption, co-product cost allocation and the batch rules that judge derivation, merge, consolidation, customer requirements, attributes, picking and rework."
      />
      <CardGrid as="ul" label="Process manufacturing areas">
        {AREAS.map((a) => (
          <li key={a.to}>
            <Card title={a.title}>
              <div className={styles.stack}>
                <p className={styles.note}>{a.text}</p>
                <Link to={a.to}>{`Open ${a.title.toLowerCase()}`}</Link>
              </div>
            </Card>
          </li>
        ))}
      </CardGrid>
    </div>
  );
}
