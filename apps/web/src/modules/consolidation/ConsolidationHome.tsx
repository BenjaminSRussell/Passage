import { Link } from "react-router-dom";
import { Card, CardGrid, Icon, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const tools = [
  {
    title: "Elimination entries",
    to: "/consolidation/eliminations",
    action: "Preview eliminations",
    description:
      "Enter a parent and subsidiary's intercompany sales, balances, inventory and acquisition equity and see entries E1 to E5 with goodwill and the non-controlling interest, balanced per rule.",
  },
  {
    title: "Currency translation",
    to: "/consolidation/translation",
    action: "Translate a unit",
    description:
      "Translate a unit's net assets and net income into group currency under IAS 21 and see the translation difference (CTA) with the share that belongs to the non-controlling interest.",
  },
];

/** Landing page of the consolidation workspace (wave 40). */
export function ConsolidationHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Consolidation workspace"
        description="Check the translation and elimination arithmetic of a group consolidation. Group structures, versioned runs, the consolidated trial balance and the monitor arrive with the next slices."
      />
      <CardGrid as="ul" label="Consolidation tools">
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
