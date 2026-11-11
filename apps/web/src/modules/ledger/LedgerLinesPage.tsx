import { useInfiniteQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { exportJournalLinesCsv, fetchJournalLines, LedgerApiError, type JournalLine, type JournalLineQuery } from "./api/client";
import { parseDimensionFilter } from "./BatchLinesEditor";
import { ExportButton } from "./ExportButton";
import { UUID } from "./pages";
import styles from "./pages.module.css";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

/** Lines per request; the API allows up to 1,000. */
export const LINES_PAGE_SIZE = 100;

const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
/** Renders dimension values as "CC=C1, DEPT=D100" (sorted by attribute). */
export function formatDimensions(dims: Record<string, string> | undefined): string {
  return Object.entries(dims ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}=${v}`)
    .join(", ");
}

interface Draft {
  ledgerId: string;
  accountFrom: string;
  accountTo: string;
  dateFrom: string;
  dateTo: string;
  voucher: string;
  dimensions: string;
}

const FIELDS: Record<keyof Draft, string> = {
  ledgerId: "ledger",
  accountFrom: "account_from",
  accountTo: "account_to",
  dateFrom: "date_from",
  dateTo: "date_to",
  voucher: "voucher",
  dimensions: "dims",
};

function draftFrom(params: URLSearchParams): Draft {
  const d = {} as Draft;
  for (const [key, param] of Object.entries(FIELDS) as [keyof Draft, string][]) d[key] = params.get(param) ?? "";
  return d;
}

/** Field errors of a draft; empty when it can be sent. */
export function validateLineFilter(d: Draft): Partial<Record<keyof Draft, string>> {
  const e: Partial<Record<keyof Draft, string>> = {};
  if (!UUID.test(d.ledgerId.trim())) e.ledgerId = "Choose the ledger.";
  if (d.accountFrom && !ACCOUNT.test(d.accountFrom)) e.accountFrom = "Up to 20 letters, digits, dots, underscores or dashes.";
  if (d.accountTo && !ACCOUNT.test(d.accountTo)) e.accountTo = "Up to 20 letters, digits, dots, underscores or dashes.";
  if (d.dateFrom && !DATE.test(d.dateFrom)) e.dateFrom = "Dates use the form YYYY-MM-DD.";
  if (d.dateTo && !DATE.test(d.dateTo)) e.dateTo = "Dates use the form YYYY-MM-DD.";
  if (!e.dateFrom && !e.dateTo && d.dateFrom && d.dateTo && d.dateTo < d.dateFrom) e.dateTo = "Must not be before Posting date from.";
  if (d.voucher.length > 30) e.voucher = "Vouchers are at most 30 characters.";
  if (parseDimensionFilter(d.dimensions) === null) e.dimensions = "Use ATTRIBUTE=value pairs separated by commas, such as DEPT=D100, CC=C1.";
  return e;
}

/** The API query for the filters in the address, or null while they are missing or invalid. */
function queryFrom(d: Draft): JournalLineQuery | null {
  if (Object.keys(validateLineFilter(d)).length > 0) return null;
  const dims = parseDimensionFilter(d.dimensions) ?? {};
  return {
    ledgerId: d.ledgerId.trim(),
    accountFrom: d.accountFrom || undefined,
    accountTo: d.accountTo || undefined,
    dateFrom: d.dateFrom || undefined,
    dateTo: d.dateTo || undefined,
    voucher: d.voucher || undefined,
    dimensions: Object.keys(dims).length > 0 ? dims : undefined,
    limit: LINES_PAGE_SIZE,
  };
}

const columns: DataTableColumn<JournalLine>[] = [
  { key: "voucher", header: "Voucher", mono: true },
  { key: "postingDate", header: "Posting date" },
  { key: "displayName", header: "Account", mono: true },
  { key: "dimensions", header: "Dimensions", mono: true, render: (l) => formatDimensions(l.dimensions) },
  {
    key: "debitCredit",
    header: "D/C",
    render: (l) => (
      <>
        {l.debitCredit === "D" ? "Debit" : "Credit"}
        {l.negativePosting ? (
          <>
            {" "}
            <Badge tone="warning">Storno</Badge>
          </>
        ) : null}
      </>
    ),
  },
  { key: "amount", header: "Amount", align: "end", render: (l) => `${l.amount} ${l.currency}` },
  { key: "quantity", header: "Quantity", align: "end", render: (l) => (l.quantity ? `${l.quantity} ${l.unit ?? ""}`.trim() : "") },
  { key: "text", header: "Text", render: (l) => l.text || l.description || "" },
  { key: "source", header: "Source", render: (l) => `${l.sourceSystem} ${l.sourceType} ${l.sourceId}` },
  { key: "traceId", header: "Trace", mono: true },
];

function errorText(error: unknown): string {
  if (error instanceof LedgerApiError) {
    const fields = Object.entries(error.fields).map(([k, v]) => `${k} ${v}`);
    return fields.length > 0 ? `${error.message}: ${fields.join("; ")}` : error.message;
  }
  return error instanceof Error ? error.message : "The request failed.";
}

/**
 * Ledger transactions (FR-11.14, PRD §2.11.6): posted lines of a ledger in
 * posting order, filtered by account range, posting dates, voucher and
 * dimension values, read a keyset page at a time. Filters live in the address
 * so a search can be shared; opening a line drills to its journal entry, which
 * shows the source document and trace.
 */
export function LedgerLinesPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const applied = draftFrom(params);
  const query = queryFrom(applied);
  const [draft, setDraft] = useState<Draft>(applied);
  const [errors, setErrors] = useState<Partial<Record<keyof Draft, string>>>({});

  const result = useInfiniteQuery({
    queryKey: ["ledger", "journal-lines", query],
    queryFn: ({ pageParam }) => fetchJournalLines({ ...(query as JournalLineQuery), cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    enabled: query !== null,
  });
  const rows = result.data?.pages.flatMap((p) => p.items) ?? [];

  function set<K extends keyof Draft>(key: K, value: string) {
    setDraft({ ...draft, [key]: value });
  }

  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const trimmed: Draft = { ...draft, ledgerId: draft.ledgerId.trim(), accountFrom: draft.accountFrom.trim(), accountTo: draft.accountTo.trim(), voucher: draft.voucher.trim() };
    const found = validateLineFilter(trimmed);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    const next: Record<string, string> = {};
    for (const [key, param] of Object.entries(FIELDS) as [keyof Draft, string][]) if (trimmed[key]) next[param] = trimmed[key];
    setParams(next);
  }

  const field = (key: keyof Draft, id: string, label: string, extra: { placeholder?: string; hint?: string; optional?: boolean; mono?: boolean } = {}) => (
    <FormField id={id} label={label} hint={extra.hint} optional={extra.optional} error={errors[key]}>
      {(c) => (
        <Input
          {...c}
          className={extra.mono ? styles.mono : undefined}
          placeholder={extra.placeholder}
          value={draft[key]}
          onChange={(e) => set(key, e.target.value)}
          invalid={Boolean(errors[key])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Ledger transactions" description="Posted journal lines in posting order. Open a line to see its entry, source document and trace." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Ledger transaction filters">
          <LookupFormField id="lq-ledger" label="Ledger" entity="ledger" value={draft.ledgerId} error={errors.ledgerId} onChange={(id) => set("ledgerId", id)} />
          {field("accountFrom", "lq-account-from", "Account from", { optional: true, placeholder: "6000" })}
          {field("accountTo", "lq-account-to", "Account to", { optional: true, placeholder: "6999" })}
          {field("dateFrom", "lq-date-from", "Posting date from", { optional: true, placeholder: "2026-01-01" })}
          {field("dateTo", "lq-date-to", "Posting date to", { optional: true, placeholder: "2026-01-31" })}
          {field("voucher", "lq-voucher", "Voucher", { optional: true, mono: true })}
          {field("dimensions", "lq-dims", "Dimension values", { optional: true, hint: "For example DEPT=D100, CC=C1. Every pair must match; an empty value (CC=) keeps lines without that attribute.", mono: true })}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Show lines
            </Button>
          </div>
        </form>
      </Card>
      {query === null ? (
        <p className={styles.note}>Choose a ledger to list its posted lines.</p>
      ) : result.isError && rows.length === 0 ? (
        <Alert tone="danger" title="Could not load the ledger lines" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {errorText(result.error)}
        </Alert>
      ) : (
        <>
          <DataTable
            label="Ledger transactions"
            columns={columns}
            rows={rows}
            getRowKey={(l) => l.id}
            loading={result.isPending}
            empty={{ title: "No lines match", description: "Nothing is posted to this ledger for these filters. Widen the account range or the posting dates." }}
            onRowOpen={(l) => navigate(`/ledger/entries/${l.entryId}`)}
          />
          {result.isFetchNextPageError ? (
            <Alert tone="danger" title="Could not load more lines" live="alert" action={<Button size="sm" onClick={() => void result.fetchNextPage()}>Try again</Button>}>
              {errorText(result.error)}
            </Alert>
          ) : null}
          {result.hasNextPage && !result.isFetchNextPageError ? (
            <div className={styles.formActions}>
              <Button loading={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>
                Load more lines
              </Button>
              <span className={styles.note}>{rows.length} lines shown</span>
            </div>
          ) : null}
          {rows.length > 0 && query ? <ExportButton label="Export lines (CSV)" run={() => exportJournalLinesCsv(query)} /> : null}
        </>
      )}
    </div>
  );
}
