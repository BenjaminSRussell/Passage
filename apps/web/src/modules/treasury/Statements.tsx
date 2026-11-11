import { useState, type ChangeEvent, type FormEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getStatement,
  importStatement,
  listStatementLines,
  type ImportResult,
  type Problem,
  type Statement,
  type StatementLine,
} from "./api/client";
import { dateTime, money, UUID } from "./format";
import { CommandFailure, Detail, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

/** The service's default upload limit (TREASURY_MAX_FILE_MB = 50); larger files get TRS-0490 anyway. */
export const MAX_BYTES = 50 * 1024 * 1024;
const LINE_PAGE = 100;
/** File pickers filter on these; the service decides the format from the content, not the name. */
export const STATEMENT_ACCEPT = ".xml,.053,.bai,.bai2,.940,.sta,.mt940,.txt,application/xml,text/xml,text/plain";

type Errors = Record<string, string>;

export interface ImportDraft {
  bankAccountId: string;
  file: File | null;
}

export function validateImport(v: ImportDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.bankAccountId.trim())) errors.bankAccountId = "Choose the bank account.";
  if (!v.file) errors.file = "Choose the camt.053, BAI2 or MT940 file from the bank.";
  else if (v.file.size > MAX_BYTES) errors.file = "The file is larger than 50 MB; ask the bank for daily statements.";
  return errors;
}

/** Titles for the import refusals a user can act on (README error table). */
const IMPORT_FAILURES: Record<string, string> = {
  "TRS-0406": "A statement with this id was already imported with another closing balance",
  "TRS-0408": "The statement currency is not the account currency",
  "TRS-0409": "The file's account is not this bank account's IBAN or statement account number",
  "TRS-0412": "The bank account is not active",
  "TRS-0490": "The file is too large",
  "TRS-0493": "The file is not a readable camt.053, BAI2 or MT940 statement",
  "TRS-0494": "The statement balances do not add up",
};

