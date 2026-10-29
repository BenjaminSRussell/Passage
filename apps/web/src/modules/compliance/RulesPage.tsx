import { Alert, Badge, Button, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listRules, type Rule } from "./api/client";
import { sentence, severityTone } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Rule>[] = [
  { key: "ruleId", header: "Rule", mono: true },
  { key: "name", header: "Name" },
  { key: "dutyA", header: "Duty", mono: true },
  { key: "dutyB", header: "Conflicts with", mono: true },
  { key: "severity", header: "Severity", render: (r) => <Badge tone={severityTone[r.severity]}>{sentence(r.severity)}</Badge> },
  { key: "mitigation", header: "Mitigation", render: (r) => (r.mitigationAllowed ? `Up to ${r.maxMitigationDays} days` : "Not allowed") },
  { key: "status", header: "Status", render: (r) => sentence(r.status) },
];

/** The SoD rule set the engine evaluates (FR-48.4 seed rules). */
export function RulesPage() {
  const state = useLoad((signal) => listRules(signal), "rules");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="SoD rules"
        description="Pairs of duties one user must not hold together. A pattern such as *Post* matches every posting duty."
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="SoD rules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.ruleId}
          loading={state.loading}
          empty={{ title: "No rules yet", description: "Rules come from the seed set and the GRC import." }}
        />
      )}
    </div>
  );
}
