import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listSalesGroups, type SalesGroup } from "./api/client";
import { describeReps } from "./commissionFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const columns: DataTableColumn<SalesGroup>[] = [
  { key: "name", header: "Sales group" },
  { key: "reps", header: "Reps and shares", mono: true, render: (row) => describeReps(row.reps) },
  { key: "legalEntityId", header: "Legal entity", render: (row) => <EntityName entity="legalEntity" id={row.legalEntityId} /> },
];

/** Sales groups (FR-26.18; AX CommissionSalesGroup): the reps who share the commission of an order. */
export function SalesGroupsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listSalesGroups("", signal), "all");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Sales groups"
        description="Each group splits its commission across reps by share. Rules on the group set the rate per customer and item commission group."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/pricing/commission/transactions">
              Commission transactions
            </Link>
            <Link className={buttonClassName("primary")} to="/pricing/commission/sales-groups/new">
              New sales group
            </Link>
          </>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the sales groups" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Sales groups"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No sales groups", description: "Create a sales group with its reps before invoices can post commission." }}
          onRowOpen={(row) => navigate(`/pricing/commission/sales-groups/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 sales groups.</p> : null}
    </div>
  );
}
