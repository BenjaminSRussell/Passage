import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listBenefitPlans, type BenefitPlan, type Problem } from "./api/client";
import { planTypeLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<BenefitPlan>[] = [
  { key: "code", header: "Plan", mono: true },
  { key: "planType", header: "Type", render: (row) => planTypeLabel[row.planType] ?? row.planType },
  { key: "window", header: "Open enrollment", mono: true, render: (row) => `${row.enrollmentStart} to ${row.enrollmentEnd}` },
  { key: "waitingDays", header: "Waiting days", mono: true, align: "end" },
  { key: "options", header: "Options", align: "end", mono: true, render: (row) => row.options.length },
  { key: "currency", header: "Currency", mono: true },
  { key: "preTax", header: "Tax", render: (row) => (row.preTax ? <Badge tone="info">Pre-tax</Badge> : <Badge tone="neutral">After tax</Badge>) },
];

/** Benefit plans in the tenant (FR-42.8): what is offered, when enrollment is open, and who waits how long. */
export function BenefitPlansPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listBenefitPlans({}, signal), "benefit-plans");
  const [more, setMore] = useState<{ items: BenefitPlan[]; cursor?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  const rows = [...(state.data?.items ?? []), ...(more?.items ?? [])];
  const cursor = more ? more.cursor : state.data?.nextCursor;

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await listBenefitPlans({ cursor });
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
        title="Benefit plans"
        description="Each plan has coverage options with employee and employer rates, an eligibility rule and an open enrollment window. Open a plan to see who is eligible on a date."
        actions={
          <Link className={buttonClassName("primary")} to="/hr/benefits/plans/new">
            New plan
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load benefit plans" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Benefit plans"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No benefit plans yet", description: "Create a plan such as a medical plan with employee and family options." }}
          onRowOpen={(row) => navigate(`/hr/benefits/plans/${row.id}`)}
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
