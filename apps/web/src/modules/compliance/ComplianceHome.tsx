import { Link } from "react-router-dom";
import { Card, CardGrid, Icon, PageHeader } from "../../shell/ui";
import styles from "./pages.module.css";

const areas = [
  { to: "/compliance/sod/conflicts", title: "SoD conflicts", link: "Review conflicts", text: "Review conflicts, approve compensating controls and accept low and medium risks." },
  { to: "/compliance/sod/check", title: "Role assignment check", link: "Check an assignment", text: "Simulate a role assignment before you save it, or record an evaluation of a user's roles." },
  { to: "/compliance/sod/rules", title: "SoD rules", link: "See the rules", text: "The duty pairs the engine keeps apart, with severity and mitigation limits." },
  { to: "/compliance/audit/run", title: "Audit rules", link: "Run an audit rule", text: "Check expenses, invoices, orders or journals for duplicates, keywords, watch-list parties, limits and weekend postings." },
  { to: "/compliance/audit/cases", title: "Audit cases", link: "Work with cases", text: "Group audit violations into cases and check a review, resolution, close or archive step." },
  { to: "/compliance/break-glass", title: "Break-glass access", link: "Plan a session", text: "Plan a time-boxed emergency session with its approval path and review deadline." },
  { to: "/compliance/incidents/breach-clocks", title: "Breach notification clocks", link: "Compute clocks", text: "See who must be told about a personal-data breach, and by when, under each regime." },
  { to: "/compliance/screening/hits", title: "Sanctions screening", link: "Review screening hits", text: "Clear or confirm parties and documents that match a sanctions or denied-party list, or screen one on demand." },
  { to: "/compliance/export-checks", title: "Export license check", link: "Check a delivery", text: "See whether each line of a delivery needs an export license and whether one on file covers it." },
  { to: "/compliance/privacy/requests", title: "Privacy requests", link: "Work the requests", text: "Verify, discover, approve, deliver and close GDPR and CCPA subject requests against their deadlines, with legal holds stopping erasure." },
  { to: "/compliance/campaigns", title: "Certification campaign", link: "Plan a campaign", text: "Set a campaign window and see its progress and what closing it revokes or flags." },
  { to: "/compliance/incidents/step", title: "Incident steps", link: "Check a step", text: "Check an incident's next lifecycle step and whether its breach notifications allow it to close." },
  { to: "/compliance/export-policy", title: "Export policy", link: "Check a download", text: "See what the policy for a data classification does to a download, and which fields a read logs." },
  { to: "/compliance/packs", title: "Country packs", link: "Browse country packs", text: "See what each country pack ships and check whether it can be installed on a legal entity." },
  { to: "/compliance/tds", title: "India TDS", link: "Calculate TDS", text: "Work out the tax to deduct at source on a payment under the India pack." },
];

/** Compliance workspace overview: one card per area this slice ships. */
export function ComplianceHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Compliance workspace"
        description="Segregation of duties, audit rules and cases, break-glass access, breach notification, trade compliance and country localization. Incident records, evidence collection, signatures, stored audit runs and cases, legal holds, stored certification campaigns and classification and license maintenance arrive in later releases."
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
