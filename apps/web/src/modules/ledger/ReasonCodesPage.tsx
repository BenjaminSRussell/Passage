import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { fetchReasonCodes, type ReasonCode } from "./api/client";
import styles from "./pages.module.css";

/** Display label of an applies_to module value. */
export function moduleLabel(m: string): string {
  const s = m.replace(/_/g, " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

const yesNo = (b: boolean) => (b ? "Yes" : "No");

const columns: DataTableColumn<ReasonCode>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "appliesTo", header: "Applies to", render: (r) => r.appliesTo.map(moduleLabel).join(", ") },
  { key: "requiresComment", header: "Comment required", render: (r) => yesNo(r.requiresComment) },
  { key: "negativePostingAllowed", header: "Negative posting", render: (r) => yesNo(r.negativePostingAllowed) },
  { key: "alternativePostingDateAllowed", header: "Other reversal date", render: (r) => yesNo(r.alternativePostingDateAllowed) },
  { key: "active", header: "Status", render: (r) => <StatusPill status={r.active ? "active" : "inactive"} /> },
];

/** Shared financial reason codes of the tenant (FR-11.15). */
export function ReasonCodesPage() {
  const navigate = useNavigate();
  const result = useQuery({ queryKey: ["ledger", "reason-codes"], queryFn: () => fetchReasonCodes() });

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Reason codes"
        description="One shared list for the ledger, bank, customers, vendors, fixed assets, counting, tax and projects. Reversals pick a code that applies to the ledger."
        actions={
          <Link className={buttonClassName("primary")} to="/ledger/reason-codes/new">
            New reason code
          </Link>
        }
      />
      {result.isError ? (
        <Alert tone="danger" title="Could not load the reason codes" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : (
        <DataTable
          label="Reason codes"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(r) => r.code}
          loading={result.isPending}
          empty={{ title: "No reason codes yet", description: "Create a reason code before you reverse entries on a ledger that requires one." }}
          onRowOpen={(r) => navigate(`/ledger/reason-codes/${encodeURIComponent(r.code)}`)}
        />
      )}
      <p className={styles.note}>Codes are deactivated, never deleted, because posted reversals keep their reason.</p>
    </div>
  );
}
