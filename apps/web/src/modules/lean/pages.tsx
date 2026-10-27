import { Link } from "react-router-dom";
import { Card, CardGrid, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const AREAS = [
  { to: "/lean/board", title: "Kanban board", text: "Cards by state for a flow or rule; move each card as its work progresses (FR-24.10)." },
  { to: "/lean/rules", title: "Kanban rules", text: "Fixed, scheduled and event replenishment per item, and kanban generation (FR-24.9, FR-24.11)." },
  { to: "/lean/flows", title: "Production flows", text: "Value-stream activities with cycle times, outputs and predecessors; activate when connected (FR-24.8)." },
  { to: "/lean/subcontracting", title: "Lean subcontracting", text: "Purchase order lines for service jobs on vendor work cells, priced by agreement (FR-24.20)." },
];

/** Lean manufacturing workspace: where each task lives. */
export function LeanHome() {
  return (
    <div className={styles.stack}>
      <PageHeader title="Lean manufacturing" description="Production flows, kanban rules and the kanban board for work cells." />
      <CardGrid as="ul" label="Lean manufacturing areas">
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
