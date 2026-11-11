import { useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { createVerification, describeProblem, listVerifications, newKey, type Problem, type VerificationSummary } from "./api/client";
import { UUID, VERIFICATION_LABELS, VERIFICATION_TONES } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export const verificationColumns: DataTableColumn<VerificationSummary>[] = [
  { key: "verifiedAt", header: "Checked", mono: true, render: (v) => <time dateTime={v.verifiedAt}>{v.verifiedAt}</time> },
  { key: "ledgerId", header: "Ledger", mono: true, render: (v) => <span title={v.ledgerId}>{v.ledgerId.slice(0, 8)}</span> },
  { key: "status", header: "Result", render: (v) => <Badge tone={VERIFICATION_TONES[v.status]}>{VERIFICATION_LABELS[v.status]}</Badge> },
  { key: "accountsCompared", header: "Accounts", mono: true, align: "end" },
  { key: "diffCount", header: "Differing", mono: true, align: "end" },
  { key: "hwmBefore", header: "As of sequence", mono: true, align: "end", render: (v) => (v.hwmBefore === v.hwmAfter ? String(v.hwmAfter) : `${v.hwmBefore} to ${v.hwmAfter}`) },
  { key: "requestedBy", header: "Requested by", mono: true },
];

/** Ledger verification (FR-15.1): checks that the read model still adds up to the ledger's trial balance. */
export function VerificationsPage() {
  const navigate = useNavigate();
  const [ledger, setLedger] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per ledger value, so a retried click after a timeout returns the check already stored.
  const attempt = useRef<{ ledger: string; key: string } | null>(null);
  const trimmed = ledger.trim().toLowerCase();
  const list = useLoad((signal) => listVerifications("", signal), "verifications");

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!UUID.test(trimmed)) {
      setError("Enter the ledger id as a UUID, such as 0190f0a0-0000-7000-8000-000000000201.");
      return;
    }
    setError(undefined);
    setFailure(null);
    if (attempt.current?.ledger !== trimmed) attempt.current = { ledger: trimmed, key: newKey() };
    setBusy(true);
    try {
      const check = await createVerification(trimmed, attempt.current.key);
      attempt.current = null;
      navigate(`/finreport/verifications/${check.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Ledger verification"
        description="Compares the net of every account in the reporting read model with the ledger's own trial balance. A difference means a statement would not tie to the ledger."
      />
      <Card title="Check a ledger">
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Check a ledger">
          <FormField id="fr-verify-ledger" label="Ledger id" error={error} hint="The check reads the ledger's trial balance, so it needs the ledger duty to read balances, and stores the result.">
            {(c) => <Input {...c} className={styles.mono} value={ledger} onChange={(e) => setLedger(e.target.value)} />}
          </FormField>
          {failure ? (
            <Alert tone="danger" title="The check could not run" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? "Checking…" : "Check ledger"}
            </Button>
          </div>
        </form>
      </Card>
      {list.error ? (
        <Alert tone="danger" title="Could not load the checks" live="alert" action={<Button size="sm" onClick={list.reload}>Try again</Button>}>
          <ProblemMessage problem={list.error} />
        </Alert>
      ) : (
        <DataTable
          label="Ledger checks"
          columns={verificationColumns}
          rows={list.data ?? []}
          getRowKey={(v) => v.id}
          loading={list.loading}
          empty={{ title: "No ledger has been checked yet", description: "Enter a ledger id above to compare its read model with the ledger." }}
          onRowOpen={(v) => navigate(`/finreport/verifications/${v.id}`)}
        />
      )}
    </div>
  );
}
