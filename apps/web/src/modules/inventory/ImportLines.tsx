import { problemSummary } from "../../shell/errors";
import { useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Textarea } from "../../shell/ui";
import { addLines, describeProblem, type Journal, type JournalType, type Problem } from "./api/client";
import { parseLinesCsv, type CsvImport, type CsvLineValues, MAX_IMPORT_ROWS } from "./csvLines";
import styles from "./pages.module.css";

const TEMPLATE = "product\tqty\tunit\tcost\tsite\twarehouse\tlocation\tbatch\tserial\n";

/** Server field errors name the line as lines[i].field; i is the data row, so row i + 2 of the sheet. */
const LINE_FIELD = /^lines\[(\d+)\]\.(.+)$/;

/** The service's per-line refusals by sheet row, so the user knows which rows to fix. */
function rowsOf(problem: Problem): string[] {
  return Object.entries(problem.fields)
    .map(([field, message]) => {
      const m = LINE_FIELD.exec(field);
      return m ? { row: Number(m[1]) + 2, text: `Row ${Number(m[1]) + 2}: ${m[2]} ${message}` } : { row: 0, text: `${field} ${message}` };
    })
    .sort((a, b) => a.row - b.row)
    .map((e) => e.text);
}

/**
 * Import of journal lines from a spreadsheet paste or a CSV file (PRD 3.16.13
 * "CSV import, paste from spreadsheet"). The text is checked against the same
 * rules as the add-line form first; a file with any bad row adds nothing. The
 * valid lines then go to lines:bulk in one request, which the service applies
 * in one transaction, so a refusal there adds nothing either. Each row carries
 * a client key kept until the text changes or the import succeeds: importing
 * again after a lost response replays the rows the service already holds
 * instead of adding them twice.
 */
export function ImportLinesForm({
  journal,
  validate,
  onAdded,
}: {
  journal: Journal;
  validate(values: CsvLineValues, type: JournalType): Record<string, string | undefined>;
  onAdded(updated: Journal): void;
}) {
  const [text, setText] = useState("");
  const [result, setResult] = useState<CsvImport | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const batch = useRef<string | null>(null);

  function changeText(value: string) {
    setText(value);
    setResult(null);
    setFailure(null);
    setNotice("");
    batch.current = null;
  }

  async function readFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    changeText(await file.text());
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice("");
    const parsed = parseLinesCsv(text, journal.journalType, validate);
    setResult(parsed);
    if (parsed.headerError || parsed.errors.length > 0) return;
    const prefix = (batch.current ??= crypto.randomUUID());
    setBusy(true);
    try {
      const updated = await addLines(
        journal.id,
        parsed.lines.map((line, i) => ({ ...line, clientKey: `${prefix}-${i}` })),
      );
      const count = parsed.lines.length;
      batch.current = null;
      setText("");
      setResult(null);
      setNotice(`Imported ${count} ${count === 1 ? "line" : "lines"}.`);
      onAdded(updated);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Import lines">
      <form onSubmit={submit} noValidate className={styles.form}>
        <p className={`${styles.muted} ${styles.formActions}`}>
          Paste rows from a spreadsheet or choose a CSV file. The first row names the columns: product, qty and site are required; unit, cost, warehouse, location, batch, serial and status
          are optional{journal.journalType === "TRANSFER" ? "; a transfer also needs toSite and may name toWarehouse, toLocation and toSegment" : ""}
          {journal.journalType === "OWNERSHIP_CHANGE" ? "; an ownership change also needs owner" : ""}. Up to {MAX_IMPORT_ROWS} lines at a time; nothing is added if any row is wrong.
        </p>
        <FormField id="import-file" label="CSV file" optional>
          {(control) => <input {...control} type="file" accept=".csv,.tsv,.txt,text/csv" onChange={readFile} />}
        </FormField>
        <FormField id="import-text" label="Rows" hint={`Example header: ${TEMPLATE.trim().replace(/\t/g, ", ")}`}>
          {(control) => (
            <Textarea
              {...control}
              rows={8}
              value={text}
              onChange={(event) => changeText(event.target.value)}
            />
          )}
        </FormField>
        {result?.headerError ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="The rows cannot be read" live="alert">
              {result.headerError}
            </Alert>
          </div>
        ) : null}
        {result && result.errors.length > 0 ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title={`${result.errors.length} ${result.errors.length === 1 ? "row needs" : "rows need"} fixing; nothing was added`} live="alert">
              <ul>
                {result.errors.slice(0, 20).map((e) => (
                  <li key={e.row}>
                    Row {e.row}: {e.messages.join(" ")}
                  </li>
                ))}
                {result.errors.length > 20 ? <li>And {result.errors.length - 20} more.</li> : null}
              </ul>
            </Alert>
          </div>
        ) : null}
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Nothing was imported" live="alert">
              {problemSummary(failure)} No line was added; fix the rows and import again.
              {rowsOf(failure).length > 0 ? (
                <ul>
                  {rowsOf(failure)
                    .slice(0, 20)
                    .map((line) => (
                      <li key={line}>{line}</li>
                    ))}
                </ul>
              ) : null}
            </Alert>
          </div>
        ) : null}
        <Alert tone="success" live="status">
          {notice}
        </Alert>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy} disabled={text.trim() === ""}>
            Import lines
          </Button>
        </div>
      </form>
    </Card>
  );
}
