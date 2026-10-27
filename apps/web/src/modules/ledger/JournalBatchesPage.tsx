import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { fetchJournalBatches, type BatchStatus, type JournalBatch } from "./api/client";
import styles from "./pages.module.css";

const statusView: Record<BatchStatus, { label: string; tone: BadgeTone }> = {
  draft: { label: "Draft", tone: "neutral" },
  validated: { label: "Validated", tone: "info" },
  in_approval: { label: "In approval", tone: "warning" },
  approved: { label: "Approved", tone: "info" },
  rejected: { label: "Rejected", tone: "danger" },
  posting: { label: "Posting", tone: "info" },
  posted: { label: "Posted", tone: "success" },
  cancelled: { label: "Cancelled", tone: "neutral" },
};

/** The statuses a user filters by (posting is never stored). */
export const FILTER_STATUSES: BatchStatus[] = ["draft", "validated", "in_approval", "approved", "rejected", "posted", "cancelled"];

export function batchStatusLabel(status: BatchStatus): string {
  return statusView[status].label;
}

/** Status of a journal batch (BR-11.10) as a badge. */
export function BatchStatusBadge({ status }: { status: BatchStatus }) {
  const view = statusView[status];
  return <Badge tone={view.tone}>{view.label}</Badge>;
}

const columns: DataTableColumn<JournalBatch>[] = [
  { key: "journalName", header: "Journal", mono: true },
  { key: "description", header: "Description", render: (b) => b.description || "Default description" },
  { key: "postingDate", header: "Posting date" },
  { key: "status", header: "Status", render: (b) => <BatchStatusBadge status={b.status} /> },
  { key: "totalDebit", header: "Debit", align: "end", render: (b) => `${b.totalDebit} ${b.currency}` },
  { key: "totalCredit", header: "Credit", align: "end", render: (b) => `${b.totalCredit} ${b.currency}` },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "createdBy", header: "Owner" },
];

/** Journals list (§2.11.6): the newest batches, filtered by status. */
export function JournalBatchesPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const result = useQuery({ queryKey: ["ledger", "journal-batches", status], queryFn: () => fetchJournalBatches({ status }) });

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Journal batches"
        description="Manual journals: enter lines, validate or submit for approval, then post them to the ledger as one entry."
        actions={
          <Link className={buttonClassName("primary")} to="/ledger/journal-batches/new">
            New batch
          </Link>
        }
      />
      <Card>
        <div className={styles.form}>
          <FormField id="jb-status" label="Status">
            {(c) => (
              <Select {...c} value={status} onChange={(e) => setParams(e.target.value ? { status: e.target.value } : {})}>
                <option value="">All statuses</option>
                {FILTER_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {batchStatusLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      {result.isError ? (
        <Alert tone="danger" title="Could not load the journal batches" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : (
        <DataTable
          label="Journal batches"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(b) => b.id}
          loading={result.isPending}
          empty={
            status
              ? { title: `No ${batchStatusLabel(status as BatchStatus).toLowerCase()} batches`, description: "Pick another status to see more batches." }
              : { title: "No journal batches yet", description: "Create a batch to enter a manual journal." }
          }
          onRowOpen={(b) => navigate(`/ledger/journal-batches/${b.id}`)}
        />
      )}
      <p className={styles.note}>Shows the newest 200 batches.</p>
    </div>
  );
}
