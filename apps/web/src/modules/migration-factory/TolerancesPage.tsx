import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listTolerances, type Tolerance } from "./api/client";
import { ANY_TOTAL, isExact, money } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<Tolerance>[] = [
  { key: "pack", header: "Pack", mono: true },
  { key: "total", header: "Control total", mono: true, render: (row) => (row.total === ANY_TOTAL ? "All money totals of the pack" : row.total) },
  { key: "amount", header: "Tolerance per currency", align: "end", render: (row) => (isExact(row.amount) ? "Exact (0.00)" : money(row.amount)) },
  { key: "reason", header: "Reason" },
  { key: "updatedBy", header: "Set by" },
  {
    key: "action",
    header: "Action",
    render: (row) => (
      <Link
        className={buttonClassName("secondary", "sm")}
        to={`/migration-factory/tolerances/set?pack=${encodeURIComponent(row.pack)}&total=${encodeURIComponent(row.total)}`}
        aria-label={`Change tolerance of ${row.total} in ${row.pack}`}
      >
        Change
      </Link>
    ),
  },
];

/** Configured control-total tolerances (FR-49.2). Without one a total must match exactly. */
export function TolerancesPage() {
  const state = useLoad((signal) => listTolerances(signal), "tolerances");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Control-total tolerances"
        description="A tolerance is the absolute delta allowed on each money total, in that total's own currency. A delta inside it is WITHIN_TOLERANCE and does not hold a pack run back from RECONCILED. Counts, quantities and every total without a tolerance compare at 0.00. A change applies to the runs started after it."
        actions={
          <Link className={buttonClassName("primary")} to="/migration-factory/tolerances/set">
            Set tolerance
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the tolerances" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Control-total tolerances"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => `${row.pack}|${row.total}`}
          loading={state.loading}
          empty={{ title: "No tolerances", description: "Every control total compares at 0.00. Set a tolerance for a pack that converts amounts." }}
        />
      )}
    </div>
  );
}
