import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Card, DataTable, FormField, Input, PageHeader, Button, type DataTableColumn } from "../../shell/ui";
import { fetchEntry, fetchTrialBalance, LedgerApiError, type Entry, type TrialBalance, type TrialBalanceQuery } from "./api/client";
import { EntryAttachmentsCard } from "./BatchAttachments";
import { parseDimensionFilter } from "./BatchLinesEditor";
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
export function transactionsLink(q: TrialBalanceQuery, account: string, groupValues: Record<string, string> = {}): string {
  const params = new URLSearchParams({ ledger: q.ledgerId, account_from: account, account_to: account });
  const dims = { ...(q.dimensions ?? {}) };
  // An empty group value is the "(none)" row: the lines whose combination does not set the attribute.
  for (const [attr, value] of Object.entries(groupValues)) dims[attr] = value;
  if (Object.keys(dims).length > 0) params.set("dims", Object.entries(dims).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => `${k}=${v}`).join(", "));
  if (q.periodFrom) params.set("date_from", `${q.periodFrom}-01`);
  if (q.periodTo) {
    const [y, m] = q.periodTo.split("-").map(Number);
    params.set("date_to", new Date(Date.UTC(y, m, 0)).toISOString().slice(0, 10));
  }
  return `/ledger/transactions?${params.toString()}`;
}

const ATTRIBUTE = /^[A-Z][A-Z0-9_]{0,19}$/;

/** Parses "DEPT, CC" into distinct attributes; null when malformed or more than five. */
export function parseGroupBy(text: string): string[] | null {
  const attrs = text.split(",").map((p) => p.trim()).filter(Boolean);
  if (attrs.length > 5 || new Set(attrs).size !== attrs.length || !attrs.every((a) => ATTRIBUTE.test(a))) return null;
  return attrs;
}

function columnsFor(groupBy: string[]): DataTableColumn<Row>[] {
  return [
    { key: "account", header: "Account", mono: true },
    { key: "name", header: "Name" },
    ...groupBy.map((attr): DataTableColumn<Row> => ({ key: `dim-${attr}`, header: attr, mono: true, render: (r) => r.dimensions?.[attr] || "(none)" })),
    ...amountColumns,
  ];
}

const amountColumns: DataTableColumn<Row>[] = [
  { key: "debit", header: "Debit", align: "end" },
  { key: "credit", header: "Credit", align: "end" },
  { key: "net", header: "Net", align: "end" },
  { key: "lineCount", header: "Lines", align: "end" },
];

/** Trial balance: account rows from the balance projection, with the balanced check. */
/** The error message with any field errors the service named (an unknown attribute, for example). */
function problemText(error: unknown): string {
  if (error instanceof LedgerApiError) {
    const fields = Object.entries(error.fields).map(([k, v]) => `${k} ${v}`);
    return fields.length > 0 ? `${error.message}: ${fields.join("; ")}` : error.message;
  }
  return error instanceof Error ? error.message : "The request failed.";
}

export function TrialBalanceScreen() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<TrialBalanceQuery>({ ledgerId: "" });
  const [dimText, setDimText] = useState("");
  const [groupText, setGroupText] = useState("");
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
    const dimensions = parseDimensionFilter(dimText);
    if (dimensions === null) found.dimensions = "Use ATTRIBUTE=value pairs separated by commas, such as DEPT=D100.";
    const groupBy = parseGroupBy(groupText);
    if (groupBy === null) found.groupBy = "List up to five distinct attributes separated by commas, such as DEPT, CC.";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setQuery({
      ...draft,
      ledgerId: draft.ledgerId.trim(),
      dimensions: dimensions && Object.keys(dimensions).length > 0 ? dimensions : undefined,
      groupBy: groupBy && groupBy.length > 0 ? groupBy : undefined,
    });
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
          <FormField id="tb-dims" label="Dimension filter" optional error={errors.dimensions} hint="Only combinations holding every value, for example DEPT=D100, CC=C1. Leave a value empty (CC=) for combinations without it.">
            {(c) => <Input {...c} className={styles.mono} value={dimText} onChange={(e) => setDimText(e.target.value)} invalid={Boolean(errors.dimensions)} />}
          </FormField>
          <FormField id="tb-group" label="Group by dimensions" optional error={errors.groupBy} hint="Split each account by these attributes, for example DEPT, CC.">
            {(c) => <Input {...c} className={styles.mono} value={groupText} onChange={(e) => setGroupText(e.target.value)} invalid={Boolean(errors.groupBy)} />}
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
          {problemText(result.error)}
        </Alert>
      ) : (
        <>
          {result.data ? (
            <dl className={styles.totals}>
              <div className={styles.entry}>
                <dt className={styles.term}>Check</dt>
                <dd className={styles.value} data-testid="tb-balanced">
                  {result.data.filtered ? "Not applicable to a dimension filter" : result.data.balanced ? "Balanced" : "Not balanced"}
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
            columns={columnsFor(query.groupBy ?? [])}
            rows={result.data?.rows ?? []}
            getRowKey={(row) => row.account + "|" + Object.values(row.dimensions ?? {}).join("|")}
            loading={result.isPending}
            empty={{ title: "No balances", description: "Nothing has been posted to this ledger for the selected periods, accounts and dimensions." }}
            onRowOpen={(row) => query && navigate(transactionsLink(query, row.account, row.dimensions))}
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
            {entry.isRealignment && entry.realignmentRunId ? (
              <div className={styles.entry}>
                <dt className={styles.term}>Dimension realignment</dt>
                <dd className={styles.value}>
                  <Link to={`/ledger/realignments/${entry.realignmentRunId}?ledger=${encodeURIComponent(entry.ledgerId)}`}>Realignment run</Link>
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
      {entry ? <EntryAttachmentsCard entryId={entry.id} /> : null}
      {entry?.isRealignment ? (
        <p className={styles.note}>This entry belongs to a dimension realignment run; reverse it from the run, which reverses all of its entries together.</p>
      ) : entry ? (
        <ReverseEntryForm entry={entry} />
      ) : null}
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
