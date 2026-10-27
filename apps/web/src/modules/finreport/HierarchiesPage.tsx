import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listHierarchies, type HierarchySummary } from "./api/client";
import { SUBJECT_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<HierarchySummary>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "subject", header: "Classifies", render: (h) => SUBJECT_LABELS[h.subject] ?? h.subject },
  { key: "strict", header: "Unassigned members", render: (h) => (h.strict ? "Block publishing" : "Allowed, owner notified") },
  { key: "latestVersion", header: "Published version", mono: true, align: "end", render: (h) => (h.latestVersion > 0 ? `v${h.latestVersion}` : "—") },
  { key: "assignedCount", header: "Members", mono: true, align: "end", render: (h) => (h.latestVersion > 0 ? String(h.assignedCount) : "—") },
  {
    key: "unassignedCount",
    header: "Unassigned",
    render: (h) => (h.unassignedCount > 0 ? <Badge tone="warning">{`${h.unassignedCount} unassigned`}</Badge> : h.latestVersion > 0 ? "None" : "—"),
  },
  { key: "hasDraft", header: "Draft", render: (h) => (h.hasDraft ? <Badge tone="info">Open draft</Badge> : null) },
];

/** Derived financial hierarchies (FR-15.13): every code with its newest published version, members and open draft. */
export function HierarchiesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listHierarchies(signal), "hierarchies");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Derived hierarchies"
        description="Trees over main accounts, account categories or dimension values whose leaves pick their members by rules. Publishing checks that every member lands in exactly one leaf."
        actions={
          <Link className={buttonClassName("primary")} to="/finreport/hierarchies/new">
            New hierarchy
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the hierarchies" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Derived hierarchies"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(h) => h.code}
          loading={state.loading}
          empty={{ title: "No hierarchies yet", description: "Create a hierarchy to group accounts or dimension values for statements." }}
          onRowOpen={(h) => navigate(`/finreport/hierarchies/${encodeURIComponent(h.code)}`)}
        />
      )}
    </div>
  );
}
