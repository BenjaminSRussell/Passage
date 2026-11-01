import { Link, useParams, useSearchParams } from "react-router-dom";
import { Alert, Badge, buttonClassName, Button, Card, PageHeader, type DataTableColumn } from "../../shell/ui";
import { advanceIncident, getIncident, listIncidents, type Incident, type IncidentStatus } from "./api/safety";
import { dash, formatUtc } from "./format";
import styles from "./pages.module.css";
import { BackLink, ConfirmAction, Entry, Facts, humanize, ListPage, Loaded, SelectField, Status, labelled, useAction } from "./parts";
import { useLoad } from "./useLoad";

const STATUSES: IncidentStatus[] = ["REPORTED", "UNDER_REVIEW", "CLOSED"];

export function WhoLabel({ incident }: { incident: Incident }) {
  if (incident.privacyCase && !incident.workerName && !incident.workerId) return <>Privacy case</>;
  return <>{incident.workerName ?? incident.workerId ?? dash}</>;
}

const columns: DataTableColumn<Incident>[] = [
  { key: "caseNo", header: "Case", mono: true, align: "end", render: (row) => <Link to={`/hr/incidents/${row.id}`}>{`#${row.caseNo}`}</Link> },
  { key: "who", header: "Worker", render: (row) => <WhoLabel incident={row} /> },
  { key: "incidentType", header: "Type", render: (row) => humanize(row.incidentType) },
  { key: "occurredAt", header: "Occurred", mono: true, render: (row) => formatUtc(row.occurredAt) },
  { key: "outcome", header: "Outcome", render: (row) => humanize(row.outcome) },
  { key: "recordable", header: "OSHA", render: (row) => (row.oshaRecordable ? <Badge tone="warning">Recordable</Badge> : "Not recordable") },
  { key: "status", header: "Status", render: (row) => <Status value={row.status} /> },
];

/** The incident register, newest case first. The worker of a privacy case is hidden by the service. */
export function IncidentsPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("status") ?? "";
  const status = STATUSES.includes(raw as IncidentStatus) ? raw : "";
  return (
    <ListPage<Incident>
      title="Incident register"
      description="Injuries and illnesses reported at work. Medical details open only on a case's own page, and each opening is audited."
      label="Incidents"
      noun="incidents"
      load={(cursor, signal) => listIncidents({ status, cursor }, signal)}
      loadKey={`incidents|${status}`}
      columns={columns}
      getRowKey={(row) => row.id}
      empty={{ title: "No incidents", description: status ? "Try another status." : "Reported incidents appear here." }}
      actions={
        <>
          <Link className={buttonClassName("secondary")} to="/hr/incidents/osha">
            OSHA logs
          </Link>
          <Link className={buttonClassName("primary")} to="/hr/incidents/new">
            Report incident
          </Link>
        </>
      }
      toolbar={
        <SelectField
          id="incident-status"
          label="Status"
          value={status}
          onChange={(value) => setParams(value ? { status: value } : {}, { replace: true })}
          options={labelled(STATUSES)}
          placeholder="All"
        />
      }
    />
  );
}

function List({ label, items }: { label: string; items?: string[] }) {
  if (!items || items.length === 0) return null;
  return (
    <Entry term={label}>
      <ul className={styles.warnings}>
        {items.map((item, i) => (
          <li key={i}>{item}</li>
        ))}
      </ul>
    </Entry>
  );
}

export function IncidentDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getIncident(id, signal), id);
  const action = useAction(state.reload);

  return (
    <Loaded state={state} title="Incident" what="incident" backTo="/hr/incidents">
      {(incident) => {
        const details = incident.details;
        return (
          <div className={styles.stack}>
            <PageHeader
              title={`Incident #${incident.caseNo}`}
              meta={<Status value={incident.status} />}
              actions={<BackLink to="/hr/incidents">Back to the register</BackLink>}
            />
            {action.feedback}
            {incident.privacyCase ? (
              <Alert tone="info" title="Privacy case">
                The worker's name is withheld on the OSHA logs and in lists.
              </Alert>
            ) : null}
            <Card
              title="Incident"
              actions={
                <div className={styles.rowActions}>
                  {incident.status === "REPORTED" ? (
                    <Button
                      variant="primary"
                      loading={action.busy === "UNDER_REVIEW"}
                      onClick={() => void action.run("UNDER_REVIEW", () => advanceIncident(incident.id, "UNDER_REVIEW"), "The incident is under review.")}
                    >
                      Start review
                    </Button>
                  ) : null}
                  {incident.status !== "CLOSED" ? (
                    <ConfirmAction
                      label="Close incident"
                      variant="primary"
                      title="Close this incident?"
                      description="A closed incident is immutable; a correction is reported as a new incident that supersedes it. Whoever reported it can't close it."
                      confirmLabel="Close incident"
                      busy={action.busy === "CLOSED"}
                      onConfirm={() => void action.run("CLOSED", () => advanceIncident(incident.id, "CLOSED"), "The incident was closed.")}
                    />
                  ) : null}
                </div>
              }
            >
              <Facts>
                <Entry term="Worker">
                  <WhoLabel incident={incident} />
                </Entry>
                <Entry term="Type">{humanize(incident.incidentType)}</Entry>
                <Entry term="Occurred" mono>
                  {formatUtc(incident.occurredAt)}
                </Entry>
                <Entry term="Reported" mono>
                  {formatUtc(incident.reportedAt)}
                </Entry>
                <Entry term="Location">{incident.location}</Entry>
                <Entry term="Body parts">{incident.bodyParts && incident.bodyParts.length > 0 ? incident.bodyParts.map(humanize).join(", ") : dash}</Entry>
                <Entry term="Outcome">{humanize(incident.outcome)}</Entry>
                <Entry term="Days away / restricted" mono>{`${incident.daysAway ?? 0} / ${incident.daysRestricted ?? 0}`}</Entry>
                <Entry term="OSHA recordable">{incident.oshaRecordable ? "Yes" : "No"}</Entry>
                <Entry term="Reported by" mono>
                  {incident.reportedBy}
                </Entry>
                {incident.supersedesId ? (
                  <Entry term="Corrects">
                    <Link to={`/hr/incidents/${incident.supersedesId}`}>Open the corrected incident</Link>
                  </Entry>
                ) : null}
                {incident.closedAt ? (
                  <Entry term="Closed" mono>
                    {formatUtc(incident.closedAt)}
                  </Entry>
                ) : null}
              </Facts>
            </Card>
            <Card title="Medical details">
              {details ? (
                <Facts>
                  {details.description ? <Entry term="What happened">{details.description}</Entry> : null}
                  <List label="Witnesses" items={details.witnesses} />
                  <List label="Treatments" items={details.treatments} />
                  {details.rootCause ? <Entry term="Root cause">{details.rootCause}</Entry> : null}
                  {details.correctiveAction ? <Entry term="Corrective action">{details.correctiveAction}</Entry> : null}
                  {details.overrideReason ? <Entry term="Recordable override reason">{details.overrideReason}</Entry> : null}
                </Facts>
              ) : (
                <p className={styles.note}>The service returned no medical details for this case.</p>
              )}
            </Card>
          </div>
        );
      }}
    </Loaded>
  );
}
