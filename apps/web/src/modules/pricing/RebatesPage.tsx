import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listRebateAgreements, type RebateAgreement } from "./api/client";
import styles from "./pages.module.css";
import { BASIS_LABELS, describeRebateCustomer, describeTiers, PERIOD_LABELS } from "./rebateFormat";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<RebateAgreement>[] = [
  { key: "name", header: "Agreement" },
  { key: "customerId", header: "Customer", mono: true, render: describeRebateCustomer },
  { key: "basis", header: "Basis", render: (row) => BASIS_LABELS[row.basis] },
  { key: "period", header: "Period", render: (row) => PERIOD_LABELS[row.period] },
  { key: "tiers", header: "Tiers", mono: true, render: describeTiers },
  { key: "currency", header: "Currency", mono: true },
  { key: "validFrom", header: "Validity", mono: true, render: (row) => `${row.validFrom} – ${row.validTo}` },
];

/** Rebate agreements (PRD §5.26.6): customer rebates that accrue on every posted invoice. */
export function RebatesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listRebateAgreements("", signal), "all");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Rebate agreements"
        description="Tiered customer rebates. Each posted invoice line accrues against its period; crossing a tier recomputes the period."
        actions={
          <Link className={buttonClassName("primary")} to="/pricing/rebates/new">
            New rebate agreement
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the rebate agreements" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Rebate agreements"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No rebate agreements", description: "Create an agreement to start accruing rebates on posted invoices." }}
          onRowOpen={(row) => navigate(`/pricing/rebates/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 agreements.</p> : null}
    </div>
  );
}
