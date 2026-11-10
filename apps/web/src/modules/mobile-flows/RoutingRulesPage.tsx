import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listRoutingRules, type RoutingRule } from "./api/print";
import { describeScope, DOCUMENT_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<RoutingRule>[] = [
  { key: "documentType", header: "Document", render: (row) => DOCUMENT_LABELS[row.documentType] ?? row.documentType },
  { key: "scope", header: "Applies to", render: (row) => describeScope(row) },
  { key: "queueCode", header: "Queue", mono: true },
  { key: "sequence", header: "Sequence", align: "end", render: (row) => String(row.sequence) },
];

/** Document routing rules (FR-32.13, BR-32.12). */
export function RoutingRulesPage() {
  const state = useLoad((signal) => listRoutingRules(signal), "routing-rules");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Routing rules"
        description="Which queue prints each document. The most specific rule wins: user, then zone, then warehouse, then the document-type default; ties go to the lowest sequence. Without a match a label fails with MOB-0112."
        actions={
          <Link className={buttonClassName("primary")} to="/mobile-flows/routing-rules/new">
            Add rule
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load routing rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Routing rules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No routing rules yet", description: "Add a default rule per document type so labels print without naming a queue." }}
        />
      )}
    </div>
  );
}
