import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, type DataTableColumn } from "../../shell/ui";
import { listPromotions, type Campaign, type Promotion } from "./api/client";
import { signedPct } from "./fundFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Promotion>[] = [
  { key: "name", header: "Promotion" },
  { key: "productId", header: "Product", mono: true },
  { key: "window", header: "Window", mono: true, render: (row) => `${row.startDate} – ${row.endDate}` },
  { key: "plannedSpend", header: "Planned spend", mono: true, align: "end", render: (row) => `${row.plannedSpend} ${row.currency}` },
  { key: "effectivenessPct", header: "Actual lift", mono: true, align: "end", render: (row) => (row.effectivenessPct ? signedPct(row.effectivenessPct) : "Awaiting actuals") },
];

/** The promotion plans that belong to a campaign (FR-26.10, FR-26.20), with a link to plan another. */
export function CampaignPromotionsCard({ campaign }: { campaign: Campaign }) {
  const navigate = useNavigate();
  const state = useLoad((signal) => listPromotions("", signal, campaign.id), campaign.id);
  return (
    <Card title="Promotions in this campaign">
      <div className={styles.stack}>
        {state.error ? (
          <Alert tone="danger" title="Could not load the promotions" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Campaign promotions"
            columns={columns}
            rows={state.data ?? []}
            getRowKey={(row) => row.id}
            loading={state.loading}
            empty={{ title: "No promotions yet", description: "Plan a promotion inside this campaign to forecast its lift." }}
            onRowOpen={(row) => navigate(`/pricing/promotions/${row.id}`)}
          />
        )}
        {campaign.killed ? (
          <p className={styles.note}>A killed campaign takes no new promotions. Restore it first.</p>
        ) : (
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={`/pricing/promotions/new?${new URLSearchParams({ campaign: campaign.id, legalEntity: campaign.legalEntityId, currency: campaign.currency })}`}>
              Plan promotion
            </Link>
          </div>
        )}
      </div>
    </Card>
  );
}
