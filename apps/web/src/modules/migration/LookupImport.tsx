import { useState, type ChangeEvent, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Select, Textarea } from "../../shell/ui";
import { describeProblem, importLookupEntries, type LookupImportMode, type LookupTable, type Problem } from "./api/client";
import { checkImportText, IMPORT_HINTS, importOutcome, MAX_IMPORT_BYTES } from "./lookupDomain";
import styles from "./pages.module.css";

/** Load entries from a spreadsheet export into the next version of the table. */
export function LookupImportCard({ table, current, onImported, onReload }: { table: LookupTable; current: number; onImported: (table: LookupTable) => void; onReload: () => void }) {
  const [csv, setCsv] = useState("");
  const [mode, setMode] = useState<LookupImportMode>("merge");
  const [hasHeader, setHasHeader] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [outcome, setOutcome] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function chooseFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > MAX_IMPORT_BYTES) {
      setError("The file is larger than 2 MB; split it.");
      return;
    }
    void file.text().then((text) => {
      setCsv(text);
      setError(undefined);
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setOutcome(null);
    const problem = checkImportText(csv);
    setError(problem);
    if (problem) return;
    setBusy(true);
    try {
      const result = await importLookupEntries(table.id, { version: current, mode, hasHeader, csv });
      setOutcome(importOutcome(result));
      if (result.written) {
        setCsv("");
        onImported(result.table);
      }
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Import from CSV">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="lookup-import-file" label="CSV file" optional hint={IMPORT_HINTS[table.kind]}>
          {(control) => <input {...control} type="file" accept=".csv,text/csv,text/plain" onChange={chooseFile} />}
        </FormField>
        <FormField id="lookup-import-csv" label="CSV lines" error={error} hint="Or paste the lines here. At most 5000 entries in the table afterwards.">
          {(control) => <Textarea {...control} mono rows={8} spellCheck={false} value={csv} invalid={Boolean(error)} onChange={(event) => setCsv(event.target.value)} />}
        </FormField>
        <FormField id="lookup-import-mode" label="Entries not in the file">
          {(control) => (
            <Select {...control} value={mode} onChange={(event) => setMode(event.target.value as LookupImportMode)}>
              <option value="merge">Keep them (add and update only)</option>
              <option value="replace">Remove them (the file becomes the whole table)</option>
            </Select>
          )}
        </FormField>
        <div className={styles.check}>
          <input id="lookup-import-header" type="checkbox" checked={hasHeader} onChange={(event) => setHasHeader(event.target.checked)} />
          <label htmlFor="lookup-import-header">The first line is a header</label>
        </div>
        {failure ? (
          <div className={styles.formActions}>
            <Alert
              tone="danger"
              title="Could not import the file"
              live="alert"
              action={
                failure.code === "MIG-VERSION-CONFLICT" ? (
                  <Button size="sm" onClick={onReload}>
                    Reload the table
                  </Button>
                ) : undefined
              }
            >
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        {outcome ? (
          <div className={styles.formActions}>
            <Alert tone="success" title="Import finished" live="status">
              {outcome}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Import as version {current + 1}
          </Button>
        </div>
      </form>
    </Card>
  );
}
