import { useEffect, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listLibrary, OUTPUT_FORMATS, type LibraryEntry, type LibraryPage as Page, type LibraryQuery, type OutputFormat, type Problem } from "./api/client";
import { formatBytes, periodLabel, shortHash, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface LibraryValues {
  definition: string;
  ledger: string;
  schedule: string;
  format: OutputFormat | "";
  period: string;
}

type LibraryErrors = Partial<Record<keyof LibraryValues, string>>;

const PERIOD = /^\d{4}-\d{2}$/;

export function blankLibraryValues(): LibraryValues {
  return { definition: "", ledger: "", schedule: "", format: "", period: "" };
}

/** The checks the service makes (a UUID ledger, a period as YYYY-PP). */
export function validateLibrary(v: LibraryValues): LibraryErrors {
  const errors: LibraryErrors = {};
  if (v.ledger.trim() !== "" && !UUID.test(v.ledger.trim())) errors.ledger = "Enter the ledger id (a UUID), or leave it empty for every ledger.";
  if (v.period.trim() !== "" && !PERIOD.test(v.period.trim())) errors.period = "Enter the fiscal year and period number, such as 2026-03.";
  return errors;
}

export function toLibraryQuery(v: LibraryValues): LibraryQuery {
  const q: LibraryQuery = {};
  if (v.definition.trim() !== "") q.definition = v.definition.trim().toUpperCase();
  if (v.ledger.trim() !== "") q.ledger = v.ledger.trim();
  if (v.schedule.trim() !== "") q.schedule = v.schedule.trim().toUpperCase();
  if (v.format !== "") q.format = v.format;
  if (v.period.trim() !== "") q.period = v.period.trim();
  return q;
}

export const columns: DataTableColumn<LibraryEntry>[] = [
  { key: "definitionCode", header: "Statement", mono: true, render: (e) => `${e.definitionCode} v${e.definitionVersion}` },
  { key: "period", header: "Period", mono: true, render: (e) => periodLabel(e.periodYear, e.periodNo) },
  { key: "ledgerId", header: "Ledger", mono: true, render: (e) => e.ledgerId.slice(0, 8) },
  { key: "format", header: "Format", render: (e) => e.format.toUpperCase() },
  { key: "sizeBytes", header: "Size", mono: true, align: "end", render: (e) => formatBytes(e.sizeBytes) },
  { key: "sha256", header: "SHA-256", mono: true, render: (e) => shortHash(e.sha256) },
  { key: "scheduleCode", header: "Schedule", mono: true },
  { key: "createdAt", header: "Generated", mono: true, render: (e) => <time dateTime={e.createdAt}>{e.createdAt}</time> },
  { key: "retainUntil", header: "Kept until", mono: true, render: (e) => <time dateTime={e.retainUntil}>{e.retainUntil.slice(0, 10)}</time> },
];

/** The report library (FR-15.8): the files of scheduled statement runs, by definition, period, entity and format. */
export function LibraryPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<LibraryValues>(blankLibraryValues);
  const [errors, setErrors] = useState<LibraryErrors>({});
  const [query, setQuery] = useState<LibraryQuery>({});
  const key = JSON.stringify(query);
  const first = useLoad((signal) => listLibrary(query, undefined, signal), key);
  const [more, setMore] = useState<Page[]>([]);
  const [moreBusy, setMoreBusy] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  useEffect(() => {
    setMore([]);
    setMoreError(null);
  }, [key]);

  const pages = first.data ? [first.data, ...more] : [];
  const items = pages.flatMap((p) => p.items);
  const nextCursor = pages.length > 0 ? pages[pages.length - 1].nextCursor : null;
  const change = (patch: Partial<LibraryValues>) => setValues((v) => ({ ...v, ...patch }));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const found = validateLibrary(values);
    setErrors(found);
    if (Object.keys(found).length === 0) setQuery(toLibraryQuery(values));
  };

  const loadMore = async () => {
    if (!nextCursor) return;
    setMoreBusy(true);
    setMoreError(null);
    try {
      const page = await listLibrary(query, nextCursor);
      setMore((m) => [...m, page]);
    } catch (failure) {
      setMoreError(describeProblem(failure));
    } finally {
      setMoreBusy(false);
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Report library"
        description="The files that scheduled statement runs produced, kept for the retention period. Open a file to see its SHA-256 and download exactly what was distributed."
      />
      <form onSubmit={submit} noValidate aria-label="Library filter">
        <Card title="Filter">
          <div className={styles.form}>
            <FormField id="fr-l-definition" label="Statement" hint="Definition code." optional>
              {(c) => <Input {...c} className={styles.mono} value={values.definition} onChange={(e) => change({ definition: e.target.value })} />}
            </FormField>
            <FormField id="fr-l-period" label="Period" hint="Fiscal year and period, such as 2026-03." optional error={errors.period}>
              {(c) => <Input {...c} className={styles.mono} value={values.period} invalid={Boolean(errors.period)} onChange={(e) => change({ period: e.target.value })} />}
            </FormField>
            <FormField id="fr-l-ledger" label="Ledger" hint="Ledger id. Empty means every ledger." optional error={errors.ledger}>
              {(c) => <Input {...c} className={styles.mono} value={values.ledger} invalid={Boolean(errors.ledger)} onChange={(e) => change({ ledger: e.target.value })} />}
            </FormField>
            <FormField id="fr-l-schedule" label="Schedule" hint="Schedule code." optional>
              {(c) => <Input {...c} className={styles.mono} value={values.schedule} onChange={(e) => change({ schedule: e.target.value })} />}
            </FormField>
            <FormField id="fr-l-format" label="Format" optional>
              {(c) => (
                <Select {...c} value={values.format} onChange={(e) => change({ format: e.target.value as OutputFormat | "" })}>
                  <option value="">Every format</option>
                  {OUTPUT_FORMATS.map((f) => (
                    <option key={f} value={f}>
                      {f.toUpperCase()}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary">
                Show files
              </Button>
            </div>
          </div>
        </Card>
      </form>
      {first.error ? (
        <Alert tone="danger" title="Could not load the report library" live="alert" action={<Button size="sm" onClick={first.reload}>Try again</Button>}>
          {first.error.message} ({first.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Library files"
          columns={columns}
          rows={items}
          getRowKey={(e) => e.id}
          loading={first.loading && !first.data}
          empty={{ title: "No files in the library", description: "Scheduled statement runs store their files here. Nothing matches this filter, or no schedule has run yet." }}
          onRowOpen={(e) => navigate(`/finreport/library/${e.id}`)}
        />
      )}
      {nextCursor && !first.error ? (
        <div className={styles.drillFooter}>
          <span className={styles.note}>Showing the newest {items.length} files.</span>
          <Button onClick={loadMore} loading={moreBusy}>
            Load more
          </Button>
        </div>
      ) : null}
      {moreError ? (
        <Alert tone="danger" live="alert" title="Could not load more files">
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
    </div>
  );
}
