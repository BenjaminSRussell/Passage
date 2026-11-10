import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listCommodityCalculations, type CommodityCalculation } from "./api/client";
import { CALC_STATES, calcState, describeFormula, describeTarget } from "./commodityFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<CommodityCalculation>[] = [
  { key: "name", header: "Calculation" },
  { key: "productId", header: "Reprices", render: describeTarget },
  { key: "factor", header: "Formula", render: describeFormula },
  {
    key: "withdrawn",
    header: "Status",
    render: (row) => {
      const s = CALC_STATES[calcState(row)];
      return <Badge tone={s.tone}>{s.label}</Badge>;
    },
  },
];

/** Commodity price calculations (FR-26.21): index x factor + margin for one product price. */
export function CommodityCalculationsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listCommodityCalculations({}, signal), "all");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Price calculations"
        description="A calculation prices one product from a commodity index. A run previews the new price, then creates a price journal that waits for review."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/pricing/commodity/bases">
              Cost basis types
            </Link>
            <Link className={buttonClassName("primary")} to="/pricing/commodity/calculations/new">
              New price calculation
            </Link>
          </>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the price calculations" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Commodity price calculations"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No price calculations", description: "Create a calculation that turns an index into a price." }}
          onRowOpen={(row) => navigate(`/pricing/commodity/calculations/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 calculations.</p> : null}
    </div>
  );
}
