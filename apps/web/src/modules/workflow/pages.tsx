import { Badge, Card, DataTable, PageHeader, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import styles from "./pages.module.css";

const sampleDataBadge = <Badge tone="neutral">Sample data</Badge>;

export function WorkflowHome({
  counts = { pending: 4, running: 1, failed: 0 },
}: {
  counts?: { pending: number; running: number; failed: number };
}) {
  return (
    <>
      <PageHeader title="Workflow and batch" description="Approvals, batch jobs and message templates." meta={sampleDataBadge} />
      <StatGroup label="Workflow tiles">
        <StatTile label="Pending work items" value={counts.pending} />
        <StatTile label="Running jobs" value={counts.running} />
        <StatTile label="Failed runs" value={counts.failed} tone="danger" />
      </StatGroup>
    </>
  );
}

interface WorkItem {
  document: string;
  step: string;
  submittedBy: string;
  due: string;
  status: string;
}

const workItemColumns: DataTableColumn<WorkItem>[] = [
  { key: "document", header: "Document", mono: true },
  { key: "step", header: "Step" },
  { key: "submittedBy", header: "Submitted by" },
  {
    key: "due",
    header: "Due",
    render: (row) => (row.due === "overdue" ? <Badge tone="warning">Overdue</Badge> : row.due),
  },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

export function InboxPage({
  items = [
    { document: "PR-1042", step: "Director", submittedBy: "alice", due: "in 3 hours", status: "pending" },
    { document: "PR-1043", step: "Controller", submittedBy: "bob", due: "overdue", status: "escalated" },
  ],
}: {
  items?: WorkItem[];
}) {
  return (
    <>
      <PageHeader title="Inbox" description="Approvals assigned to you." meta={sampleDataBadge} />
      <DataTable
        label="Work items"
        columns={workItemColumns}
        rows={items}
        getRowKey={(row) => row.document}
        empty={{ title: "You're all caught up", description: "Approvals assigned to you appear here." }}
      />
    </>
  );
}

const approvalSteps = [
  { name: "Manager role", detail: "4-hour SLA · escalate to finance" },
  { name: "Director user", detail: "4-hour SLA · delegate allowed" },
  { name: "Controller expression", detail: "4-hour SLA · escalate to admin" },
];

export function DesignerPage() {
  return (
    <>
      <PageHeader
        title="Designer"
        description="Amount-band approval chain for purchase requisitions: role, then user, then expression, with a 4-hour SLA."
      />
      <ol className={styles.steps} aria-label="Approval steps">
        {approvalSteps.map((step) => (
          <li key={step.name} className={styles.step}>
            <Card>
              <p className={styles.stepName}>{step.name}</p>
              <p className={styles.stepDetail}>{step.detail}</p>
            </Card>
          </li>
        ))}
      </ol>
    </>
  );
}

interface BatchRun {
  code: string;
  status: string;
  rows: string;
  eta: string;
}

const batchColumns: DataTableColumn<BatchRun>[] = [
  { key: "code", header: "Job", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "rows", header: "Rows", align: "end" },
  { key: "eta", header: "ETA", align: "end" },
];

export function JobMonitorPage({
  runs = [
    { code: "rows-1m", status: "executing", rows: "400000 / 1000000", eta: "2 min" },
    { code: "outbox.sweep", status: "ended", rows: "128 / 128", eta: "—" },
  ],
}: {
  runs?: BatchRun[];
}) {
  return (
    <>
      <PageHeader title="Job monitor" description="Scheduled and on-demand batch runs." meta={sampleDataBadge} />
      <DataTable
        label="Batch runs"
        columns={batchColumns}
        rows={runs}
        getRowKey={(row) => row.code}
        empty={{ title: "No batch runs yet", description: "Scheduled and on-demand jobs appear here as they run." }}
      />
    </>
  );
}

const shippedTemplates = ["workflow.workitem.assigned", "workflow.workitem.escalated", "batch.run.failed"];

export function TemplatesPage() {
  return (
    <>
      <PageHeader title="Templates" description="Message templates that workflows and batch jobs send." />
      <Card padded={false}>
        <ul className={styles.templates} aria-label="Shipped templates">
          {shippedTemplates.map((code) => (
            <li key={code} className={styles.template}>
              <code>{code}</code>
              <Badge tone="neutral">Shipped</Badge>
            </li>
          ))}
        </ul>
      </Card>
    </>
  );
}
