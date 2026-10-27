import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  JOURNAL_STATUSES,
  JOURNAL_TYPES,
  listJournals,
  type JournalPage,
  type JournalStatus,
  type JournalSummary,
  type JournalType,
  type Problem,
} from "./api/client";
import { dash, formatDateTime, sentence, shortId, statusTone, typeLabels } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<JournalSummary>[] = [
  { key: "id", header: "Journal", mono: true, render: (row) => shortId(row.id) },
  { key: "journalType", header: "Type", render: (row) => typeLabels[row.journalType] ?? row.journalType },
  { key: "status", header: "Status", render: (row) => <Badge tone={statusTone(row.status)}>{sentence(row.status)}</Badge> },
  { key: "postingDate", header: "Posting date", mono: true },
  { key: "lineCount", header: "Lines", align: "end" },
  { key: "currency", header: "Currency" },
  { key: "postedAt", header: "Posted", render: (row) => (row.postedAt ? formatDateTime(row.postedAt) : dash) },
];

/** Journal list (PRD 3.16.14 "Posted journals by type"), newest first. */
export function JournalsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<JournalStatus | "">("");
  const [type, setType] = useState<JournalType | "">("");
  const state = useLoad((signal) => listJournals({ status, type }, signal), `${status}|${type}`);
  const [more, setMore] = useState<{ key: string; page: JournalPage } | null>(null);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  const key = `${status}|${type}`;
  const extra = more && more.key === key ? more.page : null;
  const rows = [...(state.data?.items ?? []), ...(extra?.items ?? [])];
  const nextCursor = extra ? extra.nextCursor : state.data?.nextCursor;

  async function loadMore() {
    if (!nextCursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listJournals({ status, type, cursor: nextCursor });
      setMore({ key, page: { items: [...(extra?.items ?? []), ...page.items], nextCursor: page.nextCursor } });
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  const failure = state.error ?? moreError;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Inventory journals"
        description="Movement, adjustment and opening-balance journals. Open one to add lines or post it."
        actions={
          <Link className={buttonClassName("primary")} to="/inventory/journals/new">
            New journal
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="journals-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as JournalStatus | "")}>
              <option value="">All statuses</option>
              {JOURNAL_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="journals-type" label="Type">
          {(control) => (
            <Select {...control} value={type} onChange={(event) => setType(event.target.value as JournalType | "")}>
              <option value="">All types</option>
              {JOURNAL_TYPES.map((t) => (
                <option key={t} value={t}>
                  {typeLabels[t]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {failure ? (
        <Alert tone="danger" title="Could not load journals" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {state.error ? null : (
        <DataTable
          label="Journals"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No journals yet", description: "Create a journal to load opening balances or move and adjust stock." }}
          onRowOpen={(row) => navigate(`/inventory/journals/${row.id}`)}
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
