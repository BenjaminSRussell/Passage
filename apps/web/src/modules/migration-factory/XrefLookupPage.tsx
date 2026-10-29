import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, lookupXref, type Problem, type Xref } from "./api/client";
import { formatDateTime, SOURCE_LABELS, SOURCE_SYSTEMS, type SourceSystem } from "./domain";
import styles from "./pages.module.css";

const TABLES = ["open_items", "project_wip"] as const;

const columns: DataTableColumn<Xref>[] = [
  { key: "system", header: "System", render: (row) => SOURCE_LABELS[row.system] ?? row.system },
  { key: "company", header: "Company", mono: true },
  { key: "table", header: "Table", mono: true },
  { key: "legacyId", header: "Legacy id", mono: true },
  { key: "passageEntity", header: "Passage record", mono: true },
  { key: "passageId", header: "Passage id", mono: true },
  { key: "runId", header: "Loaded by run", render: (row) => <Link to={`/migration-factory/runs/${row.runId}`}>Open run</Link> },
  { key: "createdAt", header: "Loaded", render: (row) => formatDateTime(row.createdAt) },
];

interface Query {
  system: SourceSystem;
  company: string;
  table: string;
  id: string;
}

/** Legacy reference lookup: resolve a legacy id to the Passage record it became (FR-49.3, 9.49.14). */
export function XrefLookupPage() {
  const [query, setQuery] = useState<Query>({ system: "AX2012", company: "", table: "open_items", id: "" });
  const [idError, setIdError] = useState<string | undefined>();
  const [results, setResults] = useState<Xref[] | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const id = query.id.trim();
    if (id === "") {
      setIdError("Enter the legacy id, such as a voucher or invoice id.");
      return;
    }
    setIdError(undefined);
    setFailure(null);
    setBusy(true);
    try {
      setResults(await lookupXref({ system: query.system, table: query.table, id, company: query.company.trim() || undefined }));
    } catch (error) {
      setResults(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Legacy reference" description="Find the Passage record a legacy AX or SAP record was migrated to, without the legacy system online." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="xref-system" label="System">
            {(control) => (
              <Select {...control} value={query.system} onChange={(event) => setQuery({ ...query, system: event.target.value as SourceSystem })}>
                {SOURCE_SYSTEMS.map((s) => (
                  <option key={s} value={s}>
                    {SOURCE_LABELS[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="xref-company" label="Company" hint="DataAreaId or BUKRS; blank searches every company." optional>
            {(control) => <Input {...control} value={query.company} onChange={(event) => setQuery({ ...query, company: event.target.value })} />}
          </FormField>
          <FormField id="xref-table" label="Pack table">
            {(control) => (
              <Select {...control} value={query.table} onChange={(event) => setQuery({ ...query, table: event.target.value })}>
                {TABLES.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="xref-id" label="Legacy id" error={idError}>
            {(control) => <Input {...control} value={query.id} invalid={Boolean(idError)} onChange={(event) => setQuery({ ...query, id: event.target.value })} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Look up
            </Button>
          </div>
        </form>
      </Card>
      {failure ? (
        <Alert tone="danger" title="Could not look up the legacy id" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {busy || (results && results.length > 0) ? (
        <DataTable label="Legacy references" columns={columns} rows={results ?? []} getRowKey={(row) => row.xrefId} loading={busy} />
      ) : results ? (
        <EmptyState title="No legacy reference" description="No migrated record has this legacy id in this system and table." />
      ) : null}
    </div>
  );
}
