import { Link } from "react-router-dom";
import { Card, CardGrid, PageHeader, Skeleton, StatGroup, StatTile } from "../../shell/ui";
import { listPurchaseOrders, listRequisitions, listTradeAgreements, type Page } from "./api/client";
import { LoadError, useResource } from "./parts";
import styles from "./pages.module.css";

const COUNT_LIMIT = 200;

/** Count shown on a tile: the page size, with "+" when more pages exist. */
export function countLabel(page: Page<unknown>): string {
  return page.nextCursor ? `${page.items.length}+` : String(page.items.length);
}

const sections = [
  {
    title: "Requisitions",
    description: "Raise a request, track its approval chain, and see what is ready to buy.",
    links: [
      { label: "All requisitions", to: "/purchasing/requisitions" },
      { label: "New requisition", to: "/purchasing/requisitions/new" },
    ],
  },
  {
    title: "Purchase orders",
    description: "Orders to vendors, priced from trade agreements, with totals that balance to the cent.",
    links: [
      { label: "All purchase orders", to: "/purchasing/purchase-orders" },
      { label: "New purchase order", to: "/purchasing/purchase-orders/new" },
    ],
  },
  {
    title: "Vendor pricing",
    description: "Trade agreement price rows and the lookup that explains which row wins.",
    links: [
      { label: "Trade agreements", to: "/purchasing/trade-agreements" },
      { label: "Price lookup", to: "/purchasing/pricing" },
    ],
  },
];

/** Procurement workspace (FR-17.14 tiles available in this slice) and links to each area. */
export function WorkspacePage() {
  const counts = useResource(
    (signal) =>
      Promise.all([
        listRequisitions({ status: "IN_REVIEW" }, { limit: COUNT_LIMIT, signal }),
        listRequisitions({ status: "APPROVED" }, { limit: COUNT_LIMIT, signal }),
        listPurchaseOrders({ approvalStatus: "DRAFT" }, { limit: COUNT_LIMIT, signal }),
        listTradeAgreements({ status: "ACTIVE" }, { limit: COUNT_LIMIT, signal }),
      ]),
    [],
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Procurement workspace" description="Requisitions, purchase orders and vendor pricing in one place." />
      {counts.error ? <LoadError what="workspace counts" error={counts.error} onRetry={counts.reload} /> : null}
      {counts.loading ? (
        <div aria-busy="true">
          <Skeleton height="tile" />
          <span className="visually-hidden">Loading…</span>
        </div>
      ) : counts.data ? (
        <StatGroup label="Procurement tiles">
          <StatTile label="Requisitions in review" value={countLabel(counts.data[0])} />
          <StatTile label="Approved requisitions to source" value={countLabel(counts.data[1])} />
          <StatTile label="Draft purchase orders" value={countLabel(counts.data[2])} />
          <StatTile label="Active price rows" value={countLabel(counts.data[3])} />
        </StatGroup>
      ) : null}
      <CardGrid as="ul" label="Procurement areas">
        {sections.map((s) => (
          <li key={s.title}>
            <Card title={s.title}>
              <p className={styles.muted}>{s.description}</p>
              <ul className={styles.cardLinks}>
                {s.links.map((l) => (
                  <li key={l.to}>
                    <Link className={styles.link} to={l.to}>
                      {l.label}
                    </Link>
                  </li>
                ))}
              </ul>
            </Card>
          </li>
        ))}
      </CardGrid>
    </div>
  );
}
