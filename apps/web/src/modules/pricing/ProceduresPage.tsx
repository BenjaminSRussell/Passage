import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listProcedures, type Procedure } from "./api/client";
import { PROCEDURE_STATUSES, STEP_TYPE_LABELS } from "./campaignFormat";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Procedure>[] = [
  { key: "name", header: "Procedure", mono: true },
  { key: "version", header: "Version", mono: true, align: "end" },
  {
    key: "status",
    header: "Status",
    render: (row) => {
      const s = PROCEDURE_STATUSES[row.status];
      return <Badge tone={s.tone}>{s.label}</Badge>;
    },
  },
  { key: "steps", header: "Steps", render: (row) => row.steps.map((s) => STEP_TYPE_LABELS[s.conditionType] ?? s.conditionType).join(" → ") },
  { key: "legalEntityId", header: "Legal entity", mono: true },
];

/** Procedure designer (FR-26.1, §5.26.6): versions of each pricing procedure per legal entity. */
export function ProceduresPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listProcedures("", signal), "all");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Pricing procedures"
        description="A procedure orders the condition types a price is built from. Each change is a new version; activating it supersedes the active version of the same name."
        actions={
          <Link className={buttonClassName("primary")} to="/pricing/procedures/new">
            New procedure
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the procedures" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Pricing procedures"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No procedures", description: "Create AX_STANDARD or SAP_RVAA01 for a legal entity to start." }}
          onRowOpen={(row) => navigate(`/pricing/procedures/${row.id}`)}
        />
      )}
      <p className={styles.note}>Prices are still built in the reference order Price, Line, Multiline, Total discount, Surcharge; the active procedure does not drive pricing yet.</p>
    </div>
  );
}
