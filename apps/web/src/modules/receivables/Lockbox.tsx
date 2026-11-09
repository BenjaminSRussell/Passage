import { useState, type ChangeEvent, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, importLockbox, listLockboxBatches, type LockboxBatch, type Payment, type Problem } from "./api/client";
import { dateTime, DAY, money, shortId, STATUS_LABELS, today, UUID } from "./format";
import { CommandFailure, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const MAX_BYTES = 3 * 1024 * 1024;
const CREDIT_CODE = /^[1-3]\d{2}$/;

const batchColumns: DataTableColumn<LockboxBatch>[] = [
  { key: "importedAt", header: "Imported", mono: true, render: (row) => dateTime(row.importedAt) },
  { key: "receivedDate", header: "Received", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true, render: (row) => <span title={row.legalEntityId}>{shortId(row.legalEntityId)}</span> },
  { key: "accountCount", header: "Accounts", mono: true, align: "end" },
  { key: "paymentCount", header: "Checks", mono: true, align: "end" },
  { key: "fileSha256", header: "File SHA-256", mono: true, render: (row) => <span title={row.fileSha256}>{row.fileSha256.slice(0, 12)}…</span> },
];

/** Lockbox imports (FR-30.3): each bank file once, one payment per check. */
export function LockboxPage() {
  const state = useLoad((signal) => listLockboxBatches(signal), "batches");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Lockbox imports"
        description="BAI2 files from the bank, newest first. Each check becomes a Received payment to match and apply."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("primary")} to="/receivables/lockbox/new">
              Import file
            </Link>
            <Link className={buttonClassName("secondary")} to="/receivables/payments">
              Payments
            </Link>
          </div>
        }
      />
      {state.error ? (
        <LoadFailure title="Could not load the lockbox imports" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Lockbox imports"
          columns={batchColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No lockbox files", description: "Import the bank's BAI2 file to create one payment per check." }}
        />
      )}
    </div>
  );
}

type Errors = Record<string, string>;

export interface LockboxDraft {
  legalEntityId: string;
  receivedDate: string;
  typeCodes: string;
  content: string;
}

export function validateLockbox(v: LockboxDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!DAY.test(v.receivedDate)) errors.receivedDate = "Enter the date the checks arrived.";
  if (v.content.trim() === "") errors.content = "Choose the BAI2 file or paste its content.";
  else if (new Blob([v.content]).size > MAX_BYTES) errors.content = "The file is larger than 3 MB; split it.";
  const codes = splitCodes(v.typeCodes);
  if (codes.some((c) => !CREDIT_CODE.test(c))) errors.typeCodes = "Use three-digit BAI2 credit codes from 100 to 399, separated by commas.";
  return errors;
}

function splitCodes(text: string): string[] {
  return text
    .split(",")
    .map((c) => c.trim())
    .filter((c) => c !== "");
}

const paymentColumns: DataTableColumn<Payment>[] = [
  { key: "sourceRef", header: "Check reference", mono: true, render: (row) => row.sourceRef ?? "None" },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
  { key: "remittance", header: "Remittance", mono: true, render: (row) => (row.remittance.length > 0 ? row.remittance.map((r) => r.reference).join(", ") : "None") },
  { key: "status", header: "Status", render: (row) => STATUS_LABELS[row.status] ?? row.status },
];

/** Import a BAI2 lockbox file. A repeated file is refused (AR-0003); a malformed one names its line (AR-0004). */
export function LockboxImportPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<LockboxDraft>({ legalEntityId: "", receivedDate: today(), typeCodes: "", content: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<LockboxBatch | null>(null);
  const [busy, setBusy] = useState(false);

  function update(patch: Partial<LockboxDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  function chooseFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > MAX_BYTES) {
      setErrors((prev) => ({ ...prev, content: "The file is larger than 3 MB; split it." }));
      return;
    }
    void file.text().then((content) => update({ content }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateLockbox(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const codes = splitCodes(values.typeCodes);
      setResult(
        await importLockbox({
          legalEntityId: values.legalEntityId.trim(),
          receivedDate: values.receivedDate,
          content: values.content,
          ...(codes.length > 0 ? { typeCodes: codes } : {}),
        }),
      );
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
        title="Import lockbox file"
        description="Each check credit (BAI2 type 116 unless the bank uses other codes) becomes an unidentified check payment. Match and apply each one from its page."
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/lockbox">
            Lockbox imports
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="lb-entity" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <EntityLookup entity="legalEntity" {...c} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="lb-date" label="Received date" error={errors.receivedDate}>
            {(c) => <Input {...c} type="date" value={values.receivedDate} onChange={(e) => update({ receivedDate: e.target.value })} invalid={Boolean(errors.receivedDate)} />}
          </FormField>
          <FormField id="lb-codes" label="Check type codes" optional hint="Leave empty for 116. Separate codes with commas." error={errors.typeCodes}>
            {(c) => <Input {...c} className={styles.mono} value={values.typeCodes} onChange={(e) => update({ typeCodes: e.target.value })} invalid={Boolean(errors.typeCodes)} />}
          </FormField>
          <FormField id="lb-file" label="BAI2 file" optional hint="Or paste the content below.">
            {(c) => <Input {...c} type="file" accept=".bai,.bai2,.txt,text/plain" onChange={chooseFile} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="lb-content" label="File content" error={errors.content}>
              {(c) => <Textarea {...c} mono rows={10} spellCheck={false} value={values.content} onChange={(e) => update({ content: e.target.value })} invalid={Boolean(errors.content)} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title={failure.code === "AR-0003" ? "This file was already imported" : "Could not import the file"} problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Import file
            </Button>
          </div>
        </form>
      </Card>
      {result ? (
        <>
          <Alert tone="success" title="File imported" live="status">
            {result.paymentCount} check payment(s) from {result.accountCount} account(s). They wait on the payments page for matching.
          </Alert>
          <Card title="Payments created" headingLevel={3}>
            <DataTable
              label="Payments created"
              columns={paymentColumns}
              rows={result.payments ?? []}
              getRowKey={(row) => row.id}
              empty={{ title: "No checks", description: "The file has no credits with the check type codes." }}
              onRowOpen={(row) => navigate(`/receivables/payments/${row.id}`)}
            />
          </Card>
        </>
      ) : null}
    </div>
  );
}
