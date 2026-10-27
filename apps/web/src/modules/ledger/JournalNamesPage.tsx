import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { fetchJournalNames, type JournalName } from "./api/client";
import styles from "./pages.module.css";

const yesNo = (b: boolean) => (b ? "Yes" : "No");

const columns: DataTableColumn<JournalName>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "requiresApproval", header: "Approval", render: (n) => (n.requiresApproval ? (n.selfApprovalAllowed ? "Required (self-approval allowed)" : "Required") : "None") },
  { key: "reversalReasonRequired", header: "Reversal reason", render: (n) => yesNo(n.reversalReasonRequired) },
  { key: "allowReconAccounts", header: "Reconciliation accounts", render: (n) => yesNo(n.allowReconAccounts) },
  { key: "linesLimit", header: "Lines limit", align: "end" },
  { key: "active", header: "Status", render: (n) => <StatusPill status={n.active ? "active" : "inactive"} /> },
];

/** Journal names: the setup every manual journal batch is created under (FR-11.9). */
export function JournalNamesPage() {
  const navigate = useNavigate();
  const result = useQuery({ queryKey: ["ledger", "journal-names"], queryFn: () => fetchJournalNames() });

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Journal names"
        description="A journal name decides whether its batches need approval, who may approve them, whether reversing their entries needs a reason, and how many lines a batch may have."
        actions={
          <Link className={buttonClassName("primary")} to="/ledger/journal-names/new">
            New journal name
          </Link>
        }
      />
      {result.isError ? (
        <Alert tone="danger" title="Could not load the journal names" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : (
        <DataTable
          label="Journal names"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(n) => n.code}
          loading={result.isPending}
          empty={{ title: "No journal names yet", description: "Create a journal name, such as GJ for the general journal, before you enter journal batches." }}
          onRowOpen={(n) => navigate(`/ledger/journal-names/${encodeURIComponent(n.code)}`)}
        />
      )}
      <p className={styles.note}>Names are deactivated, never deleted, because batches keep the name they were entered under.</p>
    </div>
  );
}
