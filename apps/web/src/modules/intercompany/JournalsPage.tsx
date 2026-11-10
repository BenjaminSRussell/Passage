import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { getJournal, listJournals, type PostedEntry, type PostedJournal, type PostingStatus } from "./api/postings";
import { amount, dash } from "./format";
import styles from "./pages.module.css";

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

/** The IC journals posted to the ledgers, newest first (FR-40.5). */
export function JournalsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState("");
  const [state, setState] = useState<{ loading: boolean; items: PostedJournal[]; next?: string; failure: Problem | null }>({ loading: true, items: [], failure: null });
  const [reload, setReload] = useState(0);
  const [more, setMore] = useState(false);

  useEffect(() => {
    const ctl = new AbortController();
    setState((s) => ({ ...s, loading: true, failure: null }));
    listJournals({ status: status || undefined }, ctl.signal)
      .then((page) => setState({ loading: false, items: page.items, next: page.nextCursor, failure: null }))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setState({ loading: false, items: [], failure: describeProblem(err) });
      });
    return () => ctl.abort();
  }, [status, reload]);

  async function loadMore() {
    if (!state.next) return;
    setMore(true);
    try {
      const page = await listJournals({ status: status || undefined, after: state.next });
      setState((s) => ({ ...s, items: [...s.items, ...page.items], next: page.nextCursor }));
    } catch (err) {
      setState((s) => ({ ...s, failure: describeProblem(err) }));
    } finally {
      setMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Posted intercompany journals"
        description="Each journal made one entry in the ledger of every entity it touches. One that is paused can be resumed by posting it again; one that is reversed was refused by a ledger and its earlier sides were taken back."
        actions={<Link to="/intercompany/journal">Post a journal</Link>}
      />
      <Card>
        <div className={styles.filters}>
          <FormField id="journals-status" label="Show">
            {(c) => (
              <Select {...c} value={status} onChange={(e) => setStatus(e.target.value)}>
                <option value="">All journals</option>
                <option value="posted">Posted</option>
                <option value="pending">Paused</option>
                <option value="compensated">Reversed</option>
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      {state.failure ? (
        <Alert tone="danger" title="We couldn't load the journals" live="alert" action={<Button size="sm" onClick={() => setReload((n) => n + 1)}>Try again</Button>}>
          <ProblemMessage problem={state.failure} />
        </Alert>
      ) : (
        <DataTable
          label="Posted journals"
          columns={columns}
          rows={state.items}
          getRowKey={(j) => j.id}
          loading={state.loading}
          onRowOpen={(j) => navigate(`/intercompany/journals/${j.id}`)}
          empty={{ title: "No journals posted yet", description: "Preview an intercompany line and post it to see it here." }}
        />
      )}
      {state.next && !state.failure ? (
        <div className={styles.formActions}>
          <Button loading={more} onClick={() => void loadMore()}>
            Show more journals
          </Button>
        </div>
      ) : null}
    </div>
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
