import { Link } from "react-router-dom";
import { Card, CardGrid, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const areas = [
  {
    title: "Loads",
    to: "/transport/loads",
    text: "Consolidate shipments into loads by rule within weight, volume and pallet capacity, check their dangerous goods and screen their parties.",
  },
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
  {
    title: "Tenders",
    to: "/transport/tenders",
    text: "Offer loads to carriers in route-guide order; declines and missed deadlines move the load to the next carrier.",
  },
  {
    title: "Tracking",
    to: "/transport/tracking",
    text: "Status reports from EDI 214, carrier APIs and drivers, and each load's stop-by-stop timeline.",
  },
  {
    title: "Landed cost voyages",
    to: "/transport/voyages",
    text: "Containers, folios and the freight, duty and other costs of inbound shipments, allocated to receipt lines.",
  },
];

/** Transportation workspace landing page (PRD 6.34.6). */
export function TransportHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Transportation"
        description="Build, rate and tender loads to carriers, track them to delivery, reconcile carrier invoices against the freight you expected to pay,, and put landed cost onto inventory. Route optimization, yard and gate come in a later release."
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
