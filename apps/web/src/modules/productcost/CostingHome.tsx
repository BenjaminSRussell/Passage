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
    title: "Mixed cost",
    to: "/productcost/mixed-cost",
    action: "Mix alternatives",
    description:
      "Cost an item supplied from several procurement alternatives, each rolled with its own structure and weighted by its mixing ratio.",
  },
  {
    title: "Sales order estimate",
    to: "/productcost/sales-order-estimate",
    action: "Cost a sales line",
    description: "Cost a configured or make-to-order line at its order quantity, by component, and see its margin against the net amount.",
  },
  {
    title: "Estimate comparison",
    to: "/productcost/compare",
    action: "Compare estimates",
    description: "Roll a baseline and a what-if candidate and compare each item's standard and its change per cost component.",
  },
  {
    title: "Production variances",
    to: "/productcost/variances",
    action: "Decompose a variance",
    description:
      "Split an ended production order's variance into lot size, quantity, price and substitution by cost component. The four always add up to the total.",
  },
  {
    title: "Component postings",
    to: "/productcost/postings",
    action: "Preview postings",
    description:
      "Preview the journals for cost of goods sold on an issue and a purchase price variance on a receipt, split by cost component.",
  },
  {
    title: "Standard cost reconciliation",
    to: "/productcost/reconciliation",
    action: "Reconcile inventory",
    description: "Check that on-hand at the active standard ties to the GL inventory balance at each site.",
  },
  {
    title: "Activation preview",
    to: "/productcost/activation-preview",
    action: "Preview a revaluation",
    description:
      "See what activating new standards would do to on-hand inventory: the revaluation per item and site and the journal it would post.",
  },
  {
    title: "Costing versions",
    to: "/productcost/versions",
    action: "Manage versions",
    description: "Set up standard, planned, simulation and conversion versions with their fallback principle, then block, activate or close them.",
  },
  {
    title: "Standard cost conversion",
    to: "/productcost/conversion",
    action: "Convert to standard",
    description:
      "Move items from actual costing to standard cost through the checklist: validate the blockers, preview the revaluation and record each step.",
  },
];

/** Landing page of the costing workspace (wave 39). */
export function CostingHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Costing workspace"
        description="Compute and compare standard costs, cost sales lines, explain production variances, preview component postings, reconcile inventory at standard, check a revaluation before activating new standards, manage costing versions and convert items to standard cost. Stored versions, calculations and posting arrive with the persistence slice."
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
