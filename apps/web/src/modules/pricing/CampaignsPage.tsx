import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listCampaigns, type Campaign } from "./api/client";
import { CAMPAIGN_STATES, campaignState, describeTarget } from "./campaignFormat";
import { today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function columns(day: string): DataTableColumn<Campaign>[] {
  return [
    { key: "name", header: "Campaign" },
    { key: "targetCustomerGroup", header: "Customers", render: (row) => describeTarget(row.targetCustomerGroup) },
    { key: "validFrom", header: "Valid from", mono: true },
    { key: "validTo", header: "Valid to", mono: true },
    { key: "budget", header: "Budget", mono: true, align: "end", render: (row) => `${row.budget} ${row.currency}` },
    {
      key: "killed",
      header: "Status",
      render: (row) => {
        const s = CAMPAIGN_STATES[campaignState(row, day)];
        return <Badge tone={s.tone}>{s.label}</Badge>;
      },
    },
  ];
}

/** Campaigns (FR-26.10): trade agreements grouped under one validity, target group, budget and kill switch. */
export function CampaignsPage() {
  const navigate = useNavigate();
  const day = today();
  const state = useLoad((signal) => listCampaigns("", signal), "all");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Campaigns"
        description="A campaign's agreements price only while it is live: inside its validity, for its customer group, and with the kill switch off."
        actions={
          <Link className={buttonClassName("primary")} to="/pricing/campaigns/new">
            New campaign
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the campaigns" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Campaigns"
          columns={columns(day)}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No campaigns", description: "Create a campaign, then add trade agreements to it from the agreement form." }}
          onRowOpen={(row) => navigate(`/pricing/campaigns/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 campaigns.</p> : null}
    </div>
  );
}
