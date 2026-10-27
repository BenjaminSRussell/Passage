import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getCase, listWorkOrders, type WorkOrder } from "./api/client";
import { PriorityBadge, SlaBadge } from "./badges";
import { Details } from "./Details";
import { dash, duration, formatDateTime, sentence, shortId } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const orderColumns: DataTableColumn<WorkOrder>[] = [
  { key: "number", header: "Work order", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "estDurationMinutes", header: "Estimate", align: "end", render: (row) => duration(row.estDurationMinutes) },
  { key: "lines", header: "Lines", align: "end", render: (row) => row.lines.length },
];

/** One case: SLA timers, the agreement it resolved to and why, and its work orders (PRD 8.45.6 "Case detail"). */
export function CaseDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getCase(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Service case" />
        <Alert tone="danger" title="Could not load the case" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  const c = state.data;
  if (!c) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Service case" />
        <span className="visually-hidden">Loading the case…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Service case ${c.number}`}
        description={c.description || undefined}
        meta={<StatusPill status={c.status} />}
        actions={
          <Link className={buttonClassName("primary")} to={`/service/work-orders/new?caseId=${encodeURIComponent(c.id)}`}>
            Create work order
          </Link>
        }
      />
      <Card title="SLA clocks" headingLevel={3}>
        <Details
          label="SLA clocks"
          items={[
            ["SLA state", <SlaBadge key="s" state={c.slaState} />],
            ["Opened", formatDateTime(c.openedAt), true],
            ["Response due", formatDateTime(c.slaResponseDue), true],
            ["Resolution due", formatDateTime(c.slaResolutionDue), true],
            ["Paused", duration(c.slaPausedMinutes)],
          ]}
        />
      </Card>
      <Card title="Case" headingLevel={3}>
        <Details
          label="Case"
          items={[
            ["Customer", c.customerPartyId, true],
            ["Legal entity", c.legalEntityId, true],
            ["Service object", c.serviceObjectId ?? dash, true],
            ["Channel", sentence(c.channel)],
            ["Priority", <PriorityBadge key="p" priority={c.priority} />],
          ]}
        />
      </Card>
      <Card title="Entitlement" headingLevel={3}>
        <Details
          label="Entitlement"
          items={[
            ["Agreement", c.agreementId ? <Link to={`/service/agreements/${c.agreementId}`}>{shortId(c.agreementId)}</Link> : "None (billable)"],
            ["SLA profile", c.slaProfileId, true],
          ]}
        />
        {c.entitlementTrace.length > 0 ? (
          <ul className={styles.trace} aria-label="Entitlement trace">
            {c.entitlementTrace.map((line, i) => (
              <li key={i}>{line}</li>
            ))}
          </ul>
        ) : null}
      </Card>
      <Card title="Work orders" headingLevel={3}>
        <PagedTable
          label="Case work orders"
          columns={orderColumns}
          load={(cursor, signal) => listWorkOrders({ caseId: c.id, cursor }, signal)}
          loadKey={c.id}
          empty={{ title: "No work orders for this case", description: "Create one to dispatch a technician." }}
          onRowOpen={(row) => navigate(`/service/work-orders/${row.id}`)}
        />
      </Card>
    </div>
  );
}
