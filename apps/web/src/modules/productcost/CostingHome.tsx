import { Link } from "react-router-dom";
import { Card, CardGrid, Icon, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const tools = [
  {
    title: "Cost roll-up",
    to: "/productcost/roll",
    action: "Roll items",
    description:
      "Roll standard costs from BOMs, routings and the costing sheet, one item or a whole selection by level, with the cost component split and per-item diagnostics.",
  },
  {
    title: "Production variances",
    to: "/productcost/variances",
    action: "Decompose a variance",
    description:
      "Split an ended production order's variance into lot size, quantity, price and substitution by cost component. The four always add up to the total.",
  },
  {
    title: "Activation preview",
    to: "/productcost/activation-preview",
    action: "Preview a revaluation",
    description:
      "See what activating new standards would do to on-hand inventory: the revaluation per item and site and the journal it would post.",
  },
];

/** Landing page of the costing workspace (wave 39). */
export function CostingHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Costing workspace"
        description="Compute standard costs, explain production variances and check a revaluation before activating new standards. Costing versions, stored calculations and posting arrive with the next slices."
      />
      <CardGrid as="ul" label="Costing tools">
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
