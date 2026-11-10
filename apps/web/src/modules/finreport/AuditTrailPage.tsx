import { useEffect, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  AUDIT_KINDS,
  describeProblem,
  exportAuditTrail,
  listAuditTrail,
  type AuditEntry,
  type AuditExport,
  type AuditKind,
  type AuditPage,
  type AuditQuery,
  type Problem,
} from "./api/client";
import { httpUrl } from "./drill";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export const AUDIT_KIND_LABELS: Record<AuditKind, string> = {
  posted: "Journal posted",
  replayed: "Posting replayed",
  reversed: "Journal reversed",
  batch_submitted: "Batch submitted",
  batch_approved: "Batch approved",
  batch_rejected: "Batch rejected",
  period_changed: "Period status changed",
  reopen_approved: "Reopen approved",
  rate_override: "Rate override",
  tax_fallback: "Tax fallback",
  allocation_run: "Allocation run",
  year_end_run: "Year-end run",
  rebuild: "Balance rebuild",
  statement_run: "Statement run",
  definition_published: "Definition published",
  definition_retired: "Definition retired",
  hierarchy_published: "Hierarchy published",
  schedule_saved: "Schedule saved",
  audit_exported: "Audit trail exported",
  legal_hold_placed: "Legal hold placed",
  legal_hold_released: "Legal hold released",
  library_swept: "Library swept",
  runs_swept: "Ad-hoc runs swept",
  share_link_created: "Share link created",
  share_link_revoked: "Share link revoked",
};

export interface AuditValues {
  ledger: string;
  period: string;
  kind: AuditKind | "";
  actor: string;
  /** YYYY-MM-DD, inclusive. */
  fromDate: string;
  /** YYYY-MM-DD, inclusive. */
  toDate: string;
}

type AuditErrors = Partial<Record<keyof AuditValues, string>>;

const PERIOD = /^\d{4}-\d{2,3}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const DAY_MS = 24 * 60 * 60 * 1000;
const MAX_WINDOW_DAYS = 366;

function isoDate(d: Date): string {
  return d.toISOString().slice(0, 10);
}

function dayStart(date: string): number {
  return Date.parse(`${date}T00:00:00Z`);
}

/** The current month so far, in UTC: the window the page opens with. */
export function defaultAuditValues(today: Date = new Date()): AuditValues {
  const first = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), 1));
  return { ledger: "", period: "", kind: "", actor: "", fromDate: isoDate(first), toDate: isoDate(today) };
}

/** The checks the service makes (a period, or a window of at most 366 days; a UUID ledger). */
export function validateAudit(v: AuditValues): AuditErrors {
  const errors: AuditErrors = {};
  const ledger = v.ledger.trim();
  const period = v.period.trim();
  if (ledger !== "" && !UUID.test(ledger)) errors.ledger = "Enter the ledger id (a UUID), or leave it empty for every ledger.";
  if (period !== "" && !PERIOD.test(period)) errors.period = "Enter the fiscal year and period number, such as 2026-03.";
  const hasFrom = v.fromDate !== "";
  const hasTo = v.toDate !== "";
  if (hasFrom && !DATE.test(v.fromDate)) errors.fromDate = "Enter a date.";
  if (hasTo && !DATE.test(v.toDate)) errors.toDate = "Enter a date.";
  if (errors.fromDate || errors.toDate) return errors;
  if (hasFrom && hasTo) {
    const days = (dayStart(v.toDate) - dayStart(v.fromDate)) / DAY_MS + 1;
    if (days < 1) errors.toDate = "The end date is on or after the start date.";
    else if (period === "" && days > MAX_WINDOW_DAYS) errors.toDate = `The dates may span at most ${MAX_WINDOW_DAYS} days.`;
  } else if (period === "") {
    const missing = hasFrom ? "toDate" : "fromDate";
    errors[missing] = "Enter both dates, or a period.";
  }
  return errors;
}

/** The query the service expects: from inclusive, to exclusive (the day after the end date), in UTC. */
export function toAuditQuery(v: AuditValues): AuditQuery {
  const q: AuditQuery = {};
  if (v.ledger.trim() !== "") q.ledger = v.ledger.trim();
  if (v.period.trim() !== "") q.period = v.period.trim();
  if (v.kind !== "") q.kind = v.kind;
  if (v.actor.trim() !== "") q.actor = v.actor.trim();
  if (v.fromDate !== "") q.from = `${v.fromDate}T00:00:00Z`;
  if (v.toDate !== "") q.to = new Date(dayStart(v.toDate) + DAY_MS).toISOString().replace(".000Z", "Z");
  return q;
}

