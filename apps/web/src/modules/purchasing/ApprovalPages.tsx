import { useNavigate } from "react-router-dom";
import { ListPage, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { listRequisitionApprovals, type RequisitionApproval } from "./api/client";
import { money, Named, useProcurementPages } from "./parts";

export const approverRoleLabels: Record<string, string> = {
  MANAGER: "Manager",
  CATEGORY_OWNER: "Category owner",
  CFO: "CFO",
  NEXT_APPROVER: "Next approver",
};

const ROLES = ["MANAGER", "CATEGORY_OWNER", "CFO", "NEXT_APPROVER"];

const columns: ListColumn<RequisitionApproval>[] = [
  { key: "number", header: "Requisition", mono: true },
  { key: "nextApprover", header: "Waiting for", render: (row) => approverRoleLabels[row.nextApprover] ?? row.nextApprover },
  { key: "approvedSteps", header: "Level", align: "end", render: (row) => `${row.approvedSteps + 1} of ${row.chainLength}` },
  { key: "total", header: "Estimated total", align: "end", render: (row) => money(row.total, row.currency) },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "requesterId", header: "Requester", render: (row) => <Named entity="worker" id={row.requesterId} /> },
  { key: "submittedAt", header: "Last change", mono: true, render: (row) => row.submittedAt.slice(0, 10) },
];

const filters: ListFilterDef[] = [{ kind: "select", key: "role", label: "Waiting for", anyLabel: "Any role", options: ROLES.map((r) => ({ value: r, label: approverRoleLabels[r] })) }];

function useApprovalRows(query: ListQuery): ListData<RequisitionApproval> {
  const role = query.filters.role ?? "";
  return useProcurementPages(role, (cursor, signal) => listRequisitionApprovals({ role }, { cursor, signal }));
}

/** The approver's inbox: IN_REVIEW requisitions and the role that decides next; your own are not listed. */
export function ApprovalInboxPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="purchasing-requisition-approvals"
      title="Requisition approvals"
      description="Requisitions waiting for an approval level. Open one to approve it or send it back with a reason."
      noun="approvals"
      singular="approval"
      columns={columns}
      filters={filters}
      useRows={useApprovalRows}
      search={(row) => ({ number: row.number })}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/purchasing/requisitions/${row.id}`)}
      empty={{ title: "Nothing is waiting for approval", description: "Requisitions that need an approval level appear here." }}
    />
  );
}
