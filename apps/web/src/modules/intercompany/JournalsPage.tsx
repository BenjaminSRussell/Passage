import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Badge, buttonClassName, Card, DataTable, ListPage, PageHeader, type BadgeTone, type DataTableColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { getJournal, listJournals, type PostedEntry, type PostedJournal, type PostingStatus } from "./api/postings";
import { amount, dash } from "./format";
import styles from "./pages.module.css";
import { usePagedRows } from "./usePagedRows";

const statusTone: Record<PostingStatus, BadgeTone> = { posted: "success", pending: "warning", compensated: "danger" };
const statusLabel: Record<PostingStatus, string> = { posted: "Posted", pending: "Paused, can be resumed", compensated: "Reversed" };

export function PostingBadge({ status }: { status: PostingStatus }) {
  return <Badge tone={statusTone[status]}>{statusLabel[status]}</Badge>;
}

const columns: DataTableColumn<PostedJournal>[] = [
  { key: "date", header: "Posting date", mono: true, render: (j) => j.postingDate },
  { key: "source", header: "Source", render: (j) => (j.source === "charge" ? "IC charge" : "IC journal") },
  { key: "entities", header: "Entities", mono: true, render: (j) => j.entries.map((e) => e.entity).join(" / ") },
  { key: "status", header: "Status", render: (j) => <PostingBadge status={j.status} /> },
  { key: "by", header: "Posted by", render: (j) => j.createdBy },
];

const journalFilters: ListFilterDef[] = [
  {
    kind: "select",
    key: "status",
    label: "Status",
    anyLabel: "All journals",
    options: [
      { value: "posted", label: "Posted" },
      { value: "pending", label: "Paused" },
      { value: "compensated", label: "Reversed" },
    ],
  },
];

function useJournalRows(query: ListQuery): ListData<PostedJournal> {
  const status = query.filters.status ?? "";
  return usePagedRows((after, signal) => listJournals({ status: status || undefined, after }, signal), status);
}

/** The IC journals posted to the ledgers, newest first (FR-40.5). */
export function JournalsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="intercompany-journals"
      title="Posted intercompany journals"
      description="Each journal made one entry in the ledger of every entity it touches. One that is paused can be resumed by posting it again; one that is reversed was refused by a ledger and its earlier sides were taken back."
      actions={
        <Link className={buttonClassName("primary")} to="/intercompany/journal">
          Post a journal
        </Link>
      }
      noun="journals"
      singular="journal"
      columns={columns}
      filters={journalFilters}
      useRows={useJournalRows}
      getRowKey={(j) => j.id}
      onRowOpen={(j) => navigate(`/intercompany/journals/${j.id}`)}
      empty={{ title: "No journals posted yet", description: "Preview an intercompany line and post it to see it here." }}
    />
  );
}

function entryColumns(): DataTableColumn<{ account: string; side: string; amount: string }>[] {
  return [
    { key: "account", header: "Account", mono: true, render: (l) => l.account },
    { key: "debit", header: "Debit", align: "end", render: (l) => (l.side === "debit" ? amount(l.amount) : dash) },
    { key: "credit", header: "Credit", align: "end", render: (l) => (l.side === "credit" ? amount(l.amount) : dash) },
  ];
}

function EntryCard({ entry }: { entry: PostedEntry }) {
  return (
    <Card title={`Entity ${entry.entity}, ${entry.currency}`} headingLevel={3}>
      <div className={styles.stack}>
        <p className={styles.note}>
          Posted in the ledger of <EntityName entity="legalEntity" id={entry.legalEntityId} />
          {entry.ledgerEntryId ? "" : ": not posted yet"}
          {entry.reversalEntryId ? ", then reversed" : ""}.
        </p>
        <DataTable label={`Legs of entity ${entry.entity}`} columns={entryColumns()} rows={entry.legs} getRowKey={(l, i) => `${l.account}-${i}`} />
      </div>
    </Card>
  );
}

/** One posted journal with the entry each entity made. */
export function JournalDetailPage() {
  const { id = "" } = useParams();
  const [journal, setJournal] = useState<PostedJournal | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  useEffect(() => {
    const ctl = new AbortController();
    setFailure(null);
    getJournal(id, ctl.signal)
      .then(setJournal)
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setFailure(describeProblem(err));
      });
    return () => ctl.abort();
  }, [id]);

  if (failure) {
    return (
      <Alert tone="danger" title="We couldn't load this journal" live="alert">
        <ProblemMessage problem={failure} />
      </Alert>
    );
  }
  if (!journal) return <p className={styles.note}>Loading the journal…</p>;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${journal.source === "charge" ? "IC charge" : "IC journal"} posted ${journal.postingDate}`}
        description="Each entity books its own side in its own ledger and currency."
        actions={<Link to="/intercompany/journals">All journals</Link>}
      />
      <Alert tone={journal.status === "posted" ? "success" : journal.status === "pending" ? "warning" : "danger"} title={statusLabel[journal.status]}>
        {journal.status === "posted"
          ? "Every entity's ledger took its entry."
          : journal.status === "pending"
            ? "Some entries are not in a ledger yet. Post the same journal again to continue."
            : (journal.failureDetail ?? "A ledger refused its side and the earlier sides were reversed.")}
      </Alert>
      {journal.entries.map((e) => (
        <EntryCard key={e.entity} entry={e} />
      ))}
    </div>
  );
}