/** Details as "key: value" pairs in key order; nested values as JSON. */
export function formatDetails(details: Record<string, unknown>): string {
  return Object.keys(details)
    .sort()
    .map((k) => {
      const value = details[k];
      return `${k}: ${typeof value === "string" ? value : JSON.stringify(value)}`;
    })
    .join("; ");
}

function TraceLink({ entry }: { entry: AuditEntry }) {
  if (!entry.traceId) {
    return <span className={styles.muted}>None</span>;
  }
  const href = httpUrl(entry.traceUrl);
  if (!href) {
    return <span className={styles.mono}>{entry.traceId}</span>;
  }
  return (
    <a className={`${styles.link} ${styles.mono}`} href={href} target="_blank" rel="noopener noreferrer" aria-label={`Open trace ${entry.traceId} (opens in a new tab)`}>
      {entry.traceId.slice(0, 16)}
    </a>
  );
}

const columns: DataTableColumn<AuditEntry>[] = [
  { key: "occurredAt", header: "Occurred at", mono: true, render: (e) => <time dateTime={e.occurredAt}>{e.occurredAt}</time> },
  { key: "kind", header: "Kind", render: (e) => AUDIT_KIND_LABELS[e.kind] ?? e.kind },
  { key: "actorId", header: "Actor", mono: true },
  { key: "objectRef", header: "Object", mono: true },
  { key: "period", header: "Period", mono: true, render: (e) => e.period ?? "" },
  { key: "details", header: "Details", render: (e) => formatDetails(e.details) },
  { key: "traceId", header: "Trace", render: (e) => <TraceLink entry={e} /> },
];