/** Upload one camt.053, BAI2 or MT940 file (FR-36.2); the service detects the format. The same file again returns the stored statement (BR-36.8). */
export function ImportStatementPage() {
  const [params] = useSearchParams();
  const [values, setValues] = useState<ImportDraft>({ bankAccountId: params.get("bankAccountId") ?? "", file: null });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [busy, setBusy] = useState(false);

  function chooseFile(event: ChangeEvent<HTMLInputElement>) {
    setValues((prev) => ({ ...prev, file: event.target.files?.[0] ?? null }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateImport(values);
    setErrors(found);
    setFailure(null);
    setResult(null);
    if (Object.keys(found).length > 0 || !values.file) return;
    setBusy(true);
    try {
      setResult(await importStatement(values.bankAccountId.trim(), values.file, values.file.name));
    } catch (error) {
      const problem = describeProblem(error);
      // Parse errors say where the file failed: "line" of a BAI2 or MT940 file, or the camt.053 "entry".
      // Everything else keyed by field belongs next to that field.
      const { line, entry, ...fields } = problem.fields;
      const where = line ? ` (line ${line})` : entry ? ` (entry ${entry})` : "";
      if (Object.keys(fields).length > 0) setErrors(fields);
      else setFailure({ ...problem, message: `${problem.message}${where}` });
    } finally {
      setBusy(false);
    }
  }

  const accountId = values.bankAccountId.trim();
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Import bank statement"
        description="Upload the bank's camt.053, BAI2 or MT940 file (one statement per file); the format is recognised from the content. Opening balance plus the entries must equal the closing balance; every entry is kept with its source record for audit."
        actions={
          UUID.test(accountId) ? (
            <Link className={buttonClassName("secondary")} to={`/treasury/bank-accounts/${accountId}`}>
              Bank account
            </Link>
          ) : (
            <Link className={buttonClassName("secondary")} to="/treasury">
              Bank accounts
            </Link>
          )
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupFormField
            id="st-account"
            label="Bank account"
            entity="bankAccount"
            value={values.bankAccountId}
            error={errors.bankAccountId}
            onChange={(bankAccountId) => setValues((prev) => ({ ...prev, bankAccountId }))}
          />
          <FormField id="st-file" label="Statement file" hint="camt.053 XML, BAI2 or MT940 text, up to 50 MB." error={errors.file}>
            {(c) => (
              <Input
                {...c}
                type="file"
                accept={STATEMENT_ACCEPT}
                onChange={chooseFile}
                invalid={Boolean(errors.file)}
              />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title={IMPORT_FAILURES[failure.code] ?? "Could not import the statement"} problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Import statement
            </Button>
          </div>
        </form>
      </Card>
      {result ? (
        <>
          <Alert tone={result.replayed ? "info" : "success"} title={result.replayed ? "Already imported" : "Statement imported"} live="status">
            {result.replayed
              ? `This file was imported before as statement ${result.statement.statementId}; nothing was added (TRS-0405).`
              : `Statement ${result.statement.statementId}: ${result.statement.lineCount} entries, closing balance ${money(result.statement.closingBalance, result.statement.currency)}.`}
          </Alert>
          {result.statement.warnings.length > 0 ? (
            <Alert tone="warning" title="Check the statement sequence">
              <ul className={styles.list}>
                {result.statement.warnings.map((w) => (
                  <li key={w.code + w.detail}>
                    {w.detail} ({w.code})
                  </li>
                ))}
              </ul>
            </Alert>
          ) : null}
          <div className={styles.actions}>
            <Link className={buttonClassName("primary")} to={`/treasury/statements/${result.statement.id}`}>
              Open statement
            </Link>
          </div>
        </>
      ) : null}
    </div>
  );
}

const lineColumns: DataTableColumn<StatementLine>[] = [
  { key: "lineNo", header: "Line", mono: true, align: "end" },
  { key: "bookingDate", header: "Booked", mono: true },
  { key: "valueDate", header: "Value date", mono: true },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "counterpartyName", header: "Counterparty", render: (row) => row.counterpartyName || "Not given" },
  { key: "reference", header: "Reference", mono: true, render: (row) => row.endToEndRef || row.reference || "None" },
  { key: "btcCode", header: "Bank code", mono: true, render: (row) => row.btcCode || "None" },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

interface StatementView {
  statement: Statement;
  lines: StatementLine[];
  next: number | null;
}

/** One statement: balances and its entries, a page at a time in line order. */
export function StatementDetailPage() {
  const { id = "" } = useParams();
  // A new id starts with fresh paging state.
  return <StatementDetail key={id} id={id} />;
}

function StatementDetail({ id }: { id: string }) {
  const state = useLoad(async (signal): Promise<StatementView> => {
    const [statement, page] = await Promise.all([getStatement(id, signal), listStatementLines(id, 0, LINE_PAGE, signal)]);
    return { statement, lines: page.items, next: page.nextAfterLineNo };
  }, id);
  const [more, setMore] = useState<{ lines: StatementLine[]; next: number | null } | null>(null);
  const [moreFailure, setMoreFailure] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Bank statement" />
        <LoadFailure title="Could not load the statement" problem={state.error} retry={state.reload} />
      </div>
    );
  }
  const view = state.data;
  if (!view) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Bank statement" />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  const st = view.statement;
  const lines = more ? [...view.lines, ...more.lines] : view.lines;
  const next = more ? more.next : view.next;

  async function loadMore(after: number) {
    setLoadingMore(true);
    setMoreFailure(null);
    try {
      const page = await listStatementLines(id, after, LINE_PAGE);
      setMore((prev) => ({ lines: [...(prev?.lines ?? []), ...page.items], next: page.nextAfterLineNo }));
    } catch (error) {
      setMoreFailure(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Statement ${st.statementId}`}
        description={`${st.format}, imported ${dateTime(st.createdAt)}.`}
        actions={
          <Link className={buttonClassName("secondary")} to={`/treasury/bank-accounts/${st.bankAccountId}`}>
            Bank account
          </Link>
        }
      />
      <Card title="Balances" headingLevel={3}>
        <dl className={styles.details}>
          <Detail term="Opening balance" mono>
            {`${money(st.openingBalance, st.currency)} on ${st.openingDate}`}
          </Detail>
          <Detail term="Closing balance" mono>
            {`${money(st.closingBalance, st.currency)} on ${st.closingDate}`}
          </Detail>
          <Detail term="Entries" mono>
            {String(st.lineCount)}
          </Detail>
          <Detail term="Sequence" mono>
            {st.sequence == null ? "None" : String(st.sequence)}
          </Detail>
        </dl>
      </Card>
      <Card title="Entries" headingLevel={3}>
        <div className={styles.stack}>
          <DataTable
            label="Statement entries"
            columns={lineColumns}
            rows={lines}
            getRowKey={(row) => String(row.lineNo)}
            empty={{ title: "No entries", description: "The bank reported balances only for this day." }}
          />
          <p className={styles.note} role="status">
            {`Showing ${lines.length} of ${st.lineCount} entries.`}
          </p>
          {moreFailure ? <CommandFailure title="Could not load more entries" problem={moreFailure} /> : null}
          {next !== null ? (
            <div className={styles.actions}>
              <Button loading={loadingMore} onClick={() => void loadMore(next)}>
                Load more entries
              </Button>
            </div>
          ) : null}
        </div>
      </Card>
    </div>
  );
}
