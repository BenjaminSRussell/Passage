import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, StatusPill, type DataTableColumn } from "../../shell/ui";
import { JOURNAL_STATUSES, listPriceJournals, type Journal, type JournalStatus } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Journal>[] = [
  { key: "name", header: "Journal" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
  { key: "lineCount", header: "Lines", mono: true, align: "end" },
  { key: "errorCount", header: "Errors", mono: true, align: "end" },
  { key: "createdAt", header: "Created", mono: true, render: (row) => row.createdAt.slice(0, 10) },
  { key: "postedAt", header: "Posted", mono: true, render: (row) => (row.postedAt ? row.postedAt.slice(0, 10) : "—") },
];

/** Price journal log (PRD §5.26.14): mass price changes with their status, newest first. */
export function JournalsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<JournalStatus | "">("");
  const state = useLoad((signal) => listPriceJournals({ status }, signal), status);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Price journals"
        description="Mass price changes validate in full, post in one transaction and reverse with one action."
        actions={
          <Link className={buttonClassName("primary")} to="/pricing/journals/new">
            New journal
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="pj-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as JournalStatus | "")}>
              <option value="">Any status</option>
              {JOURNAL_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s.charAt(0) + s.slice(1).toLowerCase()}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the price journals" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Price journals"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No price journals", description: "Create a journal to raise, lower or withdraw many agreements at once." }}
          onRowOpen={(row) => navigate(`/pricing/journals/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the newest 100 journals. Filter by status to find others.</p> : null}
    </div>
  );
}
