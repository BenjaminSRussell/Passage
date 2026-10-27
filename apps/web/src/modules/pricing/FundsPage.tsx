import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listFunds, type Fund } from "./api/client";
import { describeScope } from "./fundFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Fund>[] = [
  { key: "name", header: "Fund" },
  { key: "type", header: "Type" },
  { key: "customerScope", header: "Customers", render: (row) => describeScope(row.customerScope) },
  { key: "budgetPerPeriod", header: "Budget per period", mono: true, align: "end", render: (row) => `${row.budgetPerPeriod} ${row.currency}` },
  { key: "allowOverspend", header: "Overspend", render: (row) => (row.allowOverspend ? "Allowed" : "Blocked") },
  { key: "owner", header: "Owner" },
  { key: "closed", header: "Status", render: (row) => <StatusPill status={row.closed ? "closed" : "open"} /> },
];

/** Trade promotion funds (FR-26.19; AX TAMFund): budgets per period that accruals, claims and deductions draw on. */
export function FundsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listFunds("", signal), "all");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Trade funds"
        description="Each fund has a budget per period. Usage beyond it is refused unless the fund allows overspend, and a closed fund takes no new usage."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/pricing/promotions">
              Promotions
            </Link>
            <Link className={buttonClassName("primary")} to="/pricing/funds/new">
              New fund
            </Link>
          </>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the funds" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Trade funds"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No trade funds", description: "Create a fund to track promotion spend against a budget per period." }}
          onRowOpen={(row) => navigate(`/pricing/funds/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 funds.</p> : null}
    </div>
  );
}
