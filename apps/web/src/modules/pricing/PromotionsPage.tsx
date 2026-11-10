import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listPromotions, type Promotion } from "./api/client";
import { signedPct } from "./fundFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const columns: DataTableColumn<Promotion>[] = [
  { key: "name", header: "Promotion" },
  { key: "eventType", header: "Event", render: (row) => [row.eventCategory, row.eventType].filter(Boolean).join(" / ") || "—" },
  { key: "productId", header: "Product", render: (row) => <EntityName entity="product" id={row.productId} /> },
  { key: "campaignId", header: "Campaign", mono: true, render: (row) => (row.campaignId ? <Link to={`/pricing/campaigns/${row.campaignId}`}>{row.campaignId}</Link> : "—") },
  { key: "window", header: "Window", mono: true, render: (row) => `${row.startDate} – ${row.endDate}` },
  { key: "baseline", header: "Baseline / week", mono: true, align: "end" },
  { key: "liftPct", header: "Forecast lift", mono: true, align: "end", render: (row) => signedPct(row.liftPct) },
  { key: "effectivenessPct", header: "Actual lift", mono: true, align: "end", render: (row) => (row.effectivenessPct ? signedPct(row.effectivenessPct) : "Awaiting actuals") },
];

/** Promotion plans (FR-26.20; AX TAMMerchandisingEvent): baseline, forecast lift and actual effectiveness. */
export function PromotionsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listPromotions("", signal), "all");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Promotions"
        description="Plan a promotion against the 13 weeks before it and compare the actual weekly units with that baseline once the window has run."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/pricing/funds">
              Trade funds
            </Link>
            <Link className={buttonClassName("primary")} to="/pricing/promotions/new">
              New promotion
            </Link>
          </>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the promotions" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Promotions"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No promotions", description: "Plan a promotion with its baseline, forecast lift and planned spend." }}
          onRowOpen={(row) => navigate(`/pricing/promotions/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 promotions.</p> : null}
    </div>
  );
}
