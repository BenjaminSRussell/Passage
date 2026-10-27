import { useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getAgreement, type AgreementLine } from "./api/client";
import { Details } from "./Details";
import { dash, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Row = AgreementLine & { id: string };

const yes = (on: boolean) => (on ? <Badge tone="success">Covered</Badge> : <span className={styles.muted}>Billable</span>);

const lineColumns: DataTableColumn<Row>[] = [
  { key: "scope", header: "Covers", mono: true, render: (row) => row.serviceObjectId ?? row.categoryId ?? "Any object" },
  { key: "includedVisits", header: "Visits", align: "end" },
  { key: "includedHours", header: "Hours", align: "end" },
  { key: "partsCovered", header: "Parts", render: (row) => yes(row.partsCovered) },
  { key: "laborCovered", header: "Labor", render: (row) => yes(row.laborCovered) },
  { key: "travelCovered", header: "Travel", render: (row) => yes(row.travelCovered) },
];

/** One agreement: validity, SLA profile, billing, lines and entitlement usage (PRD 8.45.6 "Agreement detail"). */
export function AgreementDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAgreement(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Service agreement" />
        <Alert tone="danger" title="Could not load the agreement" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  const a = state.data;
  if (!a) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Service agreement" />
        <span className="visually-hidden">Loading the agreement…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader title={`Service agreement ${a.number}`} meta={<StatusPill status={a.status} />} />
      <Card title="Agreement" headingLevel={3}>
        <Details
          label="Agreement"
          items={[
            ["Customer", a.customerPartyId, true],
            ["Legal entity", a.legalEntityId, true],
            ["Valid from", a.validFrom, true],
            ["Valid to", a.validTo ?? dash, true],
            ["SLA profile", a.slaProfileId, true],
            ["Billing model", sentence(a.billingModel)],
            ["Priority rank", a.priorityRank],
            ["Included hours used", a.hoursConsumed],
          ]}
        />
      </Card>
      <Card title="Lines" headingLevel={3}>
        <DataTable
          label="Agreement lines"
          columns={lineColumns}
          rows={a.lines.map((l, i) => ({ ...l, id: String(i) }))}
          getRowKey={(row) => row.id}
          empty={{ title: "No lines" }}
        />
      </Card>
    </div>
  );
}
