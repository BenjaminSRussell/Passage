import { useState, type ChangeEvent, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, importBankStatement, listBankStatementImports, type BankStatementBatch, type Payment, type Problem } from "./api/client";
import { dateTime, money, shortId, STATUS_LABELS, UUID } from "./format";
import { CommandFailure, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const MAX_BYTES = 3 * 1024 * 1024;

const batchColumns: DataTableColumn<BankStatementBatch>[] = [
  { key: "importedAt", header: "Imported", mono: true, render: (row) => dateTime(row.importedAt) },
  { key: "legalEntityId", header: "Legal entity", mono: true, render: (row) => <span title={row.legalEntityId}>{shortId(row.legalEntityId)}</span> },
  { key: "statementCount", header: "Statements", mono: true, align: "end" },
  { key: "paymentCount", header: "Payments", mono: true, align: "end" },
  { key: "duplicateCount", header: "Already imported", mono: true, align: "end" },
  { key: "skippedCount", header: "Not credits", mono: true, align: "end" },
  { key: "fileSha256", header: "File SHA-256", mono: true, render: (row) => <span title={row.fileSha256}>{row.fileSha256.slice(0, 12)}…</span> },
];

/** camt.053 imports (FR-30.5): each statement file once, one payment per booked customer credit. */
export function BankStatementsPage() {
  const state = useLoad((signal) => listBankStatementImports(signal), "statements");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Bank statements"
        description="camt.053 statements from the bank, newest first. Each booked customer credit becomes a Received payment to match and apply."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("primary")} to="/receivables/bank-statements/new">
              Import statement
            </Link>
            <Link className={buttonClassName("secondary")} to="/receivables/payments">
              Payments
            </Link>
          </div>
        }
      />
      {state.error ? (
        <LoadFailure title="Could not load the bank statement imports" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Bank statement imports"
          columns={batchColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No statements", description: "Import the bank's camt.053 file to create one payment per customer credit." }}
        />
      )}
    </div>
  );
}

type Errors = Record<string, string>;

export interface StatementDraft {
  legalEntityId: string;
  content: string;
}

export function validateStatement(v: StatementDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.content.trim() === "") errors.content = "Choose the camt.053 file or paste its XML.";
  else if (new Blob([v.content]).size > MAX_BYTES) errors.content = "The file is larger than 3 MB; ask the bank for daily statements.";
  return errors;
}

const paymentColumns: DataTableColumn<Payment>[] = [
  { key: "sourceRef", header: "Statement entry", mono: true, render: (row) => row.sourceRef ?? "None" },
  { key: "payerName", header: "Payer", render: (row) => row.payerName ?? "Not given" },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "remittance", header: "Remittance", mono: true, render: (row) => (row.remittance.length > 0 ? row.remittance.map((r) => r.reference).join(", ") : "None") },
  { key: "status", header: "Status", render: (row) => STATUS_LABELS[row.status] ?? row.status },
];

/** Import a camt.053 file. A repeated file is refused (AR-0003); a malformed one names its line (AR-0004). */
export function BankStatementImportPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<StatementDraft>({ legalEntityId: "", content: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<BankStatementBatch | null>(null);
  const [busy, setBusy] = useState(false);

  function update(patch: Partial<StatementDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  function chooseFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > MAX_BYTES) {
      setErrors((prev) => ({ ...prev, content: "The file is larger than 3 MB; ask the bank for daily statements." }));
      return;
    }
    void file.text().then((content) => update({ content }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateStatement(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setResult(await importBankStatement({ legalEntityId: values.legalEntityId.trim(), content: values.content }));
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Import bank statement"
        description="Booked customer credits (CdtDbtInd CRDT) become unidentified payments. Debits, pending entries and reversals are skipped; an entry already imported from an earlier statement is not created twice."
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/bank-statements">
            Bank statements
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="bs-entity" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <EntityLookup entity="legalEntity" {...c} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="bs-file" label="camt.053 file" optional hint="Or paste the XML below.">
            {(c) => <Input {...c} type="file" accept=".xml,.053,application/xml,text/xml" onChange={chooseFile} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="bs-content" label="Statement XML" error={errors.content}>
              {(c) => <Textarea {...c} mono rows={12} spellCheck={false} value={values.content} onChange={(e) => update({ content: e.target.value })} invalid={Boolean(errors.content)} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title={failure.code === "AR-0003" ? "This file was already imported" : "Could not import the statement"} problem={failure} />
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
          <Alert tone="success" title="Statement imported" live="status">
            {`${result.paymentCount} payment(s) from ${result.statementCount} statement(s); ${result.duplicateCount} already imported, ${result.skippedCount} entries not customer credits. Match each payment from its page.`}
          </Alert>
          <Card title="Payments created" headingLevel={3}>
            <DataTable
              label="Payments created"
              columns={paymentColumns}
              rows={result.payments ?? []}
              getRowKey={(row) => row.id}
              empty={{ title: "No new payments", description: "The file has no booked customer credits that were not imported before." }}
              onRowOpen={(row) => navigate(`/receivables/payments/${row.id}`)}
            />
          </Card>
        </>
      ) : null}
    </div>
  );
}
