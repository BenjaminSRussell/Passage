import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listCommodityBases, type CommodityBasis } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<CommodityBasis>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Cost basis type" },
  { key: "currency", header: "Currency", mono: true },
  { key: "unit", header: "Quoted per", mono: true },
];

/** Commodity cost basis types (FR-26.21): what an index is quoted for. */
export function CommodityBasesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listCommodityBases("", signal), "all");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Commodity prices"
        description="A cost basis type holds dated index quotes. A price calculation turns the latest quote into a price and reprices the trade agreements it names."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/pricing/commodity/calculations">
              Price calculations
            </Link>
            <Link className={buttonClassName("primary")} to="/pricing/commodity/bases/new">
              New cost basis type
            </Link>
          </>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the cost basis types" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Commodity cost basis types"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No cost basis types", description: "Create one, such as LME copper cash, then record its index quotes." }}
          onRowOpen={(row) => navigate(`/pricing/commodity/bases/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 cost basis types.</p> : null}
    </div>
  );
}
