import { Link } from "react-router-dom";
import { Card, CardGrid, Icon, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const tools = [
  {
    title: "Intercompany journal",
    to: "/intercompany/journal",
    action: "Preview a journal",
    description:
      "Enter one line in entity A with its offset in entity B and see the due-to and due-from legs both entities would post, balanced per entity and sharing one pair id.",
  },
  {
    title: "Reconciliation",
    to: "/intercompany/reconciliation",
    action: "Classify balances",
    description:
      "Compare what each side booked for an entity pair, period and currency, and see which pairs match, which are timing differences closed in the next period and which are mismatches.",
  },
];

/** Landing page of the intercompany workspace (wave 40). */
export function IntercompanyHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Intercompany workspace"
        description="Derive intercompany journals and reconcile due-to and due-from balances between legal entities. Partner setup, mirror documents, posting and the stored reconciliation workspace arrive with the next slices."
      />
      <CardGrid as="ul" label="Intercompany tools">
        {tools.map((tool) => (
          <li key={tool.to} className={styles.cell}>
            <Card title={tool.title} headingLevel={3}>
              <div className={styles.cardBody}>
                <p className={styles.note}>{tool.description}</p>
                <ul className={styles.links}>
                  <li>
                    <Link className={styles.link} to={tool.to}>
                      {tool.action}
                      <Icon name="chevronRight" size={16} />
                    </Link>
                  </li>
                </ul>
              </div>
            </Card>
          </li>
        ))}
      </CardGrid>
    </div>
  );
}
