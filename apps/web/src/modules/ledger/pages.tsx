import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Card, DataTable, FormField, Input, PageHeader, Button, type DataTableColumn } from "../../shell/ui";
import { fetchEntry, fetchTrialBalance, type Entry, type TrialBalance, type TrialBalanceQuery } from "./api/client";
import styles from "./pages.module.css";
import { ReverseEntryForm } from "./ReverseEntryForm";

export const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const PERIOD = /^\d{4}-\d{2}$/;

type Row = TrialBalance["rows"][number];

/**
 * The ledger transactions address for one trial balance row: the account and
 * the posting dates its periods cover (period keys are calendar months until
 * fiscal calendars arrive).
 */
export function transactionsLink(q: TrialBalanceQuery, account: string): string {
  const params = new URLSearchParams({ ledger: q.ledgerId, account_from: account, account_to: account });
  if (q.periodFrom) params.set("date_from", `${q.periodFrom}-01`);
  if (q.periodTo) {
    const [y, m] = q.periodTo.split("-").map(Number);
    params.set("date_to", new Date(Date.UTC(y, m, 0)).toISOString().slice(0, 10));
  }
  return `/ledger/transactions?${params.toString()}`;
}

const balanceColumns: DataTableColumn<Row>[] = [
  { key: "account", header: "Account", mono: true },
  { key: "name", header: "Name" },
  { key: "debit", header: "Debit", align: "end" },
  { key: "credit", header: "Credit", align: "end" },
  { key: "net", header: "Net", align: "end" },
  { key: "lineCount", header: "Lines", align: "end" },
];

/** Trial balance: account rows from the balance projection, with the balanced check. */
export function TrialBalanceScreen() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<TrialBalanceQuery>({ ledgerId: "" });
  const [query, setQuery] = useState<TrialBalanceQuery | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const result = useQuery({
    queryKey: ["ledger", "trial-balance", query],
    queryFn: () => fetchTrialBalance(query as TrialBalanceQuery),
    enabled: query !== null,
  });

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(draft.ledgerId.trim())) found.ledgerId = "Enter the ledger id as a UUID.";
    if (draft.periodFrom && !PERIOD.test(draft.periodFrom)) found.periodFrom = "Periods use the form YYYY-MM.";
    if (draft.periodTo && !PERIOD.test(draft.periodTo)) found.periodTo = "Periods use the form YYYY-MM.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setQuery({ ...draft, ledgerId: draft.ledgerId.trim() });
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Trial balance" description="Debit and credit turnover per account from the balance projection. Storno reversals reduce the original side." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Trial balance filters">
          <FormField id="tb-ledger" label="Ledger id" error={errors.ledgerId}>
            {(c) => <Input {...c} className={styles.mono} value={draft.ledgerId} onChange={(e) => setDraft({ ...draft, ledgerId: e.target.value })} invalid={Boolean(errors.ledgerId)} />}
          </FormField>
          <FormField id="tb-from" label="Period from" optional error={errors.periodFrom}>
            {(c) => <Input {...c} placeholder="2026-01" value={draft.periodFrom ?? ""} onChange={(e) => setDraft({ ...draft, periodFrom: e.target.value || undefined })} invalid={Boolean(errors.periodFrom)} />}
          </FormField>
          <FormField id="tb-to" label="Period to" optional error={errors.periodTo}>
            {(c) => <Input {...c} placeholder="2026-12" value={draft.periodTo ?? ""} onChange={(e) => setDraft({ ...draft, periodTo: e.target.value || undefined })} invalid={Boolean(errors.periodTo)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Show trial balance
            </Button>
          </div>
        </form>
      </Card>
      {query === null ? (
        <p className={styles.note}>Enter a ledger id to see its trial balance.</p>
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the trial balance" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : (
        <>
          {result.data ? (
            <dl className={styles.totals}>
              <div className={styles.entry}>
                <dt className={styles.term}>Check</dt>
                <dd className={styles.value} data-testid="tb-balanced">
                  {result.data.balanced ? "Balanced" : "Not balanced"}
                </dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Total debit ({result.data.currency})</dt>
                <dd className={styles.value}>{result.data.debit}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Total credit ({result.data.currency})</dt>
                <dd className={styles.value}>{result.data.credit}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Net</dt>
                <dd className={styles.value}>{result.data.net}</dd>
              </div>
            </dl>
          ) : null}
          <DataTable
            label="Trial balance"
            columns={balanceColumns}
            rows={result.data?.rows ?? []}
            getRowKey={(row) => row.account}
            loading={result.isPending}
            empty={{ title: "No balances", description: "Nothing has been posted to this ledger in the selected periods." }}
            onRowOpen={(row) => query && navigate(transactionsLink(query, row.account))}
          />
          <p className={styles.note}>Open an account to see its posted lines.</p>
        </>
      )}
    </div>
  );
}

