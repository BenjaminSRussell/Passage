import { Link } from "react-router-dom";
import { Card, CardGrid, Icon, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const areas = [
  { to: "/compliance/sod/conflicts", title: "SoD conflicts", link: "Review conflicts", text: "Review conflicts, approve compensating controls and accept low and medium risks." },
  { to: "/compliance/sod/check", title: "Role assignment check", link: "Check an assignment", text: "Simulate a role assignment before you save it, or record an evaluation of a user's roles." },
  { to: "/compliance/sod/rules", title: "SoD rules", link: "See the rules", text: "The duty pairs the engine keeps apart, with severity and mitigation limits." },
  { to: "/compliance/incidents/breach-clocks", title: "Breach notification clocks", link: "Compute clocks", text: "See who must be told about a personal-data breach, and by when, under each regime." },
  { to: "/compliance/packs", title: "Country packs", link: "Browse country packs", text: "See what each country pack ships and check whether it can be installed on a legal entity." },
  { to: "/compliance/tds", title: "India TDS", link: "Calculate TDS", text: "Work out the tax to deduct at source on a payment under the India pack." },
];

/** Compliance workspace overview: one card per area this slice ships. */
export function ComplianceHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Compliance workspace"
        description="Segregation of duties, breach notification and country localization. Incident records, evidence collection, signatures, audit policies, privacy requests and screening arrive in later releases."
      />
      <CardGrid as="ul" label="Compliance areas">
        {areas.map((a) => (
          <li key={a.to}>
            <Card title={a.title}>
              <p className={styles.note}>{a.text}</p>
              <ul className={styles.links}>
                <li>
                  <Link to={a.to}>
                    {a.link} <Icon name="chevronRight" size={16} />
                  </Link>
                </li>
              </ul>
            </Card>
          </li>
        ))}
      </CardGrid>
    </div>
  );
}
