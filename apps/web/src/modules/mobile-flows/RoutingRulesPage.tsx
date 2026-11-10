import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, ConfirmDialog, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { deleteRoutingRule, describeProblem, listRoutingRules, type Problem, type RoutingRule } from "./api/print";
import { describeScope, DOCUMENT_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function columnsFor(onDelete: (rule: RoutingRule) => void): DataTableColumn<RoutingRule>[] {
  return [
  { key: "documentType", header: "Document", render: (row) => DOCUMENT_LABELS[row.documentType] ?? row.documentType },
  { key: "scope", header: "Applies to", render: (row) => describeScope(row) },
  { key: "queueCode", header: "Queue", mono: true },
  { key: "sequence", header: "Sequence", align: "end", render: (row) => String(row.sequence) },
  {
    key: "actions",
    header: "Actions",
    render: (row) => (
      <Button size="sm" variant="ghost" onClick={() => onDelete(row)} aria-label={`Delete the ${DOCUMENT_LABELS[row.documentType] ?? row.documentType} rule for ${row.queueCode}`}>
        Delete
      </Button>
    ),
  },
  ];
}

/** Document routing rules (FR-32.13, BR-32.12). */
export function RoutingRulesPage() {
  const state = useLoad((signal) => listRoutingRules(signal), "routing-rules");
  const [deleting, setDeleting] = useState<RoutingRule | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function confirmDelete() {
    const rule = deleting;
    setDeleting(null);
    if (!rule) return;
    try {
      await deleteRoutingRule(rule.id);
      setFailure(null);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    }
  }
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
      {failure ? (
        <Alert tone="danger" title="Could not delete the rule" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {deleting ? (
        <ConfirmDialog
          title="Delete this routing rule?"
          description={`Documents it sent to queue ${deleting.queueCode} fall to the next matching rule, or fail with MOB-0112 when there is none.`}
          confirmLabel="Delete rule"
          cancelLabel="Keep rule"
          onConfirm={() => void confirmDelete()}
          onCancel={() => setDeleting(null)}
        />
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load routing rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Routing rules"
          columns={columnsFor(setDeleting)}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No routing rules yet", description: "Add a default rule per document type so labels print without naming a queue." }}
        />
      )}
    </div>
  );
}
