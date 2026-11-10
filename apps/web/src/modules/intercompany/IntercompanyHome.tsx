import { Link } from "react-router-dom";
import { Card, CardGrid, Icon, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const tools = [
  {
    title: "Trading partners",
    to: "/intercompany/partners",
    action: "Manage pairings",
    description:
      "Pair a customer in one entity with a vendor in another, set the mirror-document actions, the pricing policy and the change tolerance, price a line by the policy and check whether an order change needs re-approval.",
  },
  {
    title: "Intercompany journal",
    to: "/intercompany/journal",
    action: "Preview a journal",
    description:
      "Enter one line in entity A with its offset in entity B and see the due-to and due-from legs both entities would post, balanced per entity and sharing one pair id.",
  },
  {
    title: "Posted journals",
    to: "/intercompany/journals",
    action: "Open posted journals",
    description:
      "See every intercompany journal posted to the ledgers with the entry each entity made, and which ones were paused or reversed because a ledger refused its side.",
  },
  {
    title: "Reconciliation runs",
    to: "/intercompany/reconciliation-runs",
    action: "Open the runs",
    description:
      "Open the stored reconciliation of a period, filter the pairs that need action and resolve each with a note.",
  },
  {
    title: "Charge log",
    to: "/intercompany/charge-log",
    action: "Open the charge log",
    description:
      "See every charge by source and version, including the ones kept on suspense for lack of a transfer price.",
  },
  {
    title: "Reconciliation",
    to: "/intercompany/reconciliation",
    action: "Classify balances",
    description:
      "Compare what each side booked for an entity pair, period and currency, and see which pairs match, which are timing differences closed in the next period and which are mismatches.",
  },
  {
    title: "Intercompany charges",
    to: "/intercompany/charges",
    action: "Price a charge",
    description:
      "Price an expense, hour, item or fee that another entity bears by the transfer price effective on its date, and see the cost, price, IC margin and the legs both entities post.",
  },
  {
    title: "Centralized payments",
    to: "/intercompany/centralized-payments",
    action: "Preview a payment",
    description:
      "Pay a vendor invoice or receive a customer payment on behalf of another entity, and see the bank, due-to and due-from postings in both entities and their reversal when the bank returns the payment.",
  },
  {
    title: "Intercompany planning",
    to: "/intercompany/planning",
    action: "Sequence a planning group",
    description:
      "Order a planning group so buyers run before their suppliers, and see the intercompany demand each supplier receives after the transport lead time.",
  },
  {
    title: "Direct delivery chains",
    to: "/intercompany/chains",
    action: "Check a chain",
    description:
      "Link a customer order, its intercompany purchase order and the supplier's sales order that ships straight to the customer, and see the receipt and packing slip the shipment posts in the buying entity.",
  },
  {
    title: "Transfer-pricing evidence",
    to: "/intercompany/transfer-pricing",
    action: "Produce an evidence file",
    description:
      "List the priced intercompany lines between two entities for a period and download the evidence file with price, cost, margin and pricing basis.",
  },
];

/** Landing page of the intercompany workspace (wave 40). */
export function IntercompanyHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Intercompany workspace"
        description="Set up trading partners, derive intercompany journals, reconcile due-to and due-from balances, price intercompany charges, preview centralized payments, check direct delivery chains, export transfer-pricing evidence and sequence planning groups. Journals and charges post to the ledgers and reconciliation runs are stored; mirror documents arrive with the next slice."
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
