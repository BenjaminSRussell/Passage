import { useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  CASE_NEXT,
  describeProblem,
  getCase,
  listWorkOrders,
  newKey,
  OPEN_CASE_STATUSES,
  transitionCase,
  type CaseStatus,
  type Problem,
  type ServiceCase,
  type SlaAlert,
  type WorkOrder,
} from "./api/client";
import { ActivityPanel } from "./ActivitiesPage";
import { PriorityBadge, SlaBadge } from "./badges";
import { Details } from "./Details";
import { dash, duration, formatDateTime, sentence, shortId } from "./format";
import { ArticleSuggestions } from "./KnowledgePage";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const orderColumns: DataTableColumn<WorkOrder>[] = [
  { key: "number", header: "Work order", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "estDurationMinutes", header: "Estimate", align: "end", render: (row) => duration(row.estDurationMinutes) },
  { key: "lines", header: "Lines", align: "end", render: (row) => row.lines.length },
];

/** The verb on each case status button (PRD 8.45.6 "Case detail": pause/resume, resolve). */
function actionLabel(from: CaseStatus, to: CaseStatus): string {
  switch (to) {
    case "IN_PROGRESS":
      return from === "NEW" ? "Start work" : "Resume";
    case "WAITING_CUSTOMER":
      return "Wait for customer";
    case "WAITING_PARTS":
      return "Wait for parts";
    case "RESOLVED":
      return "Resolve";
    case "CLOSED":
      return "Close";
    case "CANCELED":
      return "Cancel case";
    default:
      return sentence(to);
  }
}

const alertText: Record<SlaAlert, string> = {
  RESPONSE_WARNING: "Response at 75 %",
  RESPONSE_BREACH: "Response breached",
  RESOLUTION_WARNING: "Resolution at 75 %",
  RESOLUTION_BREACH: "Resolution breached",
};

/** Moves that end the case for good, so the page asks first. */
const finalMoves: Partial<Record<CaseStatus, { title: string; description: string }>> = {
  CANCELED: { title: "Cancel this case?", description: "A canceled case is final: its SLA clocks stop and it cannot be reopened." },
  CLOSED: { title: "Close this case?", description: "A closed case is final and cannot be reopened." },
};

/** One case: SLA timers, status moves that pause and resume them, the agreement it resolved to and why, and its work orders (PRD 8.45.6 "Case detail"). */
export function CaseDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getCase(id, signal), id);
  const [latest, setLatest] = useState<ServiceCase | null>(null);
  const [busy, setBusy] = useState<CaseStatus | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [confirming, setConfirming] = useState<CaseStatus | null>(null);
  // One key per intent: a retry of the same move replays instead of failing on the version.
  const moveKey = useRef(newKey());

  const c = latest && latest.id === id && (!state.data || latest.version >= state.data.version) ? latest : state.data;

  if (state.error && !c) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Service case" />
        <Alert tone="danger" title="Could not load the case" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!c) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Service case" />
        <span className="visually-hidden">Loading the case…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  async function move(current: ServiceCase, to: CaseStatus) {
    setBusy(to);
    setFailure(null);
    setNotice("");
    try {
      const next = await transitionCase(current.id, current.version, to, moveKey.current);
      moveKey.current = newKey();
      setLatest(next);
      setNotice(`Case is now ${sentence(next.status).toLowerCase()}; SLA ${sentence(next.slaState).toLowerCase()}.`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "conflict_version") {
        moveKey.current = newKey();
        setLatest(null);
        state.reload();
      }
    } finally {
      setBusy(null);
    }
  }

  const open = OPEN_CASE_STATUSES.includes(c.status);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Service case ${c.number}`}
        description={c.description || undefined}
        meta={<StatusPill status={c.status} />}
        actions={
          <div className={styles.actions}>
            {CASE_NEXT[c.status].map((to) => (
              <Button
                key={to}
                variant={to === "CANCELED" ? "danger" : to === "RESOLVED" ? "primary" : "secondary"}
                loading={busy === to}
                disabled={busy !== null && busy !== to}
                onClick={() => (finalMoves[to] ? setConfirming(to) : move(c, to))}
              >
                {actionLabel(c.status, to)}
              </Button>
            ))}
            {open ? (
              <Link className={buttonClassName("secondary")} to={`/service/work-orders/new?caseId=${encodeURIComponent(c.id)}`}>
                Create work order
              </Link>
            ) : null}
          </div>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.code === "conflict_version" ? "Someone else changed this case; it was reloaded" : "The status change failed"} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="SLA clocks" headingLevel={3}>
        <Details
          label="SLA clocks"
          items={[
            ["SLA state", <SlaBadge key="s" state={c.slaState} />],
            ["Opened", formatDateTime(c.openedAt), true],
            ["Response due", formatDateTime(c.slaResponseDue), true],
            ["Responded", formatDateTime(c.respondedAt), true],
            ["Resolution due", formatDateTime(c.slaResolutionDue), true],
            ["Resolution warning", formatDateTime(c.slaResolutionWarnAt), true],
            ["Resolved", formatDateTime(c.resolvedAt), true],
            ["Paused", c.slaPausedAt ? `Since ${formatDateTime(c.slaPausedAt)}, ${duration(c.slaPausedMinutes)} before` : duration(c.slaPausedMinutes)],
            [
              "SLA alerts",
              (c.slaAlerts ?? []).length > 0 ? (
                <span key="a" className={styles.actions}>
                  {(c.slaAlerts ?? []).map((a) => (
                    <Badge key={a} tone={a.endsWith("BREACH") ? "danger" : "warning"}>
                      {alertText[a] ?? sentence(a)}
                    </Badge>
                  ))}
                </span>
              ) : (
                "None"
              ),
            ],
          ]}
        />
      </Card>
      <Card title="Case" headingLevel={3}>
        <Details
          label="Case"
          items={[
            ["Customer", c.customerPartyId, true],
            ["Legal entity", c.legalEntityId, true],
            ["Service object", c.serviceObjectId ? <Link key="o" to={`/service/objects/${c.serviceObjectId}`}>{shortId(c.serviceObjectId)}</Link> : dash],
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
          empty={{ title: "No work orders for this case", description: open ? "Create one to dispatch a technician." : "The case is no longer open." }}
          onRowOpen={(row) => navigate(`/service/work-orders/${row.id}`)}
        />
      </Card>
      {open ? <ArticleSuggestions description={c.description} serviceObjectId={c.serviceObjectId} /> : null}
      <ActivityPanel entityType="case" entityId={c.id} />
      {confirming && finalMoves[confirming] ? (
        <ConfirmDialog
          title={finalMoves[confirming].title}
          description={finalMoves[confirming].description}
          confirmLabel={actionLabel(c.status, confirming)}
          cancelLabel="Keep the case"
          onConfirm={() => {
            const to = confirming;
            setConfirming(null);
            void move(c, to);
          }}
          onCancel={() => setConfirming(null)}
        />
      ) : null}
    </div>
  );
}
