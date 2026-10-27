import { Link } from "react-router-dom";
import { Card, CardGrid, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const areas = [
  {
    title: "Rate shop",
    to: "/transport/rate-shop",
    text: "Rate a shipment across carrier services and compare totals with the explain trace for every charge.",
  },
  {
    title: "Rate cards",
    to: "/transport/rate-cards",
    text: "Effective-dated carrier price lists: lanes, weight breaks, minimum charge, accessorials and fuel surcharge.",
  },
  {
    title: "Freight bills",
    to: "/transport/freight-bills",
    text: "Expected charges per load, matched line by line against the carrier's invoice.",
  },
  {
    title: "Carrier invoices",
    to: "/transport/carrier-invoices",
    text: "Register invoices from EDI 210, the carrier API or by hand; invoices without a bill wait in the exception queue.",
  },
];

/** Transportation workspace landing page (PRD 6.34.6). */
export function TransportHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Transportation"
        description="Rate carriers, keep their rate cards, and reconcile carrier invoices against the freight you expected to pay. Load planning, tendering and tracking come in later releases."
      />
      <CardGrid as="ul" label="Transportation areas">
        {areas.map((area) => (
          <li key={area.to}>
            <Card title={area.title}>
              <p className={styles.note}>{area.text}</p>
              <Link to={area.to}>Open {area.title.toLowerCase()}</Link>
            </Card>
          </li>
        ))}
      </CardGrid>
    </div>
  );
}
