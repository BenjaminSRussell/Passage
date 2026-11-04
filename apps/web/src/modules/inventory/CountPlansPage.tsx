import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { generateCountSheet, listCountPlans, type CountPlan, type Problem } from "./api/client";
import { ProblemAlert, RowAction } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Count plans (FR-16.9) with "Generate sheet", which opens the new count sheet, and tag counting (FR-16.18). */
export function CountPlansPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listCountPlans(signal), "plans");
  const [failure, setFailure] = useState<Problem | null>(null);

  const columns: DataTableColumn<CountPlan>[] = [
    { key: "name", header: "Plan" },
    { key: "warehouse", header: "Warehouse", render: (row) => row.warehouse || "All warehouses" },
    { key: "tolerance", header: "Approval above", render: (row) => `${row.tolerancePct} % or ${row.toleranceValue} ${row.currency}` },
    {
      key: "selection",
      header: "Selection",
      render: (row) =>
        [
          row.locationFrom || row.locationTo ? `Locations ${row.locationFrom || "start"} to ${row.locationTo || "end"}` : "",
          row.abcClass ? `Class ${row.abcClass}` : "",
          row.samplePct ? `${row.samplePct} % sample` : "",
          row.minDaysSinceCount ? `Not counted for ${row.minDaysSinceCount} days` : "",
          row.includeVendorOwned ? "Includes vendor-owned stock" : "",
        ]
          .filter(Boolean)
          .join(" · ") || "Everything",
    },
    {
      key: "control",
      header: "Control",
      render: (row) => [row.freezeStock ? "Freezes stock" : "", row.fourEyes ? "Four eyes" : ""].filter(Boolean).join(" · ") || "None",
    },
    { key: "unit", header: "Unit" },
    { key: "blind", header: "Blind count", render: (row) => (row.blind ? "Yes" : "No") },
    {
      key: "actions",
      header: "Actions",
      render: (row) => (
        <span className={styles.inline}>
          <RowAction
            label="Generate sheet"
            name={`for ${row.name}`}
            run={async () => {
              const sheet = await generateCountSheet(row.id);
              navigate(`/inventory/count-sheets/${sheet.id}`);
            }}
            onDone={() => setFailure(null)}
            onError={setFailure}
          />
          <Link className={buttonClassName("ghost", "sm")} to={`/inventory/count-plans/${row.id}/tags`} aria-label={`Tags for ${row.name}`}>
            Tags
          </Link>
        </span>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Count plans"
        description="Cycle count plans for a legal entity or one warehouse. Generate a sheet to count the stock as it stands now, or count with pre-numbered tags."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/inventory/count-sheets">
              Count sheets
            </Link>
            <Link className={buttonClassName("primary")} to="/inventory/count-plans/new">
              New count plan
            </Link>
          </>
        }
      />
      {state.error ? <ProblemAlert title="Could not load count plans" problem={state.error} onRetry={state.reload} /> : null}
      {failure ? <ProblemAlert title="No count sheet was generated" problem={failure} /> : null}
      {state.error ? null : (
        <DataTable
          label="Count plans"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No count plans yet", description: "Create a plan with its tolerances, then generate count sheets from it." }}
        />
      )}
    </div>
  );
}