type Line = Entry["lines"][number];

const lineColumns: DataTableColumn<Line>[] = [
  { key: "lineNumber", header: "Line", align: "end" },
  { key: "account", header: "Account", mono: true },
  {
    key: "displayName",
    header: "Dimensions",
    mono: true,
    render: (l) => (
      <>
        {l.displayName}
        {l.derivedByStepIds && l.derivedByStepIds.length > 0 ? (
          <>
            {" "}
            <Badge tone="info">Derived by {l.derivedByStepIds.length} {l.derivedByStepIds.length === 1 ? "step" : "steps"}</Badge>
          </>
        ) : null}
      </>
    ),
  },
  {
    key: "debitCredit",
    header: "D/C",
    render: (l) => (
      <>
        {l.debitCredit}
        {l.negativePosting ? (
          <>
            {" "}
            <Badge tone="warning">Storno</Badge>
          </>
        ) : null}
      </>
    ),
  },
  { key: "amount", header: "Amount", align: "end" },
  { key: "text", header: "Text", render: (l) => l.text ?? "" },
];

/** One posted entry with its lines and totals, and the reverse form (FR-11.8, FR-11.15). */
export function EntryScreen() {
  const { id = "" } = useParams();
  const valid = UUID.test(id);
  const result = useQuery({
    queryKey: ["ledger", "entry", id],
    queryFn: () => fetchEntry(id),
    enabled: valid,
  });

  if (!valid) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Journal entry" />
        <Alert tone="danger" title="The entry id in the link is not valid" live="alert" />
      </div>
    );
  }
  if (result.isError) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Journal entry" />
        <Alert tone="danger" title="Could not load the entry" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      </div>
    );
  }
  const entry = result.data;
  return (
    <div className={styles.stack}>
      <PageHeader title={entry ? `Journal entry ${entry.voucher}` : "Journal entry"} description={entry?.description} />
      {entry ? (
        <Card title="Details">
          <dl className={styles.details}>
            <Detail term="Posting date" value={entry.postingDate} />
            <Detail term="Period" value={entry.periodKey} />
            <Detail term="Posting sequence" value={String(entry.postingSequence)} />
            <Detail term={`Debit (${entry.totals.currency})`} value={entry.totals.debit} />
            <Detail term={`Credit (${entry.totals.currency})`} value={entry.totals.credit} />
            {entry.reversalOfEntryId ? (
              <div className={styles.entry}>
                <dt className={styles.term}>Reverses</dt>
                <dd className={styles.value}>
                  <Link to={`/ledger/entries/${entry.reversalOfEntryId}`}>Original entry</Link>
                </dd>
              </div>
            ) : null}
            {entry.reasonCode ? <Detail term="Reason code" value={entry.reasonCode} mono /> : null}
            {entry.reasonComment ? <Detail term="Reason comment" value={entry.reasonComment} /> : null}
            <Detail term="Trace" value={entry.traceId} mono />
          </dl>
        </Card>
      ) : null}
      <DataTable
        label="Lines"
        columns={lineColumns}
        rows={entry?.lines ?? []}
        getRowKey={(l) => l.id}
        loading={result.isPending}
        empty={{ title: "No lines" }}
      />
      {entry ? <ReverseEntryForm entry={entry} /> : null}
    </div>
  );
}

function Detail({ term, value, mono = false }: { term: string; value: string; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
    </div>
  );
}