function saveFile(name: string, text: string) {
  if (typeof URL.createObjectURL !== "function") return;
  const url = URL.createObjectURL(new Blob([text], { type: "text/csv" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

/** Audit trail report (FR-15.10): who posted, approved, reversed or reopened what, and who changed reporting objects; export with a SHA-256. */
export function AuditTrailPage() {
  const [values, setValues] = useState<AuditValues>(() => defaultAuditValues());
  const [errors, setErrors] = useState<AuditErrors>({});
  const [query, setQuery] = useState<AuditQuery>(() => toAuditQuery(defaultAuditValues()));
  const key = JSON.stringify(query);
  const first = useLoad((signal) => listAuditTrail(query, undefined, signal), key);
  const [more, setMore] = useState<AuditPage[]>([]);
  const [moreBusy, setMoreBusy] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [selected, setSelected] = useState<AuditEntry | null>(null);
  const [exporting, setExporting] = useState(false);
  const [exported, setExported] = useState<AuditExport | null>(null);
  const [exportError, setExportError] = useState<Problem | null>(null);

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setSelected(null);
  }, [key]);

  const pages = first.data ? [first.data, ...more] : [];
  const items = pages.flatMap((p) => p.items);
  const nextCursor = pages.length > 0 ? pages[pages.length - 1].nextCursor : null;

  const change = (patch: Partial<AuditValues>) => setValues((v) => ({ ...v, ...patch }));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const found = validateAudit(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setExported(null);
    setExportError(null);
    setQuery(toAuditQuery(values));
  };

  const loadMore = async () => {
    if (!nextCursor) return;
    setMoreBusy(true);
    setMoreError(null);
    try {
      const page = await listAuditTrail(query, nextCursor);
      setMore((m) => [...m, page]);
    } catch (failure) {
      setMoreError(describeProblem(failure));
    } finally {
      setMoreBusy(false);
    }
  };

  const runExport = async () => {
    setExporting(true);
    setExported(null);
    setExportError(null);
    try {
      const result = await exportAuditTrail(query);
      setExported(result);
      saveFile(result.fileName, result.csv);
    } catch (failure) {
      setExportError(describeProblem(failure));
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Audit trail"
        description="Who posted, approved, reversed or reopened what in a ledger, and who ran, published or scheduled statements. Export gives a CSV file with its SHA-256 so the evidence can be verified later."
      />
      <form onSubmit={submit} noValidate aria-label="Audit trail filter">
        <Card title="Filter">
          <div className={styles.form}>
            <FormField id="fr-a-from" label="From" hint="Start date, UTC." error={errors.fromDate}>
              {(c) => <Input {...c} type="date" value={values.fromDate} invalid={Boolean(errors.fromDate)} onChange={(e) => change({ fromDate: e.target.value })} />}
            </FormField>
            <FormField id="fr-a-to" label="To" hint="End date, UTC, included. At most 366 days after the start." error={errors.toDate}>
              {(c) => <Input {...c} type="date" value={values.toDate} invalid={Boolean(errors.toDate)} onChange={(e) => change({ toDate: e.target.value })} />}
            </FormField>
            <FormField id="fr-a-period" label="Period" hint="Fiscal year and period, such as 2026-03. With a period the dates may be left empty." optional error={errors.period}>
              {(c) => <Input {...c} className={styles.mono} value={values.period} invalid={Boolean(errors.period)} onChange={(e) => change({ period: e.target.value })} />}
            </FormField>
            <FormField id="fr-a-ledger" label="Ledger" hint="Ledger id. Empty means every ledger." optional error={errors.ledger}>
              {(c) => <Input {...c} className={styles.mono} value={values.ledger} invalid={Boolean(errors.ledger)} onChange={(e) => change({ ledger: e.target.value })} />}
            </FormField>
            <FormField id="fr-a-kind" label="Kind" optional>
              {(c) => (
                <Select {...c} value={values.kind} onChange={(e) => change({ kind: e.target.value as AuditKind | "" })}>
                  <option value="">Every kind</option>
                  {AUDIT_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {AUDIT_KIND_LABELS[k]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="fr-a-actor" label="Actor" hint="User id." optional>
              {(c) => <Input {...c} className={styles.mono} value={values.actor} onChange={(e) => change({ actor: e.target.value })} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary">
                Show entries
              </Button>
              <Button onClick={runExport} loading={exporting}>
                Export CSV
              </Button>
            </div>
          </div>
        </Card>
      </form>

      {exported ? (
        <Alert tone="success" live="status" title="Audit trail exported">
          {exported.entries} {exported.entries === 1 ? "entry" : "entries"} in {exported.fileName}. SHA-256 <span className={styles.mono}>{exported.sha256}</span>. The export is
          recorded in the audit trail as <span className={styles.mono}>audit-export:{exported.id}</span>.
        </Alert>
      ) : null}
      {exportError ? (
        <Alert tone="danger" live="alert" title={exportError.code === "REPORT-0030" ? "Too many entries for one export" : "The export failed"}>
          <ProblemMessage problem={exportError} />
        </Alert>
      ) : null}

      {first.error ? (
        <Alert tone="danger" title="Could not load the audit trail" live="alert" action={<Button size="sm" onClick={first.reload}>Try again</Button>}>
          <ProblemMessage problem={first.error} />
        </Alert>
      ) : (
        <DataTable
          label="Audit entries"
          columns={columns}
          rows={items}
          getRowKey={(e) => e.id}
          loading={first.loading && !first.data}
          empty={{ title: "No audit entries", description: "Nothing matches this filter. Widen the dates, or clear the ledger, kind or actor." }}
          onRowOpen={(e) => setSelected(e)}
        />
      )}
      {nextCursor && !first.error ? (
        <div className={styles.drillFooter}>
          <span className={styles.note}>
            Showing the first {items.length} entries, oldest first.
          </span>
          <Button onClick={loadMore} loading={moreBusy}>
            Load more
          </Button>
        </div>
      ) : null}
      {moreError ? (
        <Alert tone="danger" live="alert" title="Could not load more entries">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}

      {selected ? (
        <Card
          title={`${AUDIT_KIND_LABELS[selected.kind] ?? selected.kind} at ${selected.occurredAt}`}
          actions={
            <Button size="sm" variant="ghost" onClick={() => setSelected(null)}>
              Close
            </Button>
          }
        >
          <dl className={styles.details}>
            {[
              ["Entry", selected.id],
              ["Actor", selected.actorId],
              ["Object", selected.objectRef],
              ["Ledger", selected.ledgerId ?? "None"],
              ["Period", selected.period ?? "None"],
            ].map(([term, value]) => (
              <div key={term} className={styles.entry}>
                <dt className={styles.term}>{term}</dt>
                <dd className={styles.value}>{value}</dd>
              </div>
            ))}
            <div className={styles.entry}>
              <dt className={styles.term}>Trace</dt>
              <dd className={styles.value}>
                <TraceLink entry={selected} />
              </dd>
            </div>
          </dl>
          <pre className={styles.json} aria-label="Entry details">
            {JSON.stringify(selected.details, null, 2)}
          </pre>
        </Card>
      ) : null}
      <p className={styles.note}>
        Postings, approvals, reversals and period changes appear here once the ledger, approval and period events are connected to the reporting service; changes to
        reporting objects are recorded as they happen.
      </p>
    </div>
  );
}
