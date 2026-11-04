import { useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, PageHeader, Skeleton, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  FOLLOW_UP_STATUSES,
  followUpWorkOrder,
  getWorkOrder,
  newKey,
  NEXT_STATUSES,
  postWorkOrder,
  transitionWorkOrder,
  type PostedLine,
  type Problem,
  type Versioned,
  type WorkLine,
  type WorkOrder,
  type WorkOrderStatus,
} from "./api/client";
import { ActionLogPanel } from "./ActionLogPanel";
import { ActivityPanel } from "./ActivitiesPage";
import { CoverageBadge } from "./badges";
import { SchedulingPanel } from "./SchedulingPanel";
import { Details } from "./Details";
import { dash, duration, formatDateTime, money, sentence, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const lineColumns: DataTableColumn<Required<WorkLine>>[] = [
  { key: "kind", header: "Kind", render: (row) => sentence(row.kind) },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "unitPrice", header: "Unit price", align: "end" },
  { key: "unitCost", header: "Unit cost", align: "end" },
];

const postedColumns: DataTableColumn<PostedLine & { id: string }>[] = [
  { key: "kind", header: "Kind", render: (row) => sentence(row.kind) },
  { key: "coverage", header: "Coverage", render: (row) => <CoverageBadge coverage={row.coverage} /> },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "amount", header: "Billed", align: "end" },
  { key: "warrantyCost", header: "Warranty cost", align: "end" },
  { key: "agreementHours", header: "Included hours", align: "end" },
];

/** The verb on each status button. */
const actionLabel: Record<WorkOrderStatus, string> = {
  UNSCHEDULED: "Unschedule",
  SCHEDULED: "Schedule",
  DISPATCHED: "Dispatch",
  TRAVELING: "Start travel",
  IN_PROGRESS: "Start work",
  ON_HOLD: "Put on hold",
  COMPLETED: "Complete",
  POSTED: "Post",
  CANCELED: "Cancel work order",
};

/**
 * One work order: lines, status moves (BR-45.5), posting with per-line coverage
 * and a follow-up visit when the first one did not fix it (PRD 8.45.6 "Work order detail").
 */
export function WorkOrderDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getWorkOrder(id, signal), id);
  const [latest, setLatest] = useState<Versioned<WorkOrder> | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string>("");
  const postKey = useRef(newKey());
  const followUpKey = useRef(newKey());
  const navigate = useNavigate();
  const current = latest && latest.value.id === id ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Service work order" />
        <Alert tone="danger" title="Could not load the work order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Service work order" />
        <span className="visually-hidden">Loading the work order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: wo, etag } = current;

  async function run(action: string, call: () => Promise<Versioned<WorkOrder>>, done: (w: WorkOrder) => string) {
    setBusy(action);
    setFailure(null);
    setNotice("");
    try {
      const next = await call();
      setLatest(next);
      setNotice(done(next.value));
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "conflict_version") state.reload();
    } finally {
      setBusy(null);
    }
  }

  async function followUp() {
    setBusy("follow-up");
    setFailure(null);
    setNotice("");
    try {
      const child = await followUpWorkOrder(wo.id, etag, followUpKey.current);
      followUpKey.current = newKey();
      navigate(`/service/work-orders/${child.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code === "conflict_version") state.reload();
    } finally {
      setBusy(null);
    }
  }

  const moves = NEXT_STATUSES[wo.status];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Service work order ${wo.number}`}
        meta={<StatusPill status={wo.status} />}
        actions={
          <div className={styles.actions}>
            {moves.map((to) => (
              <Button
                key={to}
                variant={to === "CANCELED" ? "danger" : "secondary"}
                loading={busy === to}
                onClick={() => run(to, () => transitionWorkOrder(wo.id, etag, to), (w) => `Work order is now ${sentence(w.status).toLowerCase()}.`)}
              >
                {actionLabel[to]}
              </Button>
            ))}
            {wo.status === "COMPLETED" ? (
              <Button
                variant="primary"
                loading={busy === "post"}
                onClick={() => run("post", () => postWorkOrder(wo.id, etag, postKey.current), (w) => `Posted: ${money(w.posting?.billableTotal, w.currency)} billable.`)}
              >
                Post to billing
              </Button>
            ) : null}
            {FOLLOW_UP_STATUSES.includes(wo.status) ? (
              <Button variant="secondary" loading={busy === "follow-up"} onClick={followUp}>
                Open follow-up visit
              </Button>
            ) : null}
          </div>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.code === "conflict_version" ? "Someone else changed this work order" : "The action failed"} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="Work order" headingLevel={3}>
        <Details
          label="Work order"
          items={[
            ["Customer", wo.customerPartyId, true],
            ["Legal entity", wo.legalEntityId, true],
            ["Territory", wo.territoryId, true],
            ["Case", wo.caseId ? <Link to={`/service/cases/${wo.caseId}`}>{shortId(wo.caseId)}</Link> : dash],
            ["Agreement", wo.agreementId ? <Link to={`/service/agreements/${wo.agreementId}`}>{shortId(wo.agreementId)}</Link> : "None (billable)"],
            ["Estimate", duration(wo.estDurationMinutes)],
            ["Currency", wo.currency],
            ["Completed", formatDateTime(wo.completedAt), true],
            ["Signature", wo.signedBy ? `${wo.signedBy}, ${formatDateTime(wo.signedAt)}` : wo.signatureExempt ? "Not required" : "Not captured"],
            ["Technician note", wo.technicianNote || dash],
            ["First-time fix", wo.firstTimeFix === true ? "Yes" : wo.firstTimeFix === false ? "No, a follow-up was needed" : dash],
            ["Follow-up of", wo.followUpOf ? <Link to={`/service/work-orders/${wo.followUpOf}`}>{shortId(wo.followUpOf)}</Link> : dash],
          ]}
        />
      </Card>
      <SchedulingPanel key={wo.id} workOrder={wo} etag={etag} onSaved={setLatest} onConflict={state.reload} />
      <ActionLogPanel key={`actions-${wo.id}`} workOrder={wo} onSynced={setLatest} />
      <Card title="Lines" headingLevel={3}>
        <DataTable label="Work order lines" columns={lineColumns} rows={wo.lines} getRowKey={(row) => row.id} empty={{ title: "No lines", description: "This work order has nothing to bill." }} />
      </Card>
      {wo.posting ? (
        <Card title="Posting" headingLevel={3}>
          <StatGroup label="Posting totals">
            <StatTile label="Billable" value={money(wo.posting.billableTotal, wo.posting.currency)} />
            <StatTile label="Warranty cost" value={money(wo.posting.warrantyCost, wo.posting.currency)} />
            <StatTile label="Included hours used" value={wo.posting.entitlementConsumed} />
          </StatGroup>
          <DataTable
            label="Posted lines"
            columns={postedColumns}
            rows={wo.posting.lines.map((l, i) => ({ ...l, id: `${l.lineId}-${i}` }))}
            getRowKey={(row) => row.id}
            empty={{ title: "No posted lines" }}
          />
        </Card>
      ) : null}
      <ActivityPanel entityType="work_order" entityId={wo.id} />
    </div>
  );
}
