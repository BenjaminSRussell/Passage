import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { buttonClassName, Button, Card, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { advanceCompCycle, applyCompCycle, getCompCycle, listCompCycles, type CompCycle, type CycleBudget, type CycleMove, type GuidelineRow } from "./api/recruitment";
import { UUID, dayOf } from "./format";
import styles from "./pages.module.css";
import { BackLink, ConfirmAction, Entry, Facts, humanize, ListPage, Loaded, Status, TextField, useAction } from "./parts";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<CompCycle>[] = [
  { key: "name", header: "Cycle", render: (row) => <Link to={`/hr/compensation/cycles/${row.id}`}>{row.name}</Link> },
  { key: "kind", header: "Kind", render: (row) => humanize(row.kind) },
  { key: "effectiveDate", header: "Effective", mono: true, render: (row) => dayOf(row.effectiveDate) },
  { key: "currency", header: "Currency", mono: true },
  { key: "status", header: "Status", render: (row) => <Status value={row.status} /> },
];

/** Merit and bonus cycles: managers propose raises inside a guideline matrix and a department budget, then HR applies them. */
export function CompCyclesPage() {
  return (
    <ListPage<CompCycle>
      title="Compensation cycles"
      description="Merit and bonus planning. Applying a cycle creates one pay-change action per approved proposal."
      label="Compensation cycles"
      noun="cycles"
      load={(cursor, signal) => listCompCycles(cursor, signal)}
      loadKey="cycles"
      columns={columns}
      getRowKey={(row) => row.id}
      empty={{ title: "No compensation cycles", description: "Create a cycle to start planning raises." }}
      actions={
        <Link className={buttonClassName("primary")} to="/hr/compensation/cycles/new">
          New cycle
        </Link>
      }
    />
  );
}

const budgetColumns: DataTableColumn<CycleBudget>[] = [
  { key: "departmentId", header: "Department", mono: true },
  { key: "budgetAmount", header: "Budget", mono: true, align: "end" },
];

const matrixColumns: DataTableColumn<GuidelineRow>[] = [
  { key: "rating", header: "Rating", align: "end" },
  { key: "compa", header: "Compa-ratio", mono: true, render: (row) => `${row.compaMin} to ${row.compaMax}` },
  { key: "pct", header: "Raise", mono: true, render: (row) => `${row.pctMin}% to ${row.pctMax}%` },
];

const FORWARD: Record<string, { to: CycleMove; label: string; done: string }> = {
  PLANNING: { to: "MANAGER_INPUT", label: "Open to managers", done: "Managers can now fill in their worksheets." },
  MANAGER_INPUT: { to: "APPROVAL", label: "Send to approval", done: "The cycle is waiting for approval." },
};

export function CompCycleDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getCompCycle(id, signal), id);
  const action = useAction(state.reload);
  const [manager, setManager] = useState("");
  const [managerError, setManagerError] = useState<string | undefined>();

  return (
    <Loaded state={state} title="Compensation cycle" what="compensation cycle" backTo="/hr/compensation/cycles">
      {(cycle) => {
        const forward = FORWARD[cycle.status];
        const live = cycle.status !== "APPLIED" && cycle.status !== "CANCELED";
        return (
          <div className={styles.stack}>
            <PageHeader title={cycle.name} meta={<Status value={cycle.status} />} actions={<BackLink to="/hr/compensation/cycles">Back to cycles</BackLink>} />
            {action.feedback}
            <Card
              title="Cycle"
              actions={
                <div className={styles.rowActions}>
                  {forward ? (
                    <Button variant="primary" loading={action.busy === forward.to} onClick={() => void action.run(forward.to, () => advanceCompCycle(cycle.id, forward.to), forward.done)}>
                      {forward.label}
                    </Button>
                  ) : null}
                  {cycle.status === "APPROVAL" ? (
                    <ConfirmAction
                      label="Apply cycle"
                      variant="primary"
                      title="Apply this cycle?"
                      description="This creates a pay-change action for every approved proposal and can't be undone."
                      confirmLabel="Apply cycle"
                      busy={action.busy === "apply"}
                      onConfirm={() =>
                        void action.run("apply", () => applyCompCycle(cycle.id), (applied) => `The cycle was applied and created ${applied.actionIds?.length ?? 0} pay-change actions.`)
                      }
                    />
                  ) : null}
                  {live ? (
                    <ConfirmAction
                      label="Cancel cycle"
                      title="Cancel this cycle?"
                      description="No proposals will be applied."
                      confirmLabel="Cancel cycle"
                      busy={action.busy === "CANCELED"}
                      onConfirm={() => void action.run("CANCELED", () => advanceCompCycle(cycle.id, "CANCELED"), "The cycle was cancelled.")}
                    />
                  ) : null}
                </div>
              }
            >
              <Facts>
                <Entry term="Kind">{humanize(cycle.kind)}</Entry>
                <Entry term="Effective" mono>
                  {dayOf(cycle.effectiveDate)}
                </Entry>
                <Entry term="Currency" mono>
                  {cycle.currency}
                </Entry>
                <Entry term="Over budget">{cycle.allowOverBudget ? "Allowed" : "Not allowed"}</Entry>
                {cycle.appliedAt ? (
                  <Entry term="Applied" mono>
                    {dayOf(cycle.appliedAt)}
                  </Entry>
                ) : null}
              </Facts>
            </Card>
            <Card title="Manager worksheets">
              <form
                className={styles.toolbar}
                noValidate
                aria-label="Open a manager's worksheet"
                onSubmit={(event) => {
                  event.preventDefault();
                  if (!UUID.test(manager.trim())) {
                    setManagerError("Enter the manager's worker id.");
                    return;
                  }
                  navigate(`/hr/compensation/cycles/${cycle.id}/worksheets/${manager.trim()}`);
                }}
              >
                <TextField id="cycle-manager" label="Manager's worker id" value={manager} error={managerError} onChange={(v) => {
                    setManager(v);
                    if (UUID.test(v.trim())) setManagerError(undefined);
                  }}
                />
                <Button type="submit">Open worksheet</Button>
              </form>
            </Card>
            <Card title="Department budgets">
              <DataTable label="Department budgets" columns={budgetColumns} rows={cycle.budgets} getRowKey={(row) => row.departmentId} empty={{ title: "No budgets" }} />
            </Card>
            <Card title="Guideline matrix">
              <DataTable
                label="Guideline matrix"
                columns={matrixColumns}
                rows={cycle.guidelineMatrix}
                getRowKey={(row) => `${row.rating}|${row.compaMin}`}
                empty={{ title: "No guideline rows" }}
              />
            </Card>
          </div>
        );
      }}
    </Loaded>
  );
}
