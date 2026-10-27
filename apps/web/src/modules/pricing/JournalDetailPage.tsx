import { useRef, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getPriceJournal,
  newKey,
  postPriceJournal,
  PricingApiError,
  reversePriceJournal,
  validatePriceJournal,
  type ConditionRecord,
  type Journal,
  type JournalLine,
  type Problem,
} from "./api/client";
import { describePercent } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Action = "validate" | "post" | "reverse";

const OP_LABELS: Record<JournalLine["op"], string> = { CREATE: "Create", UPDATE: "Update", DELETE: "Withdraw", RESTORE: "Restore" };

/** "95 USD · 10%" for a snapshot; a dash when the line has none (not validated yet, or a create). */
function snapshot(r: ConditionRecord | undefined): string {
  if (!r) return "—";
  const pct = describePercent(r);
  return `${r.amount} ${r.currency}${pct ? ` · ${pct}` : ""}${r.status === "WITHDRAWN" ? " · withdrawn" : ""}`;
}

const lineColumns: DataTableColumn<JournalLine>[] = [
  { key: "lineNo", header: "Line", mono: true, align: "end" },
  { key: "op", header: "Operation", render: (l) => OP_LABELS[l.op] + (l.op === "UPDATE" && Number(l.adjustPct) !== 0 ? ` ${Number(l.adjustPct) > 0 ? "+" : ""}${l.adjustPct}%` : "") },
  { key: "recordId", header: "Agreement", mono: true, render: (l) => <Link to={`/pricing/agreements/${l.recordId}`}>{l.recordId}</Link> },
  { key: "before", header: "Before", mono: true, render: (l) => snapshot(l.before) },
  { key: "after", header: "After", mono: true, render: (l) => snapshot(l.after) },
  { key: "errors", header: "Problems", render: (l) => (l.errors.length === 0 ? "—" : l.errors.map((e) => `${e.code}: ${e.detail}`).join("; ")) },
];

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const DONE: Record<Action, string> = {
  validate: "Validation finished.",
  post: "The journal is posted; every line took effect together.",
  reverse: "The journal is reversed. This is the reversing journal.",
};

/** One price journal: its lines with before and after values, and validate, post and reverse (BR-26.9). */
export function JournalDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getPriceJournal(id, signal), id);
  const [busy, setBusy] = useState<Action | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const key = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Price journal" />
        <Alert tone="danger" title="Could not load the journal" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  const journal = state.data;
  if (!journal) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Price journal" />
        <span className="visually-hidden">Loading the journal…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  async function act(action: Action, j: Journal) {
    setBusy(action);
    setFailure(null);
    setDone("");
    try {
      if (action === "validate") await validatePriceJournal(j.id);
      if (action === "post") await postPriceJournal(j.id, key.current);
      if (action === "reverse") {
        const reversal = await reversePriceJournal(j.id, key.current);
        key.current = newKey();
        setDone(DONE.reverse);
        navigate(`/pricing/journals/${reversal.id}`);
        return;
      }
      key.current = newKey();
      setDone(DONE[action]);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (error instanceof PricingApiError && error.status < 500) key.current = newKey();
      if (error instanceof PricingApiError && error.code === "PRC-0005") state.reload();
    } finally {
      setBusy(null);
    }
  }

  const lines = journal.lines ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={journal.name}
        description={`${journal.lineCount} lines · created ${journal.createdAt.slice(0, 10)}${journal.smartRound ? " · smart rounding" : ""}`}
        meta={<StatusPill status={journal.status.toLowerCase()} />}
        actions={
          <div className={styles.actions}>
            {journal.status === "OPEN" || journal.status === "VALIDATED" ? (
              <Button variant={journal.status === "OPEN" ? "primary" : "secondary"} loading={busy === "validate"} onClick={() => act("validate", journal)}>
                Validate
              </Button>
            ) : null}
            {journal.status === "VALIDATED" ? (
              <Button variant="primary" loading={busy === "post"} onClick={() => act("post", journal)}>
                Post journal
              </Button>
            ) : null}
            {journal.status === "POSTED" ? (
              <Button variant="danger" loading={busy === "reverse"} onClick={() => act("reverse", journal)}>
                Reverse journal
              </Button>
            ) : null}
            <Link className={buttonClassName("ghost")} to="/pricing/journals">
              All journals
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.code === "PRC-0005" ? "Nothing was posted" : "The action failed"} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {journal.errorCount > 0 ? (
        <Alert tone="warning" title={journal.errorCount === 1 ? "1 line needs attention" : `${journal.errorCount} lines need attention`}>
          Fix the lines listed under Problems in a new journal, or validate again after the agreements change. The journal cannot post until every line validates.
        </Alert>
      ) : null}
      <Card title="Journal">
        <dl className={styles.details}>
          <Detail term="Status">{journal.status.charAt(0) + journal.status.slice(1).toLowerCase()}</Detail>
          <Detail term="Lines" mono>
            {journal.lineCount}
          </Detail>
          <Detail term="Errors" mono>
            {journal.errorCount}
          </Detail>
          <Detail term="Posted" mono>
            {journal.postedAt ? journal.postedAt.slice(0, 16).replace("T", " ") : "Not posted"}
          </Detail>
          {journal.reversesJournalId ? (
            <Detail term="Reverses" mono>
              <Link to={`/pricing/journals/${journal.reversesJournalId}`}>{journal.reversesJournalId}</Link>
            </Detail>
          ) : null}
          {journal.reversedByJournalId ? (
            <Detail term="Reversed by" mono>
              <Link to={`/pricing/journals/${journal.reversedByJournalId}`}>{journal.reversedByJournalId}</Link>
            </Detail>
          ) : null}
          <Detail term="Legal entity" mono>
            {journal.legalEntityId}
          </Detail>
        </dl>
      </Card>
      <Card title="Lines">
        <div className={styles.stack}>
          <DataTable
            label="Journal lines"
            columns={lineColumns}
            rows={lines}
            getRowKey={(l) => String(l.lineNo)}
            loading={state.loading}
            empty={{ title: "No lines", description: "This journal has no lines." }}
          />
          {journal.linesTruncated ? <p className={styles.note}>Showing the first 1,000 lines. Every line's problems are counted above.</p> : null}
          {journal.status === "OPEN" && journal.errorCount === 0 ? <p className={styles.note}>Before and after values appear once the journal is validated.</p> : null}
        </div>
      </Card>
    </div>
  );
}
