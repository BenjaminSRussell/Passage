import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { describeProblem, importPriceJournalCsv, newKey, type Problem } from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

/** The service's limit on one upload (maxJournalCSVBytes). */
export const MAX_CSV_BYTES = 32 * 1024 * 1024;
/** How many row faults the page lists before summarising the rest. */
const SHOWN_FAULTS = 20;

export const CSV_COLUMNS =
  "op, recordId, expectedVersion, adjustPct, type, customerKind, customerKey, productKind, productKey, variant, currency, unit, fromQuantity, amount, discPct1, discPct2, validFrom, validTo, searchNext, exclusive, campaignId";

export interface ImportDraft {
  legalEntityId: string;
  name: string;
  smartRound: boolean;
  file: File | null;
}

type Errors = Partial<Record<"legalEntityId" | "name" | "file", string>>;

export function validateImport(v: ImportDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!v.file) errors.file = "Choose a CSV file.";
  else if (v.file.size > MAX_CSV_BYTES) errors.file = "The file is larger than 32 MB. Split it into several journals.";
  return errors;
}

/**
 * Turns the service's field keys into lines a person can act on: rows[n].column is data row n
 * of the file, lines[i].field is journal line i + 1 (the same row).
 */
export function describeFaults(fields: Record<string, string>): string[] {
  const out: { order: number; text: string }[] = [];
  for (const [key, message] of Object.entries(fields)) {
    const row = /^rows\[(\d+)\]\.(.+)$/.exec(key);
    const line = /^lines\[(\d+)\]\.?(.*)$/.exec(key);
    if (row) out.push({ order: Number(row[1]), text: `Row ${row[1]}, ${row[2]}: ${message}` });
    else if (line) out.push({ order: Number(line[1]) + 1, text: `Row ${Number(line[1]) + 1}${line[2] ? `, ${line[2]}` : ""}: ${message}` });
    else if (key === "csv.header") out.push({ order: -1, text: `Header: ${message}` });
    else out.push({ order: 0, text: key === "csv" || key === "lines" ? message : `${key}: ${message}` });
  }
  return out.sort((a, b) => a.order - b.order).map((f) => f.text);
}

function readText(file: File): Promise<string> {
  if (typeof file.text === "function") return file.text();
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ""));
    reader.onerror = () => reject(reader.error ?? new Error("The file could not be read"));
    reader.readAsText(file);
  });
}

/** Price journal CSV import (PRD §5.26.6 "import CSV (≤ 100k rows)"): the file becomes an OPEN journal. */
export function ImportJournalPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ImportDraft>({ legalEntityId: "", name: "", smartRound: false, file: null });
  const [errors, setErrors] = useState<Errors>({});
  const [faults, setFaults] = useState<string[]>([]);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ImportDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateImport(values);
    setErrors(found);
    setFaults([]);
    setFailure(null);
    if (Object.keys(found).length > 0 || !values.file) return;
    setBusy(true);
    try {
      const csv = await readText(values.file);
      const created = await importPriceJournalCsv({ legalEntityId: values.legalEntityId.trim(), name: values.name.trim(), smartRound: values.smartRound }, csv, key.current);
      navigate(`/pricing/journals/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const { legalEntityId, name, ...rest } = problem.fields;
      const fieldErrors: Errors = {};
      if (legalEntityId) fieldErrors.legalEntityId = legalEntityId;
      if (name) fieldErrors.name = name;
      setErrors(fieldErrors);
      const listed = describeFaults(rest);
      setFaults(listed);
      if (listed.length === 0 && Object.keys(fieldErrors).length === 0) setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Import price journal"
        description="Upload a CSV file of up to 100,000 rows. It is saved as an open journal: validate it to see every line's before and after values, then post it in one step."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="pi-legal-entity" label="Legal entity id" error={errors.legalEntityId}>
            {(c) => (
              <Input {...c} className={styles.mono} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />
            )}
          </FormField>
          <FormField id="pi-name" label="Journal name" error={errors.name}>
            {(c) => <Input {...c} maxLength={120} value={values.name} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="pi-file" label="CSV file" hint={`First row: the column names, op first. Columns: ${CSV_COLUMNS}.`} error={errors.file}>
              {(c) => (
                <Input
                  {...c}
                  type="file"
                  accept=".csv,text/csv"
                  onChange={(e) => update({ file: e.target.files?.[0] ?? null })}
                  invalid={Boolean(errors.file)}
                />
              )}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="pi-smart" type="checkbox" checked={values.smartRound} onChange={(e) => update({ smartRound: e.target.checked })} />
              <label htmlFor="pi-smart">Smart rounding</label>
            </span>
            <span className={styles.note}>
              CREATE rows carry the whole agreement; UPDATE rows take recordId, expectedVersion and adjustPct (or the whole agreement); DELETE rows take recordId and
              expectedVersion.
            </span>
          </div>
          {faults.length > 0 ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The file was not imported" live="alert">
                <ul className={styles.faults}>
                  {faults.slice(0, SHOWN_FAULTS).map((f) => (
                    <li key={f}>{f}</li>
                  ))}
                </ul>
                {faults.length > SHOWN_FAULTS ? <p className={styles.note}>{faults.length - SHOWN_FAULTS} more problems are not shown.</p> : null}
              </Alert>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not import the journal" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Import journal
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/journals">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
