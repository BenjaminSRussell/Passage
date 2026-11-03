import { Link, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  buttonClassName,
  Card,
  DataTable,
  PageHeader,
  Skeleton,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import { getParty, listPartyVendors, type Vendor } from "./api/client";
import { displayOrDash, formatDateTime, partyTypeLabel } from "./format";
import styles from "./pages.module.css";
import { Entry, ProblemAlert } from "./parts";
import { useLoad } from "./useLoad";

const vendorColumns: DataTableColumn<Vendor>[] = [
  { key: "accountNumber", header: "Account", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true },
  { key: "currencyCode", header: "Currency", mono: true },
  { key: "blocked", header: "Blocked", render: (row) => <Badge tone={row.blocked === "none" ? "success" : "warning"}>{row.blocked}</Badge> },
];

function RolesCard({ partyId }: { partyId: string }) {
  const state = useLoad((signal) => listPartyVendors(partyId, signal), partyId);
  return (
    <Card title="Roles">
      <div className={styles.stack}>
        <p className={styles.note}>
          Vendor roles on this party in the legal entities you can see. Customer roles have a create API but no list yet, so they are not shown here.
        </p>
        {state.error && !state.data ? (
          <ProblemAlert problem={state.error} title="Could not load vendor roles" retry={state.reload} />
        ) : (
          <DataTable
            label="Vendor roles"
            columns={vendorColumns}
            rows={state.data ?? []}
            getRowKey={(row) => row.id}
            loading={state.loading}
            empty={{ title: "No vendor roles", description: "This party has no vendor role in your legal-entity scope." }}
          />
        )}
      </div>
    </Card>
  );
}

/** One party: identity, merge redirect if any, and vendor roles. No merge action (#450). */
export function PartyDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getParty(id, signal), id);

  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader
          title="Party"
          actions={
            <Link className={buttonClassName("ghost")} to="/party">
              Back to parties
            </Link>
          }
        />
        <ProblemAlert problem={state.error} title="Could not load the party" retry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Party" />
        <span className="visually-hidden">Loading the party…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  const party = state.data;
  const person = party.partyType === "person";

  return (
    <div className={styles.stack}>
      <PageHeader
        title={party.name}
        meta={<StatusPill status={party.status} />}
        actions={
          <Link className={buttonClassName("ghost")} to="/party">
            Back to parties
          </Link>
        }
      />
      {party.redirected ? (
        <Alert tone="info" title="This id was merged">
          The id you opened ({party.requestedId}) now points at surviving party {party.partyNumber}. Merge is not offered here.
        </Alert>
      ) : null}
      {party.status === "merged" && party.mergedIntoId ? (
        <Alert tone="warning" title="Merged party">
          This record was merged into{" "}
          <Link to={`/party/${party.mergedIntoId}`}>{party.mergedIntoId}</Link>.
        </Alert>
      ) : null}
      <Card>
        <dl className={styles.details} aria-label="Party">
          <Entry term="Number" mono>
            {party.partyNumber}
          </Entry>
          <Entry term="Type">{partyTypeLabel[party.partyType] ?? party.partyType}</Entry>
          <Entry term="Status">
            <StatusPill status={party.status} />
          </Entry>
          <Entry term="Id" mono>
            {party.id}
          </Entry>
          {person ? (
            <>
              <Entry term="Given name">{displayOrDash(party.givenName)}</Entry>
              <Entry term="Family name">{displayOrDash(party.familyName)}</Entry>
              <Entry term="Date of birth">{displayOrDash(party.dob)}</Entry>
            </>
          ) : (
            <>
              <Entry term="Legal name">{displayOrDash(party.legalName)}</Entry>
              <Entry term="Trade name">{displayOrDash(party.tradeName)}</Entry>
              <Entry term="Registration number" mono>
                {displayOrDash(party.registrationNumber)}
              </Entry>
            </>
          )}
          <Entry term="Updated">{formatDateTime(party.updatedAt)}</Entry>
        </dl>
      </Card>
      <RolesCard partyId={party.id} />
      <p className={styles.note}>
        Duplicate merge is not available on this screen. A merge needs duty party.merge, which is not granted to any role until the approval workflow lands.
      </p>
    </div>
  );
}
