import { Link } from "react-router-dom";
import { Card, CardGrid, Icon, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const tools = [
  {
    title: "Groups and trial balance",
    to: "/consolidation/groups",
    action: "Open the groups",
    description:
      "Set up a group's legal entities, their ownership and the account mapping, collect each unit's balances from its ledger and read the consolidated trial balance with each unit's share.",
  },
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
  {
    title: "Profit in inventory",
    to: "/consolidation/profit-in-inventory",
    action: "Compute unrealized profit",
    description:
      "List the intercompany receipt layers an entity still holds and see the unrealized profit to eliminate per layer, from the supplier's group cost or the seller's margin.",
  },
  {
    title: "Intercompany differences",
    to: "/consolidation/ic-differences",
    action: "Eliminate partner pairs",
    description:
      "Eliminate each partner pair's receivables against its payables and see which differences go to the intercompany difference account and which stop the task.",
  },
  {
    title: "Investments",
    to: "/consolidation/investments",
    action: "Apply the equity method",
    description:
      "Take an associate's share of profit and dividends into the investment, and compute the gain or loss when a subsidiary is sold.",
  },
  {
    title: "Unit validation",
    to: "/consolidation/validation",
    action: "Validate units",
    description:
      "Check each unit's balance sheet, retained earnings roll and intercompany partners, and see whether the group translation task is blocked.",
  },
  {
    title: "Dividends within the group",
    to: "/consolidation/dividends",
    action: "Eliminate a dividend",
    description:
      "Remove a dividend a subsidiary paid to its parent from dividend income and NCI equity against dividends declared, with a rounding line when cents do not add up.",
  },
  {
    title: "Reclassification",
    to: "/consolidation/reclassification",
    action: "Dry-run the rules",
    description:
      "Regroup vendors in debit, customers in credit, long-term items and negative group balances, and see each line and when entity-level entries reverse.",
  },
  {
    title: "Top-side group journals",
    to: "/consolidation/group-journals",
    action: "Check a journal",
    description:
      "Check a manual group or unit journal before posting: balance, reason and attachment, whether it needs approval, and the periods a carried-forward entry posts in.",
  },
];

/** Landing page of the consolidation workspace (wave 40). */
export function ConsolidationHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Consolidation workspace"
        description="Check the translation, elimination, profit-in-inventory, investment, validation, reclassification and top-side journal arithmetic of a group consolidation. Group structures and the consolidated trial balance are stored; versioned runs over collected balances and the stored monitor arrive with the next slices."
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
