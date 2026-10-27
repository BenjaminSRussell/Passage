import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listCleanoutRules, type CleanoutRule } from "./api/client";
import { dash, formatMinutes } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** "After 5 batches · after 48 h · at campaign end". */
export function describeTriggers(rule: CleanoutRule): string {
  const parts = [
    rule.afterBatches !== null ? `after ${rule.afterBatches} batches` : null,
    rule.afterHours !== null ? `after ${rule.afterHours} h` : null,
    rule.atCampaignEnd ? "at campaign end" : null,
  ].filter((p): p is string => p !== null);
  if (parts.length === 0) return dash;
  const text = parts.join(" · ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

const columns: DataTableColumn<CleanoutRule>[] = [
  { key: "setupGroup", header: "Setup group", mono: true },
  { key: "durationMinutes", header: "Duration", align: "end", render: (r) => formatMinutes(r.durationMinutes) },
  { key: "triggers", header: "Due", render: (r) => describeTriggers(r) },
  { key: "operationSetId", header: "Cleaning set", render: (r) => r.operationSetId ?? dash },
  { key: "atCampaignEnd", header: "Campaign end", render: (r) => <Badge tone={r.atCampaignEnd ? "info" : "neutral"}>{r.atCampaignEnd ? "Always" : "Only by limit"}</Badge> },
];

/** Cleanout definitions per setup group (FR-23.15, BR-23.13). */
export function CleanoutRulesPage() {
  const state = useLoad((signal) => listCleanoutRules(signal), "cleanout-rules");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cleanout rules"
        description="When a setup group must be cleaned out: after a number of batches or run hours, whichever comes first, and at campaign end."
        actions={
          <Link className={buttonClassName("primary")} to="/scheduling/cleanout-rules/new">
            New cleanout rule
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load cleanout rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Cleanout rules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No cleanout rules yet", description: "Campaigns insert no cleanouts until a setup group has a rule." }}
        />
      )}
    </div>
  );
}
