import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listJournals, moveJournal, type JournalPage, type JournalSummary, type Problem } from "./api/client";
import { formatDateTime, shortId, typeLabels } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

type Step = "approve" | "reject";

/**
 * Approver inbox (BR-16.11): the journals waiting for approval, oldest first so
 * the longest wait is on top. Approve or reject here, or open the journal to
 * read its lines first. The service refuses the step to a caller without the
 * posting duty and to a stale version (the row is reloaded then).
 */
export function ApprovalsPage() {
  const state = useLoad((signal) => listJournals({ status: "SUBMITTED", limit: 200 }, signal), "submitted");
  const [busy, setBusy] = useState<{ id: string; step: Step } | null>(null);
  const [failure, setFailure] = useState<{ id: string; step: Step; problem: Problem } | null>(null);
  const [notice, setNotice] = useState("");
  const [more, setMore] = useState<JournalPage | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  const nextCursor = more ? more.nextCursor : state.data?.nextCursor;
  const rows = [...(state.data?.items ?? []), ...(more?.items ?? [])].sort((a, b) => a.createdAt.localeCompare(b.createdAt));

  async function act(row: JournalSummary, step: Step) {
    setBusy({ id: row.id, step });
    setFailure(null);
    setNotice("");
    try {
      await moveJournal(row.id, row.version, step);
      setNotice(`Journal ${shortId(row.id)} ${step === "approve" ? "approved. It can now be posted." : "rejected."}`);
      setMore(null);
      state.reload();
    } catch (error) {
      setFailure({ id: row.id, step, problem: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  async function loadMore() {
    if (!nextCursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listJournals({ status: "SUBMITTED", limit: 200, cursor: nextCursor });
      setMore({ items: [...(more?.items ?? []), ...page.items], nextCursor: page.nextCursor });
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  const columns: DataTableColumn<JournalSummary>[] = [
    { key: "id", header: "Journal", mono: true, render: (row) => <Link to={`/inventory/journals/${row.id}`}>{shortId(row.id)}</Link> },
    { key: "journalType", header: "Type", render: (row) => typeLabels[row.journalType] ?? row.journalType },
    { key: "postingDate", header: "Posting date", mono: true },
    { key: "lineCount", header: "Lines", align: "end" },
    { key: "currency", header: "Currency" },
    { key: "createdAt", header: "Created", render: (row) => formatDateTime(row.createdAt) },
    {
      key: "actions",
      header: "Decision",
      render: (row) => (
        <span className={styles.rowActions}>
          <Button size="sm" variant="primary" loading={busy?.id === row.id && busy.step === "approve"} disabled={busy !== null} onClick={() => act(row, "approve")}>
            Approve
          </Button>
          <Button size="sm" variant="danger" loading={busy?.id === row.id && busy.step === "reject"} disabled={busy !== null} onClick={() => act(row, "reject")}>
            Reject
          </Button>
        </span>
      ),
    },
  ];

  const error = state.error ?? moreError;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Journal approvals"
        description="Journals submitted for approval, oldest first. Approving releases a journal for posting; rejecting sends it back to its author."
        actions={
          <Link className={buttonClassName("secondary")} to="/inventory/journals">
            All journals
          </Link>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title={`Journal ${shortId(failure.id)} was not ${failure.step === "approve" ? "approved" : "rejected"}`}
          live="alert"
          action={failure.problem.code === "conflict_version" ? <Button size="sm" onClick={state.reload}>Reload</Button> : undefined}
        >
          <ProblemMessage problem={failure.problem} />
        </Alert>
      ) : null}
      {error ? (
        <Alert tone="danger" title="Could not load the approvals" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={error} />
        </Alert>
      ) : null}
      {state.error ? null : (
        <DataTable
          label="Journals awaiting approval"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "Nothing to approve", description: "Journals that need approval appear here once their author submits them." }}
        />
      )}
      {nextCursor && !state.loading ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more journals
          </Button>
        </div>
      ) : null}
    </div>
  );
}
