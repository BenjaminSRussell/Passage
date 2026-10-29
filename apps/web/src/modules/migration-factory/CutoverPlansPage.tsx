import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listPlans, type CutoverPlan } from "./api/client";
import { formatDateTime } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function planState(plan: CutoverPlan): string {
  if (plan.rolledBack) return "Rolled back";
  if (plan.abortDue) return "Abort due";
  return plan.stepsDone === plan.stepsTotal ? "Complete" : "In progress";
}

const columns: DataTableColumn<CutoverPlan>[] = [
  { key: "t0", header: "T-0", render: (row) => formatDateTime(row.t0) },
  { key: "entities", header: "Legal entities", render: (row) => row.entities.join(", ") },
  { key: "packs", header: "Packs", mono: true, render: (row) => row.packs.join(", ") },
  { key: "steps", header: "Steps done", align: "end", render: (row) => `${row.stepsDone} / ${row.stepsTotal}` },
  { key: "openGoNoGo", header: "Open go/no-go", align: "end", render: (row) => row.openGoNoGo.length },
  { key: "state", header: "State", render: (row) => <StatusPill status={planState(row)} /> },
];

/** Cutover plans with their progress (inquiry "Cutover runbook status", 9.49.14). */
export function CutoverPlansPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listPlans(signal), "plans");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cutover runbooks"
        description="Timed plans from T-14d to the first month-end close, with go/no-go gates and the rollback decision point."
        actions={
          <Link className={buttonClassName("primary")} to="/migration-factory/cutover/new">
            Generate plan
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load cutover plans" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Cutover plans"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.planId}
          loading={state.loading}
          empty={{ title: "No cutover plans yet", description: "Generate a plan from the packs and legal entities you are cutting over." }}
          onRowOpen={(row) => navigate(`/migration-factory/cutover/${row.planId}`)}
        />
      )}
    </div>
  );
}
