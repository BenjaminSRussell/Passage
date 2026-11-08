import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Button, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listRequisitionApprovals, type RequisitionApproval } from "./api/client";
import { LoadError, ShortId, money, useResource } from "./parts";
import styles from "./pages.module.css";

export const approverRoleLabels: Record<string, string> = {
  MANAGER: "Manager",
  CATEGORY_OWNER: "Category owner",
  CFO: "CFO",
  NEXT_APPROVER: "Next approver",
};

const ROLES = ["MANAGER", "CATEGORY_OWNER", "CFO", "NEXT_APPROVER"];

const columns: DataTableColumn<RequisitionApproval>[] = [
  { key: "number", header: "Requisition", mono: true },
  { key: "nextApprover", header: "Waiting for", render: (row) => approverRoleLabels[row.nextApprover] ?? row.nextApprover },
  { key: "approvedSteps", header: "Level", align: "end", render: (row) => `${row.approvedSteps + 1} of ${row.chainLength}` },
  { key: "total", header: "Estimated total", align: "end", render: (row) => money(row.total, row.currency) },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "requesterId", header: "Requester", render: (row) => <ShortId id={row.requesterId} /> },
  { key: "submittedAt", header: "Last change", mono: true, render: (row) => row.submittedAt.slice(0, 10) },
];

/** The approver's inbox: IN_REVIEW requisitions and the role that decides next; your own are not listed. */
export function ApprovalInboxPage() {
  const navigate = useNavigate();
  const [role, setRole] = useState("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<RequisitionApproval[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listRequisitionApprovals({ role }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [role, cursor],
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Requisition approvals"
        description="Requisitions waiting for an approval level. Open one to approve it or send it back with a reason."
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="approval-role-filter" label="Waiting for">
            {(control) => (
              <Select
                {...control}
                value={role}
                onChange={(e) => {
                  setCursor(undefined);
                  setRole(e.target.value);
                }}
              >
                <option value="">Any role</option>
                {ROLES.map((r) => (
                  <option key={r} value={r}>
                    {approverRoleLabels[r]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      </div>
      {page.error ? <LoadError what="the approvals" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Requisitions waiting for approval"
        columns={columns}
        rows={page.error ? [] : rows}
        getRowKey={(row) => row.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "Nothing is waiting for approval", description: "Requisitions that need an approval level appear here." }}
        onRowOpen={(row) => navigate(`/purchasing/requisitions/${row.id}`)}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}
