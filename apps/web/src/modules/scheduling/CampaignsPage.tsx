import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { CAMPAIGN_STATUSES, listCampaigns, listResources, SEQUENCE_RULE_LABELS, type Campaign, type CampaignStatus } from "./api/client";
import { formatDateTime, formatMinutes, sentence, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const campaignTone: Record<CampaignStatus, BadgeTone> = {
  proposed: "neutral",
  planned: "info",
  started: "success",
  completed: "success",
  cancelled: "warning",
};

interface View {
  rows: Campaign[];
  codes: Record<string, string>;
}

/** Campaigns: orders that run back to back on one resource (FR-23.14). */
export function CampaignsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<CampaignStatus | "">("");
  const state = useLoad<View>(async (signal) => {
    const [rows, resources] = await Promise.all([listCampaigns({ status }, signal), listResources(signal)]);
    return { rows, codes: Object.fromEntries(resources.map((r) => [r.id, r.code])) };
  }, status);
  const codes = state.data?.codes ?? {};

  const columns: DataTableColumn<Campaign>[] = [
    { key: "campaignKey", header: "Campaign key", mono: true },
    { key: "resourceId", header: "Resource", mono: true, render: (row) => codes[row.resourceId] ?? shortId(row.resourceId) },
    { key: "status", header: "Status", render: (row) => <Badge tone={campaignTone[row.status] ?? "neutral"}>{sentence(row.status)}</Badge> },
    { key: "plannedStart", header: "Planned start", render: (row) => formatDateTime(row.plannedStart) },
    { key: "orderCount", header: "Orders", align: "end" },
    { key: "sequenceRule", header: "Sequence rule", render: (row) => SEQUENCE_RULE_LABELS[row.sequenceRule] ?? sentence(row.sequenceRule) },
    { key: "changeoverMinutes", header: "Changeover", align: "end", render: (row) => formatMinutes(row.changeoverMinutes) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Campaigns"
        description="Orders grouped to run back to back on one resource, sequenced by the setup matrix with a leading setup and cleanouts."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/scheduling/campaign-proposals">
              Proposals
            </Link>
            <Link className={buttonClassName("primary")} to="/scheduling/campaigns/new">
              New campaign
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="campaigns-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as CampaignStatus | "")}>
              <option value="">All statuses</option>
              {CAMPAIGN_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load campaigns" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Campaigns"
          columns={columns}
          rows={state.data?.rows ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status ? `No ${status} campaigns` : "No campaigns yet",
            description: "Create a campaign on a resource and add the orders that share its setup family.",
          }}
          onRowOpen={(row) => navigate(`/scheduling/campaigns/${row.id}`)}
        />
      )}
    </div>
  );
}
