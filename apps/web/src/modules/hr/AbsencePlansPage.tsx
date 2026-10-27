import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listAbsencePlans, type AbsencePlan, type Problem } from "./api/client";
import { absenceUnitLabel, frequencyLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<AbsencePlan>[] = [
  { key: "code", header: "Plan", mono: true },
  { key: "accrualFrequency", header: "Accrues", render: (row) => frequencyLabel[row.accrualFrequency] ?? row.accrualFrequency },
  { key: "accrualRate", header: "Rate", mono: true, align: "end", render: (row) => `${row.accrualRate} ${absenceUnitLabel[row.unit] ?? row.unit}` },
  { key: "maxBalance", header: "Max balance", mono: true, align: "end" },
  { key: "carryOverMax", header: "Carry-over max", mono: true, align: "end" },
  { key: "planYearStart", header: "Plan year starts", mono: true },
  {
    key: "negativeBalanceAllowed",
    header: "Negative balance",
    render: (row) => (row.negativeBalanceAllowed ? <Badge tone="warning">Allowed</Badge> : <Badge tone="neutral">Not allowed</Badge>),
  },
];

/** Absence plans in the tenant (FR-42.6): what accrues, how fast, and the caps. */
export function AbsencePlansPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listAbsencePlans({}, signal), "absence-plans");
  const [more, setMore] = useState<{ items: AbsencePlan[]; cursor?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  const rows = [...(state.data?.items ?? []), ...(more?.items ?? [])];
  const cursor = more ? more.cursor : state.data?.nextCursor;

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await listAbsencePlans({ cursor });
      setMore({ items: [...(more?.items ?? []), ...next.items], cursor: next.nextCursor });
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Absence plans"
        description="Each plan accrues leave per period, caps the balance and carries part of it into the next plan year. Open a plan to run its accrual or close a year."
        actions={
          <Link className={buttonClassName("primary")} to="/hr/absence/plans/new">
            New plan
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load absence plans" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Absence plans"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No absence plans yet", description: "Create a plan such as US-PTO-15, then run its first accrual." }}
          onRowOpen={(row) => navigate(`/hr/absence/plans/${row.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more plans" live="alert">
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
      {!state.error && !state.loading && cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more plans
          </Button>
        </div>
      ) : null}
    </div>
  );
}
