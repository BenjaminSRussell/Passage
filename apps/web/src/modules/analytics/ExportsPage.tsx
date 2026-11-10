import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  type DataTableColumn,
} from "../../shell/ui";
import {
  ASYNC_EXPORT_ROWS,
  createExport,
  describeProblem,
  EXPORT_FORMATS,
  listExports,
  MAX_EXPORT_ROWS,
  type ExportFilter,
  type ExportInput,
  type ExportJob,
  type Problem,
  type QueryInput,
} from "./api/client";
import { count, DATE, dateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function ExportStatus({ job }: { job: ExportJob }) {
  switch (job.status) {
    case "READY":
      return <Badge tone="success">Ready</Badge>;
    case "DELETED":
      return <Badge tone="neutral">Deleted</Badge>;
    default:
      return <Badge tone="info">Queued</Badge>;
  }
}

const columns: DataTableColumn<ExportJob>[] = [
  { key: "createdAt", header: "Requested", render: (e) => dateTime(e.createdAt) },
  { key: "requestedBy", header: "Requested by", mono: true },
  { key: "measures", header: "Metrics", mono: true, render: (e) => e.plan.measures.join(", ") },
  { key: "format", header: "Format" },
  { key: "mode", header: "Runs as", render: (e) => (e.mode === "TEMPORAL_JOB" ? "Background job" : "Inline") },
  { key: "rowCount", header: "Rows", align: "end", render: (e) => count(e.rowCount) },
  { key: "status", header: "Status", render: (e) => <ExportStatus job={e} /> },
  { key: "linkExpiresAt", header: "Link valid until", render: (e) => dateTime(e.linkExpiresAt) },
  { key: "deleteAt", header: "File deleted", render: (e) => dateTime(e.deleteAt) },
];

type FilterErrors = Partial<Record<keyof ExportFilter, string>>;

/** Checks that mirror the export log query parameters of GET /analytics/exports. */
export function validateExportFilter(f: Required<ExportFilter>): FilterErrors {
  const errors: FilterErrors = {};
  if (f.user.length > 255) errors.user = "Enter at most 255 characters.";
  if (f.from !== "" && !DATE.test(f.from)) errors.from = "Choose a day.";
  if (f.to !== "" && !DATE.test(f.to)) errors.to = "Choose a day.";
  else if (f.from !== "" && f.to !== "" && f.to < f.from) errors.to = "The last day must not be before the first day.";
  return errors;
}

const noFilter: Required<ExportFilter> = { user: "", format: "", from: "", to: "" };

/** Export log (PRD §9.46.14): every explorer export with its row count, format and file lifetime. */
export function ExportsPage() {
  const [draft, setDraft] = useState<Required<ExportFilter>>(noFilter);
  const [applied, setApplied] = useState<Required<ExportFilter>>(noFilter);
  const [errors, setErrors] = useState<FilterErrors>({});
  const state = useLoad((signal) => listExports(applied, undefined, signal), `exports/${JSON.stringify(applied)}`);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateExportFilter(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) setApplied({ ...draft, user: draft.user.trim() });
  }

  const set = (key: keyof ExportFilter, value: string) => setDraft((d) => ({ ...d, [key]: value }));
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Exports"
        description="Explorer exports to XLSX and CSV. Files are deleted 24 hours after they are created, and an identical request within an hour returns the existing file."
        actions={
          <Link className={buttonClassName("primary")} to="/analytics/explorer">
            New export
          </Link>
        }
      />
      <Card>
        <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Filter exports">
          <FormField id="exports-user" label="Requested by" optional error={errors.user}>
            {(control) => <Input {...control} className={styles.mono} invalid={Boolean(errors.user)} value={draft.user} onChange={(e) => set("user", e.target.value)} />}
          </FormField>
          <FormField id="exports-format" label="Format">
            {(control) => (
              <Select {...control} value={draft.format} onChange={(e) => set("format", e.target.value)}>
                <option value="">Any format</option>
                {EXPORT_FORMATS.map((f) => (
                  <option key={f} value={f}>
                    {f}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="exports-from" label="From" optional error={errors.from}>
            {(control) => <Input {...control} type="date" invalid={Boolean(errors.from)} value={draft.from} onChange={(e) => set("from", e.target.value)} />}
          </FormField>
          <FormField id="exports-to" label="To" optional error={errors.to}>
            {(control) => <Input {...control} type="date" invalid={Boolean(errors.to)} value={draft.to} onChange={(e) => set("to", e.target.value)} />}
          </FormField>
          <Button type="submit">Apply filters</Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone="danger" title="Could not load exports" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Export log"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(e) => e.exportId}
          loading={state.loading}
          empty={{ title: "No exports found", description: "Exports started from the explorer appear here with their row count and link lifetime." }}
        />
      )}
    </div>
  );
}

interface ExportDraft {
  format: ExportInput["format"];
  rowCount: string;
  linkTtlHours: string;
}
type ExportErrors = Partial<Record<keyof ExportDraft, string>>;

/** Checks that mirror the export policy in services/analytics (1,000,000-row cap, 1 to 168 hour link). */
export function validateExport(d: ExportDraft): ExportErrors {
  const errors: ExportErrors = {};
  if (!/^\d+$/.test(d.rowCount)) errors.rowCount = "Enter the number of rows as a whole number.";
  else if (Number(d.rowCount) > MAX_EXPORT_ROWS) errors.rowCount = `Exports are limited to ${count(MAX_EXPORT_ROWS)} rows. Narrow the query.`;
  const ttl = Number(d.linkTtlHours);
  if (!/^\d+$/.test(d.linkTtlHours) || ttl < 1 || ttl > 168) errors.linkTtlHours = "Enter 1 to 168 hours.";
  return errors;
}

/**
 * Exports the query whose plan the explorer shows (FR-46.9). The service plans
 * it again with the caller's scope, so an export never holds more than the
 * caller may see. One idempotency key per request makes a retried submit safe.
 */
export function ExportPanel({ query }: { query: QueryInput }) {
  const [draft, setDraft] = useState<ExportDraft>({ format: "XLSX", rowCount: "", linkTtlHours: "24" });
  const [errors, setErrors] = useState<ExportErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [job, setJob] = useState<ExportJob | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef<{ body: string; key: string } | null>(null);
  const set = <K extends keyof ExportDraft>(k: K, value: ExportDraft[K]) => setDraft((d) => ({ ...d, [k]: value }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateExport(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const input: ExportInput = { ...query, format: draft.format, rowCount: Number(draft.rowCount), linkTtlHours: Number(draft.linkTtlHours) };
    const body = JSON.stringify(input);
    if (key.current?.body !== body) key.current = { body, key: crypto.randomUUID() };
    setBusy(true);
    try {
      setJob(await createExport(input, key.current.key));
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      setErrors(problem.fields as ExportErrors);
      setJob(null);
    } finally {
      setBusy(false);
    }
  }

  const rows = Number(draft.rowCount);
  const background = /^\d+$/.test(draft.rowCount) && rows > ASYNC_EXPORT_ROWS;
  return (
    <Card title="Export">
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="Export query">
        <FormField id="export-format" label="Format">
          {(control) => (
            <Select {...control} value={draft.format} onChange={(e) => set("format", e.target.value as ExportDraft["format"])}>
              {EXPORT_FORMATS.map((f) => (
                <option key={f} value={f}>
                  {f}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField
          id="export-rows"
          label="Rows"
          hint={background ? "Above 100,000 rows the export runs as a background job." : "The number of rows the query returns."}
          error={errors.rowCount}
        >
          {(control) => <Input {...control} inputMode="numeric" invalid={Boolean(errors.rowCount)} value={draft.rowCount} onChange={(e) => set("rowCount", e.target.value)} />}
        </FormField>
        <FormField id="export-ttl" label="Link valid for (hours)" hint="1 to 168 hours; 24 by default. The link ends when the file is deleted, 24 hours after it is created." error={errors.linkTtlHours}>
          {(control) => (
            <Input {...control} inputMode="numeric" invalid={Boolean(errors.linkTtlHours)} value={draft.linkTtlHours} onChange={(e) => set("linkTtlHours", e.target.value)} />
          )}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The export was not started" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Export
          </Button>
          <Link to="/analytics/exports">Export log</Link>
        </div>
      </form>
      <p role="status" className={styles.muted}>
        {job
          ? job.reused
            ? `An identical export from the last hour was returned: ${count(job.rowCount)} rows, link valid until ${dateTime(job.linkExpiresAt)}.`
            : `Export queued: ${count(job.rowCount)} rows as ${job.format}${job.mode === "TEMPORAL_JOB" ? " in a background job" : ""}, link valid until ${dateTime(job.linkExpiresAt)}.`
          : ""}
      </p>
    </Card>
  );
}
