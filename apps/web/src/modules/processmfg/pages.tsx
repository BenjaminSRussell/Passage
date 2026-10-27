import { Link } from "react-router-dom";
import { Card, CardGrid, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const AREAS = [
  { to: "/processmfg/formulas", title: "Formulas", text: "Recipes with scalable and percent-controlled lines, co- and by-products (FR-24.1)." },
  { to: "/processmfg/batch-orders", title: "Batch orders", text: "Orders scaled from a formula snapshot; rescale until the order starts (FR-24.2)." },
  { to: "/processmfg/potency", title: "Potency consumption", text: "Active-ingredient quantity for a batch's potency and the filler that keeps the batch size (BR-24.2)." },
  { to: "/processmfg/allocation", title: "Co-product allocation", text: "How a batch's cost splits over main, co- and by-products (BR-24.3)." },
  { to: "/processmfg/derivation", title: "Batch derivation", text: "A product batch attribute from the ingredient batches it consumed (FR-24.15)." },
  { to: "/processmfg/merge", title: "Batch merge", text: "The quantity, cost and attributes of the batch that merging sources produces (FR-24.16)." },
  { to: "/processmfg/batch-requirements", title: "Customer batch requirements", text: "Which batches meet a customer's shelf-life, attribute and same-batch rules (FR-24.19)." },
  { to: "/processmfg/consolidation", title: "Bulk consolidation", text: "Bulk batch orders sized for packed orders and pegged by due date (FR-24.17)." },
  { to: "/processmfg/batch-record-check", title: "Batch record approval check", text: "Whether every review exception is dispositioned and signed (BR-24.15)." },
];

/** Process manufacturing workspace: where each task lives. */
export function ProcessHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Process manufacturing"
        description="Formula-based batch orders: formulas, scaling to batch size, potency consumption, co-product cost allocation and the batch rules that judge derivation, merge, consolidation and customer requirements."
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
