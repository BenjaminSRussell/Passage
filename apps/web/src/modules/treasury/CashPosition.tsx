import { useState, type ChangeEvent, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Badge, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getCashPosition,
  importIntradayReport,
  listBankAccounts,
  type AccountPositionRow,
  type BankAccount,
  type CurrencyTotal,
  type Problem,
} from "./api/client";
import { dateTime, UUID } from "./format";
import { CommandFailure, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const SOURCES: Record<AccountPositionRow["source"], string> = {
  "camt.052": "Intraday report",
  "camt.053": "Bank statement",
  opening: "Opening balance from the migration",
  none: "No bank file yet",
};

const IMPORT_FAILURES: Record<string, string> = {
  "TRS-0408": "The report currency is not the account currency",
  "TRS-0409": "The file's account is not this bank account's IBAN or statement account number",
  "TRS-0412": "The bank account is not active",
  "TRS-0493": "The file is not a readable camt.052 report",
  "TRS-0525": "A report with this id was imported with other balances",
};

const totalColumns: DataTableColumn<CurrencyTotal>[] = [
  { key: "currency", header: "Currency", mono: true },
  { key: "booked", header: "Booked", mono: true, align: "end" },
  { key: "available", header: "Available", mono: true, align: "end" },
];

/** Cash position (FR-36.8): booked and available balance per bank account from the freshest bank file, with totals per currency. */
export function CashPositionPage() {
  const [params, setParams] = useSearchParams();
  const legalEntityId = params.get("legalEntityId") ?? "";
  const chosen = UUID.test(legalEntityId);
  const position = useLoad((signal) => (chosen ? getCashPosition(legalEntityId, signal) : Promise.resolve(null)), legalEntityId);
  const accounts = useLoad((signal) => (chosen ? listBankAccounts(legalEntityId, signal) : Promise.resolve([] as BankAccount[])), legalEntityId);
  const [accountId, setAccountId] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(accountId)) found.accountId = "Choose the bank account the report is for.";
    if (!file) found.file = "Choose the camt.052 file from the bank.";
    setErrors(found);
    setFailure(null);
    setNotice(null);
    if (Object.keys(found).length > 0 || !file) return;
    setBusy(true);
    try {
      const result = await importIntradayReport(accountId, file, file.name);
      setNotice(result.replayed ? "This report was already imported; nothing was added." : `Report ${result.report.reportId} imported.`);
      setFile(null);
      position.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<AccountPositionRow>[] = [
    { key: "accountCode", header: "Bank account", render: (r) => `${r.accountCode} (${r.ibanMasked})` },
    { key: "source", header: "From", render: (r) => SOURCES[r.source] },
    { key: "booked", header: "Booked", mono: true, align: "end", render: (r) => `${r.booked} ${r.currency}` },
    { key: "available", header: "Available", mono: true, align: "end", render: (r) => `${r.available} ${r.currency}` },
    { key: "asOf", header: "As of", render: (r) => (r.asOf ? dateTime(r.asOf) : "Not known") },
    { key: "stale", header: "Freshness", render: (r) => <Badge tone={r.stale ? "warning" : "success"}>{r.stale ? "Out of date" : "Current"}</Badge> },
  ];

  const data = position.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cash position"
        description="What each bank account holds right now, from the newest bank file: an intraday report (camt.052) or the last statement. A figure older than a day is marked out of date."
      />
      <Card>
        <div className={styles.toolbar}>
          <FormField id="cp-entity" label="Legal entity">
            {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => setParams(e.target.value ? { legalEntityId: e.target.value } : {})} />}
          </FormField>
        </div>
      </Card>
      {!chosen ? (
        <Card>
          <EmptyState icon="database" title="Choose a legal entity" description="The position is computed per legal entity." />
        </Card>
      ) : position.error ? (
        <LoadFailure title="Could not load the cash position" problem={position.error} retry={position.reload} />
      ) : (
        <>
          <DataTable
            label="Cash position by bank account"
            columns={columns}
            rows={data?.accounts ?? []}
            getRowKey={(r) => r.bankAccountId}
            loading={position.loading && !data}
            empty={{ title: "No active bank accounts", description: "Activate a bank account, then import its statements or intraday reports." }}
          />
          {data && data.totals.length > 0 ? (
            <Card title="Total by currency" headingLevel={3}>
              <DataTable label="Totals by currency" columns={totalColumns} rows={data.totals} getRowKey={(t) => t.currency} empty={{ title: "No totals", description: "" }} />
            </Card>
          ) : null}
          <Card title="Import an intraday report" headingLevel={3}>
            <form className={styles.form} onSubmit={submit} noValidate>
              <FormField id="cp-account" label="Bank account" error={errors.accountId}>
                {(c) => (
                  <Select {...c} value={accountId} onChange={(e) => setAccountId(e.target.value)} invalid={Boolean(errors.accountId)}>
                    <option value="">Choose a bank account</option>
                    {(accounts.data ?? []).map((a) => (
                      <option key={a.id} value={a.id}>{`${a.accountCode} (${a.ibanMasked}, ${a.currency})`}</option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="cp-file" label="Report file" hint="camt.052 XML, one account per file." error={errors.file}>
                {(c) => <Input {...c} type="file" accept=".xml,application/xml,text/xml" onChange={(e: ChangeEvent<HTMLInputElement>) => setFile(e.target.files?.[0] ?? null)} invalid={Boolean(errors.file)} />}
              </FormField>
              {failure ? (
                <div className={styles.formActions}>
                  <CommandFailure title={IMPORT_FAILURES[failure.code] ?? "Could not import the report"} problem={failure} />
                </div>
              ) : null}
              {notice ? (
                <Alert tone="success" title={notice} live="status">
                  The position above uses it when it is newer than the last statement.
                </Alert>
              ) : null}
              <div className={styles.formActions}>
                <Button type="submit" variant="primary" loading={busy}>
                  Import report
                </Button>
              </div>
            </form>
          </Card>
        </>
      )}
    </div>
  );
}
